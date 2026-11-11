import React, {useContext, useEffect, useId, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {GCPConsoleContext} from './gcp-console-shell.jsx';
import './gcp-storage.css';
import './gcp-appengine.css';

// App Engine view. Every request uses the actual App Engine Admin API v1
// paths on this simulator; the capability document only names the endpoint,
// project, locations and data-plane template. Authority stays on the server.
const segment = value => encodeURIComponent(String(value));
export function appURL(endpoint, project, ...parts) {
  return endpoint + 'v1/apps/' + segment(project) + parts.map(p => '/' + segment(p)).join('');
}
export function versionHost(version, service, project) { return version + '-dot-' + service + '-dot-' + project + '.appspot.com'; }
// Same-origin paths only: the template comes from capabilities and the host
// segment is percent-encoded, so a link never carries another scheme, origin
// or path segment.
export function dataPlaneURL(template, host) {
  const value = String(template || '');
  return value.startsWith('/') && !value.startsWith('//') && value.includes('{hostname}') ? value.replace('{hostname}', encodeURIComponent(host)) : '#';
}
export function percent(fraction) { return Math.round(Number(fraction) * 1000) / 10 + '%'; }
export function splitText(split) {
  const allocations = split?.allocations || {};
  return Object.keys(allocations).sort().map(v => v + ' ' + percent(allocations[v])).join(', ');
}
// KEY=VALUE lines; blank lines are ignored. Values may contain "=".
export function parseEnvironment(text) {
  const env = {};
  for (const raw of String(text || '').split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    const at = line.indexOf('=');
    if (at < 1) throw Error('Use one KEY=VALUE pair per line.');
    const key = line.slice(0, at).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw Error('Environment variable names use letters, digits and underscores.');
    env[key] = line.slice(at + 1);
  }
  return env;
}
export function notFound(error) { return /^404:/.test(error?.message || ''); }
export const DEFAULT_RULE = 2147483647;
export function deployBody(id, sourceUrl, environment) {
  const body = {id, runtime: 'python312', deployment: {zip: {sourceUrl}}};
  if (Object.keys(environment).length) body.envVariables = environment;
  return body;
}

export default function GCPAppEngineView({request, appEngine}) {
  const {t} = useLocale();
  const ids = useId();
  const endpoint = appEngine?.endpoint || '', project = appEngine?.project || '';
  const locations = appEngine?.locations || [];
  const [app, setApp] = useState({state: 'loading'});
  const [services, setServices] = useState([]), [service, setService] = useState('default');
  const [versions, setVersions] = useState([]), [rules, setRules] = useState([]);
  const [location, setLocation] = useState(locations[0]?.locationId || '');
  const [deploy, setDeploy] = useState({service: 'default', id: '', source: '', env: ''});
  const [rule, setRule] = useState({priority: '', action: 'DENY', sourceRange: '', description: ''});
  const [pending, setPending] = useState(''), [loading, setLoading] = useState(false), [busy, setBusy] = useState(false);
  const [error, setError] = useState(''), [notice, setNotice] = useState(null);
  const revision = useRef(0), alive = useRef(true);
  const fresh = () => ++revision.current, valid = epoch => alive.current && epoch === revision.current;
  useEffect(() => () => { alive.current = false; revision.current++; }, []);
  const shell = useContext(GCPConsoleContext);
  useEffect(() => { shell?.setProject(project); }, [shell, project]);
  useEffect(() => shell?.registerNavigation(() => { setNotice(null); load(); }), [shell]);
  async function call(url, options = {}) {
    const response = await request(url, {...options, headers: {'Content-Type': 'application/json', ...options.headers}});
    const text = await response.text();
    return text ? JSON.parse(text) : {};
  }
  async function load(prefer = service) {
    const epoch = fresh(); setLoading(true); setError('');
    try {
      let application;
      try { application = await call(appURL(endpoint, project)); }
      catch (e) {
        if (!notFound(e)) throw e;
        if (valid(epoch)) { setApp({state: 'missing'}); setServices([]); setVersions([]); setRules([]); }
        return;
      }
      const [listed, firewall] = await Promise.all([
        call(appURL(endpoint, project, 'services') + '?pageSize=100'),
        call(appURL(endpoint, project, 'firewall', 'ingressRules') + '?pageSize=1000')]);
      const all = listed.services || [];
      // Keep only server-listed IDs in state: a typed service name never
      // becomes part of a link or path without the server naming it.
      const chosen = (all.find(s => s.id === prefer) || all[0])?.id || '';
      const page = chosen ? await call(appURL(endpoint, project, 'services', chosen, 'versions') + '?pageSize=100') : {};
      if (!valid(epoch)) return;
      setApp({state: 'ready', value: application}); setServices(all); setService(chosen || 'default');
      setVersions(page.versions || []); setRules(firewall.ingressRules || []);
    } catch (e) { if (valid(epoch)) setError(e.message); } finally { if (valid(epoch)) setLoading(false); }
  }
  useEffect(() => { if (endpoint && project) load(); }, [endpoint, project]);
  // Writes reload only after the server acknowledged them, so a refusal
  // stays visible next to the unchanged state.
  async function write(url, options, kind, name, prefer) {
    fresh(); setBusy(true); setError(''); setNotice(null); setPending('');
    let done = false;
    try { await call(url, options); done = true; }
    catch (e) { if (alive.current) setError(e.message); } finally { if (alive.current) setBusy(false); }
    if (done && alive.current) { await load(prefer); if (alive.current) setNotice({kind, name}); }
    return done;
  }
  const json = (method, body) => ({method, body: JSON.stringify(body)});
  async function createApp(event) {
    event.preventDefault();
    await write(endpoint + 'v1/apps', json('POST', {id: project, locationId: location}), 'app-created', project);
  }
  function toggleApp() {
    const next = app.value?.servingStatus === 'SERVING' ? 'USER_DISABLED' : 'SERVING';
    write(appURL(endpoint, project) + '?updateMask=servingStatus', json('PATCH', {servingStatus: next}), next === 'SERVING' ? 'app-enabled' : 'app-disabled', project);
  }
  async function deployVersion(event) {
    event.preventDefault();
    let environment;
    try { environment = parseEnvironment(deploy.env); }
    catch (e) { setNotice(null); setError(t(e.message, e.message.startsWith('Use one') ? '1 行に KEY=VALUE を 1 つ書いてください。' : '環境変数名には英字、数字、アンダースコアを使ってください。')); return; }
    const target = deploy.service.trim() || 'default';
    if (await write(appURL(endpoint, project, 'services', target, 'versions'), json('POST', deployBody(deploy.id.trim(), deploy.source.trim(), environment)), 'version-deployed', target + '/' + deploy.id.trim(), target)) {
      setDeploy(d => ({...d, id: '', env: ''}));
    }
  }
  const versionPath = id => appURL(endpoint, project, 'services', service, 'versions', id);
  const setStatus = (v, status) => write(versionPath(v.id) + '?updateMask=servingStatus', json('PATCH', {servingStatus: status}), status === 'SERVING' ? 'version-started' : 'version-stopped', service + '/' + v.id);
  const routeAll = v => write(appURL(endpoint, project, 'services', service) + '?updateMask=split', json('PATCH', {split: {allocations: {[v.id]: 1}}}), 'traffic-moved', service + '/' + v.id);
  const deleteVersion = v => write(versionPath(v.id), {method: 'DELETE'}, 'version-deleted', service + '/' + v.id);
  const deleteService = s => write(appURL(endpoint, project, 'services', s.id), {method: 'DELETE'}, 'service-deleted', s.id, 'default');
  async function createRule(event) {
    event.preventDefault();
    const body = {priority: Number(rule.priority), action: rule.action, sourceRange: rule.sourceRange.trim()};
    if (rule.description.trim()) body.description = rule.description.trim();
    if (await write(appURL(endpoint, project, 'firewall', 'ingressRules'), json('POST', body), 'rule-created', String(body.priority))) {
      setRule({priority: '', action: 'DENY', sourceRange: '', description: ''});
    }
  }
  const deleteRule = r => write(appURL(endpoint, project, 'firewall', 'ingressRules', r.priority), {method: 'DELETE'}, 'rule-deleted', String(r.priority));
  const defaultAction = r => write(appURL(endpoint, project, 'firewall', 'ingressRules', r.priority) + '?updateMask=action', json('PATCH', {action: r.action === 'ALLOW' ? 'DENY' : 'ALLOW'}), 'rule-updated', String(r.priority));
  // Notices keep a canonical kind and the exact resource name; they are
  // translated at render time, so a language change keeps them readable.
  const notices = {
    'app-created': [n => 'Application ' + n + ' created.', n => 'アプリケーション ' + n + ' を作成しました。'],
    'app-enabled': [n => 'Application ' + n + ' is serving.', n => 'アプリケーション ' + n + ' を有効にしました。'],
    'app-disabled': [n => 'Application ' + n + ' is disabled.', n => 'アプリケーション ' + n + ' を無効にしました。'],
    'version-deployed': [n => 'Version ' + n + ' deployed.', n => 'バージョン ' + n + ' をデプロイしました。'],
    'version-started': [n => 'Version ' + n + ' started.', n => 'バージョン ' + n + ' を開始しました。'],
    'version-stopped': [n => 'Version ' + n + ' stopped.', n => 'バージョン ' + n + ' を停止しました。'],
    'version-deleted': [n => 'Version ' + n + ' deleted.', n => 'バージョン ' + n + ' を削除しました。'],
    'traffic-moved': [n => 'All traffic now goes to ' + n + '.', n => 'すべてのトラフィックを ' + n + ' に移しました。'],
    'service-deleted': [n => 'Service ' + n + ' deleted.', n => 'サービス ' + n + ' を削除しました。'],
    'rule-created': [n => 'Firewall rule ' + n + ' created.', n => 'ファイアウォール ルール ' + n + ' を作成しました。'],
    'rule-updated': [n => 'Firewall rule ' + n + ' updated.', n => 'ファイアウォール ルール ' + n + ' を更新しました。'],
    'rule-deleted': [n => 'Firewall rule ' + n + ' deleted.', n => 'ファイアウォール ルール ' + n + ' を削除しました。'],
  };
  const noticeText = notice && notices[notice.kind] ? t(notices[notice.kind][0](notice.name), notices[notice.kind][1](notice.name)) : '';
  const current = services.find(s => s.id === service);
  const allocations = current?.split?.allocations || {};
  const confirm = (key, label, action) => pending === key
    ? <><button type="button" className="gcp-storage-danger" disabled={busy} onClick={action}>{t('Confirm delete ', '削除を確定: ') + label}</button>
      <button type="button" disabled={busy} onClick={() => setPending('')}>{t('Cancel', 'キャンセル')}</button></>
    : <button type="button" className="gcp-storage-danger" disabled={busy} aria-label={t('Delete ', '削除: ') + label} onClick={() => setPending(key)}>{t('Delete', '削除')}</button>;
  const header = <div className="gcp-storage-toolbar">
    <h1 id={ids + 'title'} className="gcp-storage-title">App Engine</h1>
    <button type="button" disabled={busy || loading} onClick={() => { setNotice(null); load(); }}>{t('Refresh', '更新')}</button>
    <a className="gcp-storage-link" href={appEngine?.reference || '/_simulator/docs/gcp-appengine.html'} target="_blank" rel="noreferrer">{t('API reference', 'API リファレンス')}</a>
  </div>;
  const feedback = <>
    {error && <p role="alert" className="gcp-storage-error">{error}</p>}
    {noticeText && <p role="status" className="gcp-storage-notice">{noticeText}</p>}
  </>;
  if (app.state === 'loading') return <section className="gcp-storage gcp-appengine" aria-labelledby={ids + 'title'}>{header}{feedback}
    <p role="status" className="gcp-storage-notice">{t('Loading the App Engine application', 'App Engine アプリケーションを読み込み中')}</p></section>;
  if (app.state === 'missing') return <section className="gcp-storage gcp-appengine" aria-labelledby={ids + 'title'}>{header}{feedback}
    <form className="gcp-storage-form" onSubmit={createApp} aria-labelledby={ids + 'create'}>
      <h2 id={ids + 'create'}>{t('Create an application', 'アプリケーションを作成')}</h2>
      <p>{t('Project ' + project + ' has no App Engine application yet. Choose where it runs. The location cannot be changed later, and an App Engine application cannot be deleted.',
        'プロジェクト ' + project + ' には App Engine アプリケーションがまだありません。実行するロケーションを選んでください。ロケーションは後から変更できず、App Engine アプリケーションは削除できません。')}</p>
      <label htmlFor={ids + 'location'}>{t('Location', 'ロケーション')}</label>
      <select id={ids + 'location'} value={location} onChange={e => setLocation(e.target.value)}>
        {locations.map(l => <option key={l.locationId} value={l.locationId}>{l.displayName + ' (' + l.locationId + ')'}</option>)}
      </select>
      <div className="gcp-storage-actions"><button type="submit" className="gcp-storage-primary" disabled={busy || !location}>{t('Create application', 'アプリケーションを作成')}</button></div>
    </form>
  </section>;
  const a = app.value || {};
  const serving = a.servingStatus === 'SERVING';
  return <section className="gcp-storage gcp-appengine" aria-labelledby={ids + 'title'}>
    {header}
    {(appEngine?.bounds || []).map(b => <p key={b} className="gcp-storage-limits">{b}</p>)}
    {feedback}
    <h2 id={ids + 'app'}>{t('Application', 'アプリケーション')}</h2>
    <dl className="gcp-storage-metadata">
      <dt>{t('Application ID', 'アプリケーション ID')}</dt><dd>{a.id}</dd>
      <dt>{t('Location', 'ロケーション')}</dt><dd>{a.locationId}</dd>
      <dt>{t('Serving status', '提供ステータス')}</dt><dd>{a.servingStatus}</dd>
      <dt>{t('Default hostname', 'デフォルトのホスト名')}</dt><dd>{a.defaultHostname}</dd>
    </dl>
    <p className="gcp-storage-limits">{t('An App Engine application cannot be deleted. Disable it to stop serving every version.', 'App Engine アプリケーションは削除できません。すべてのバージョンの提供を止めるには無効にしてください。')}</p>
    <button type="button" disabled={busy} onClick={toggleApp}>{serving ? t('Disable application', 'アプリケーションを無効にする') : t('Enable application', 'アプリケーションを有効にする')}</button>

    <h2 id={ids + 'services'}>{t('Services', 'サービス')}</h2>
    <div className="gcp-storage-table-scroll" role="region" aria-labelledby={ids + 'services'} tabIndex={0}>
      <table aria-busy={loading}>
        <caption>{t('Services', 'サービス')} ({services.length})</caption>
        <thead><tr><th scope="col">{t('Service', 'サービス')}</th><th scope="col">{t('Traffic split', 'トラフィック分割')}</th><th scope="col">{t('Actions', '操作')}</th></tr></thead>
        <tbody>{services.length ? services.map(s => <tr key={s.id}>
          <th scope="row"><button type="button" className="gcp-storage-link" aria-pressed={s.id === service} disabled={busy} onClick={() => { setPending(''); load(s.id); }}>{s.id}</button></th>
          <td>{splitText(s.split)}{s.split?.shardBy ? ' · ' + s.split.shardBy : ''}</td>
          <td className="gcp-storage-row-actions">{s.id === 'default' ? t('The default service cannot be deleted.', 'default サービスは削除できません。') : confirm('service:' + s.id, t('service ', 'サービス ') + s.id, () => deleteService(s))}</td>
        </tr>) : <tr><td colSpan="3">{loading ? t('Loading', '読み込み中') : t('No services yet. Deploy a version of the default service first.', 'サービスはまだありません。最初に default サービスのバージョンをデプロイしてください。')}</td></tr>}</tbody>
      </table>
    </div>

    <h2 id={ids + 'versions'}>{t('Versions of ', 'バージョン: ') + (current ? service : '—')}</h2>
    <div className="gcp-storage-table-scroll" role="region" aria-labelledby={ids + 'versions'} tabIndex={0}>
      <table aria-busy={loading}>
        <caption>{t('Versions', 'バージョン')} ({current ? versions.length : 0})</caption>
        <thead><tr><th scope="col">{t('Version', 'バージョン')}</th><th scope="col">{t('Runtime', 'ランタイム')}</th><th scope="col">{t('Status', 'ステータス')}</th>
          <th scope="col">{t('Traffic', 'トラフィック')}</th><th scope="col">{t('Created', '作成日時')}</th><th scope="col">{t('Actions', '操作')}</th></tr></thead>
        <tbody>{current && versions.length ? versions.map(v => <tr key={v.id}>
          <th scope="row">{v.id}</th><td>{v.runtime}</td><td>{v.servingStatus}</td><td>{percent(allocations[v.id] || 0)}</td><td>{v.createTime}</td>
          <td className="gcp-storage-row-actions">
            <a className="gcp-storage-link" href={dataPlaneURL(appEngine?.dataPlane, versionHost(v.id, service, project))} target="_blank" rel="noreferrer" aria-label={t('Open ', '開く: ') + v.id}>{t('Open', '開く')}</a>
            <button type="button" disabled={busy} aria-label={(v.servingStatus === 'SERVING' ? t('Stop ', '停止: ') : t('Start ', '開始: ')) + v.id} onClick={() => setStatus(v, v.servingStatus === 'SERVING' ? 'STOPPED' : 'SERVING')}>{v.servingStatus === 'SERVING' ? t('Stop', '停止') : t('Start', '開始')}</button>
            {allocations[v.id] !== 1 && <button type="button" disabled={busy} aria-label={t('Send all traffic to ', 'すべてのトラフィックを送る: ') + v.id} onClick={() => routeAll(v)}>{t('Send all traffic here', 'すべてのトラフィックをここへ')}</button>}
            {confirm('version:' + service + '/' + v.id, v.id, () => deleteVersion(v))}
          </td>
        </tr>) : <tr><td colSpan="6">{loading ? t('Loading', '読み込み中') : t('No versions', 'バージョンはありません')}</td></tr>}</tbody>
      </table>
    </div>

    <form className="gcp-storage-form" onSubmit={deployVersion} aria-labelledby={ids + 'deploy'}>
      <h2 id={ids + 'deploy'}>{t('Deploy a version', 'バージョンをデプロイ')}</h2>
      <p>{t('Upload a ZIP of Python 3.12 sources to Cloud Storage first. main.py must define the WSGI callable app (entrypoint main:app). Only Python sources are accepted; each request runs once in a fresh local sandbox without network access.',
        '先に Python 3.12 のソースを ZIP にして Cloud Storage へアップロードしてください。main.py に WSGI の呼び出し可能オブジェクト app（エントリポイント main:app）が必要です。受け付けるのは Python のソースだけです。各リクエストはネットワークのない新しいローカル サンドボックスで 1 回実行されます。')}</p>
      <label htmlFor={ids + 'service'}>{t('Service', 'サービス')}</label>
      <input id={ids + 'service'} required value={deploy.service} onChange={e => setDeploy(d => ({...d, service: e.target.value}))}/>
      <label htmlFor={ids + 'version'}>{t('Version ID', 'バージョン ID')}</label>
      <input id={ids + 'version'} required value={deploy.id} aria-describedby={ids + 'version-hint'} onChange={e => setDeploy(d => ({...d, id: e.target.value}))}/>
      <p id={ids + 'version-hint'}>{t('Lowercase letters, digits and hyphens. A new service sends all traffic to its first version; later versions get none until you move traffic.', '英小文字、数字、ハイフンを使います。新しいサービスでは最初のバージョンにすべてのトラフィックが送られます。以降のバージョンにはトラフィックを移すまで送られません。')}</p>
      <label htmlFor={ids + 'source'}>{t('Cloud Storage ZIP URL', 'Cloud Storage の ZIP の URL')}</label>
      <input id={ids + 'source'} required inputMode="url" placeholder="https://storage.googleapis.com/BUCKET/app.zip" value={deploy.source} onChange={e => setDeploy(d => ({...d, source: e.target.value}))}/>
      <label htmlFor={ids + 'env'}>{t('Environment variables (optional, KEY=VALUE per line)', '環境変数（任意、1 行に KEY=VALUE）')}</label>
      <textarea id={ids + 'env'} rows={3} value={deploy.env} onChange={e => setDeploy(d => ({...d, env: e.target.value}))}/>
      <div className="gcp-storage-actions"><button type="submit" className="gcp-storage-primary" disabled={busy}>{t('Deploy', 'デプロイ')}</button></div>
    </form>

    <h2 id={ids + 'firewall'}>{t('Firewall rules', 'ファイアウォール ルール')}</h2>
    <p className="gcp-storage-limits">{t('Rules are checked in priority order against the client address of each request; the first match decides. The default rule (2147483647) cannot be deleted.',
      'ルールは各リクエストのクライアント アドレスに対して優先度の順に評価され、最初に一致したルールで決まります。デフォルト ルール（2147483647）は削除できません。')}</p>
    <div className="gcp-storage-table-scroll" role="region" aria-labelledby={ids + 'firewall'} tabIndex={0}>
      <table aria-busy={loading}>
        <caption>{t('Firewall rules', 'ファイアウォール ルール')} ({rules.length})</caption>
        <thead><tr><th scope="col">{t('Priority', '優先度')}</th><th scope="col">{t('Action', 'アクション')}</th><th scope="col">{t('IP range', 'IP 範囲')}</th><th scope="col">{t('Description', '説明')}</th><th scope="col">{t('Actions', '操作')}</th></tr></thead>
        <tbody>{rules.map(r => <tr key={r.priority}>
          <th scope="row">{r.priority === DEFAULT_RULE ? t('default', 'デフォルト') + ' (' + r.priority + ')' : r.priority}</th><td>{r.action}</td><td>{r.sourceRange}</td><td>{r.description || ''}</td>
          <td className="gcp-storage-row-actions">{r.priority === DEFAULT_RULE
            ? <button type="button" disabled={busy} onClick={() => defaultAction(r)}>{r.action === 'ALLOW' ? t('Deny by default', 'デフォルトで拒否') : t('Allow by default', 'デフォルトで許可')}</button>
            : confirm('rule:' + r.priority, t('rule ', 'ルール ') + r.priority, () => deleteRule(r))}</td>
        </tr>)}</tbody>
      </table>
    </div>
    <form className="gcp-storage-form" onSubmit={createRule} aria-labelledby={ids + 'rule'}>
      <h2 id={ids + 'rule'}>{t('Create a firewall rule', 'ファイアウォール ルールを作成')}</h2>
      <label htmlFor={ids + 'priority'}>{t('Priority (1 to 2147483646)', '優先度（1〜2147483646）')}</label>
      <input id={ids + 'priority'} required inputMode="numeric" pattern="[0-9]+" value={rule.priority} onChange={e => setRule(r => ({...r, priority: e.target.value}))}/>
      <label htmlFor={ids + 'action'}>{t('Action', 'アクション')}</label>
      <select id={ids + 'action'} value={rule.action} onChange={e => setRule(r => ({...r, action: e.target.value}))}><option value="DENY">DENY</option><option value="ALLOW">ALLOW</option></select>
      <label htmlFor={ids + 'range'}>{t('IP range (*, an address or CIDR)', 'IP 範囲（*、アドレスまたは CIDR）')}</label>
      <input id={ids + 'range'} required value={rule.sourceRange} onChange={e => setRule(r => ({...r, sourceRange: e.target.value}))}/>
      <label htmlFor={ids + 'description'}>{t('Description (optional)', '説明（任意）')}</label>
      <input id={ids + 'description'} maxLength={400} value={rule.description} onChange={e => setRule(r => ({...r, description: e.target.value}))}/>
      <div className="gcp-storage-actions"><button type="submit" className="gcp-storage-primary" disabled={busy}>{t('Create rule', 'ルールを作成')}</button></div>
    </form>
  </section>;
}
