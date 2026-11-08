import React, {useEffect, useId, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import './azure-blob.css';
import './azure-arm.css';

// Generic Azure Resource Manager view. Requests use the official ARM paths and
// api-versions on this simulator; the descriptors only say which ones exist.
// It shares the Azure console styling and never introduces authority.
const GROUP_TYPE = 'Microsoft.Resources/resourceGroups';
const JA_TITLES = {
  'Microsoft.Resources/resourceGroups': 'リソース グループ', 'Microsoft.KeyVault/vaults': 'キー コンテナー',
  'Microsoft.Network/virtualNetworks': '仮想ネットワーク', 'Microsoft.Network/virtualNetworks/subnets': 'サブネット',
  'Microsoft.Network/networkSecurityGroups': 'ネットワーク セキュリティ グループ', 'Microsoft.Network/publicIPAddresses': 'パブリック IP アドレス',
  'Microsoft.ManagedIdentity/userAssignedIdentities': 'ユーザー割り当てマネージド ID', 'Microsoft.OperationalInsights/workspaces': 'Log Analytics ワークスペース',
  'Microsoft.AppConfiguration/configurationStores': 'App Configuration ストア', 'Microsoft.ContainerRegistry/registries': 'コンテナー レジストリ',
  'Microsoft.EventHub/namespaces': 'Event Hubs 名前空間', 'Microsoft.EventHub/namespaces/eventhubs': 'イベント ハブ',
  'Microsoft.ServiceBus/namespaces': 'Service Bus 名前空間', 'Microsoft.ServiceBus/namespaces/queues': 'キュー',
  'Microsoft.DocumentDB/databaseAccounts': 'Azure Cosmos DB アカウント', 'Microsoft.Web/serverfarms': 'App Service プラン',
};

// fill replaces the official path variables in order with encoded values.
export function fill(path, values) {
  let index = 0;
  return path.replace(/\{[^}]+\}/g, () => encodeURIComponent(values[index++] ?? ''));
}
export function assign(body, dotted, value) {
  const parts = dotted.split('.');
  let node = body;
  for (const part of parts.slice(0, -1)) node = node[part] ??= {};
  node[parts.at(-1)] = value;
}
export function buildBody(type, form, location) {
  const body = {};
  if (type.location) body.location = location;
  for (const [path, value] of Object.entries(type.defaults || {})) {
    if (value === '$location-list') assign(body, path, [{locationName: location, failoverPriority: 0}]);
  }
  for (const field of type.fields) {
    const raw = form[field.path];
    if (raw === undefined || raw === '') continue;
    let value = raw;
    if (field.type === 'integer') value = Number(raw);
    else if (field.type === 'boolean') value = raw === true;
    else if (field.type.startsWith('array:')) value = String(raw).split(',').map(s => s.trim()).filter(Boolean);
    assign(body, field.path, value);
  }
  return body;
}
function initialForm(type) {
  const form = {};
  for (const field of type?.fields || []) {
    const value = field.default;
    if (value === undefined) continue;
    form[field.path] = Array.isArray(value) ? value.join(', ') : value;
  }
  return form;
}
function groupOf(id) { return /\/resourceGroups\/([^/]+)/i.exec(id || '')?.[1] || ''; }

export default function AzureARMView({request, catalog}) {
  const {locale, setLocale, t} = useLocale();
  const ids = useId();
  const [descriptors, setDescriptors] = useState(null);
  // Resource groups lead the menu, as they scope every other resource.
  const providers = [...(descriptors?.providers || [])].sort((a, b) => (b.namespace === 'Microsoft.Resources') - (a.namespace === 'Microsoft.Resources'));
  const types = providers.flatMap(p => p.types.map(type => ({...type, provider: p})));
  const [typeName, setTypeName] = useState(GROUP_TYPE);
  const type = types.find(x => x.type === typeName);
  const parentType = type?.parent ? types.find(x => x.type === type.parent) : null;
  const [groups, setGroups] = useState([]), [group, setGroup] = useState(''), [parents, setParents] = useState([]), [parent, setParent] = useState('');
  const [items, setItems] = useState([]), [next, setNext] = useState(''), [selected, setSelected] = useState(null), [detail, setDetail] = useState(null);
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const [pane, setPane] = useState(''), [form, setForm] = useState({}), [name, setName] = useState(''), [location, setLocation] = useState(''), [confirming, setConfirming] = useState(false);
  const [menuFilter, setMenuFilter] = useState('');
  const [menuOpen, setMenuOpen] = useState(false);
  const dialog = useRef(null), opener = useRef(null), firstField = useRef(null);
  const life = useRef({alive: true, epoch: 0});
  const fresh = () => ++life.current.epoch, valid = epoch => life.current.alive && epoch === life.current.epoch;
  const title = x => x ? (locale === 'ja' ? JA_TITLES[x.type] || x.title : x.title) : '';
  const version = x => '?api-version=' + encodeURIComponent(x.provider.apiVersion);
  const sub = descriptors?.subscription;
  useEffect(() => () => { life.current.alive = false; life.current.epoch++; }, []);
  useEffect(() => {
    const node = dialog.current; if (!node) return;
    if (pane && !node.open) { opener.current = document.activeElement; node.showModal(); firstField.current?.focus(); }
    else if (!pane && node.open) { node.close(); opener.current?.focus?.(); }
  }, [pane]);
  async function call(url, options = {}) {
    const response = await request(url, {...options, headers: {'Content-Type': 'application/json', ...options.headers}});
    const text = await response.text();
    return text ? JSON.parse(text) : {};
  }
  // Descriptors and the resource group list come from the same native APIs.
  useEffect(() => {
    if (!catalog?.descriptors) { setLoading(false); return; }
    const epoch = fresh();
    request(catalog.descriptors).then(r => r.json()).then(d => { if (valid(epoch)) setDescriptors(d); })
      .catch(e => { if (valid(epoch)) { setError(e.message); setLoading(false); } });
  }, [catalog?.descriptors]);
  const groupType = types.find(x => x.type === GROUP_TYPE);
  async function listAll(op, values, provider) {
    const page = await call(descriptors.endpoint + fill(op.path, values) + '?api-version=' + encodeURIComponent(provider.apiVersion));
    return {rows: page[op.items] || [], next: op.nextLink ? page[op.nextLink] || '' : ''};
  }
  async function load(options = {}) {
    if (!type || !groupType) return;
    const epoch = fresh(); setLoading(true); setError(''); setSelected(null); setConfirming(false);
    try {
      const g = await listAll(groupType.operations.list, [sub], groupType.provider);
      if (!valid(epoch)) return;
      setGroups(g.rows);
      let scope = options.group ?? group;
      if (scope && !g.rows.some(r => r.name === scope)) scope = '';
      if (type.type === GROUP_TYPE) { setItems(g.rows); setNext(g.next); return; }
      if (parentType && !scope) scope = g.rows[0]?.name || '';
      setGroup(scope);
      if (parentType) {
        if (!scope) { setParents([]); setItems([]); setNext(''); return; }
        const p = await listAll(parentType.operations.list, [sub, scope], parentType.provider);
        if (!valid(epoch)) return;
        setParents(p.rows);
        let chosen = options.parent ?? parent;
        if (!p.rows.some(r => r.name === chosen)) chosen = p.rows[0]?.name || '';
        setParent(chosen);
        if (!chosen) { setItems([]); setNext(''); return; }
        const page = await listAll(type.operations.list, [sub, scope, chosen], type.provider);
        if (valid(epoch)) { setItems(page.rows); setNext(page.next); }
        return;
      }
      const page = scope ? await listAll(type.operations.list, [sub, scope], type.provider) : await listAll(type.operations.listAll, [sub], type.provider);
      if (valid(epoch)) { setItems(page.rows); setNext(page.next); }
    } catch (e) { if (valid(epoch)) { setItems([]); setError(e.message); } }
    finally { if (valid(epoch)) setLoading(false); }
  }
  async function nextPage() {
    const epoch = fresh(); setLoading(true); setError('');
    try {
      const link = new URL(next, globalThis.location.href);
      if (link.origin !== globalThis.location.origin) throw Error(t('The next page is not on this simulator.', '次のページはこのシミュレーター上にありません。'));
      const op = type.type === GROUP_TYPE || !(group || parentType) ? (type.operations.listAll || type.operations.list) : type.operations.list;
      const page = await call(link.pathname + link.search);
      if (valid(epoch)) { setItems(page[op.items] || []); setNext(op.nextLink ? page[op.nextLink] || '' : ''); }
    } catch (e) { if (valid(epoch)) setError(e.message); }
    finally { if (valid(epoch)) setLoading(false); }
  }
  useEffect(() => { if (descriptors) load(); }, [descriptors, typeName]);
  function itemURL(item) {
    return descriptors.endpoint + item.id.split('/').map(s => s && encodeURIComponent(s)).join('/') + version(type);
  }
  function open(kind) {
    setError(''); setNotice(''); setConfirming(false);
    if (kind === 'create') { setForm(initialForm(type)); setName(''); setLocation(descriptors.locations[0] || ''); }
    setPane(kind);
  }
  function closePane() { if (!busy) { setPane(''); setDetail(null); } }
  async function inspect(item) {
    setSelected(item); setDetail(null); open('detail');
    const epoch = fresh(); setBusy(true);
    try { const value = await call(itemURL(item)); if (valid(epoch)) setDetail(value); }
    catch (e) { if (valid(epoch)) setError(e.message); }
    finally { if (valid(epoch)) setBusy(false); }
  }
  async function create(event) {
    event.preventDefault();
    if (busy) return;
    fresh(); setBusy(true); setError(''); setNotice('');
    let created = false;
    try {
      const values = type.type === GROUP_TYPE ? [sub, name] : parentType ? [sub, group, parent, name] : [sub, group, name];
      await call(descriptors.endpoint + fill(type.operations.put.path, values) + version(type), {method: 'PUT', body: JSON.stringify(buildBody(type, form, location))});
      created = true;
    } catch (e) { if (life.current.alive) setError(e.message); }
    finally { if (life.current.alive) setBusy(false); }
    // Reload only after an acknowledged write so a failure stays visible.
    if (created && life.current.alive) { setPane(''); await load(); if (life.current.alive) setNotice(t('Resource created.', 'リソースを作成しました。')); }
  }
  async function remove() {
    if (!selected || busy) return;
    fresh(); setBusy(true); setError(''); setNotice('');
    let removed = false;
    try { await call(itemURL(selected), {method: 'DELETE'}); removed = true; }
    catch (e) { if (life.current.alive) setError(e.message); }
    finally { if (life.current.alive) { setBusy(false); setConfirming(false); } }
    if (removed && life.current.alive) { await load(); if (life.current.alive) setNotice(t('Resource deleted.', 'リソースを削除しました。')); }
  }
  if (!catalog?.descriptors) return <p className="azure-message">{t('No Azure Resource Manager providers are enabled.', '有効な Azure Resource Manager プロバイダーはありません。')}</p>;
  const scoped = type && type.type !== GROUP_TYPE;
  const canCreate = type && (type.type === GROUP_TYPE || (group && (!parentType || parent)));
  const filter = menuFilter.normalize('NFKC').toLowerCase();
  const visible = types.filter(x => !filter || (x.title + ' ' + (JA_TITLES[x.type] || '') + ' ' + x.type).normalize('NFKC').toLowerCase().includes(filter));
  return <section className="azure-blob-view azure-arm-view" aria-labelledby={ids + 'title'}>
    <nav className="azure-breadcrumbs" aria-label={t('Resource Manager navigation', 'Resource Manager のナビゲーション')}>
      <a href="#groups" onClick={e => { e.preventDefault(); if (!busy) setTypeName(GROUP_TYPE); }}>{t('Resource groups', 'リソース グループ')}</a>
      {scoped && group && <><span aria-hidden="true">›</span><span>{group}</span></>}
      {scoped && <><span aria-hidden="true">›</span><span>{title(type)}</span></>}
    </nav>
    <header className="azure-resource-heading"><div className="azure-resource-symbol" aria-hidden="true">▦</div><div>
      <h1 id={ids + 'title'}>{type ? title(type) : t('Azure Resource Manager', 'Azure Resource Manager')}</h1>
      <p>{t('Subscription', 'サブスクリプション')}: {sub || t('Loading…', '読み込み中…')}</p></div></header>
    <div className="azure-resource-layout">
      <aside className="azure-resource-menu azure-arm-menu" aria-label={t('Resource types', 'リソースの種類')}>
        <button type="button" className="azure-arm-menu-toggle" aria-expanded={menuOpen} aria-controls={ids + 'menu'} onClick={() => setMenuOpen(v => !v)}>
          {t('Resource types', 'リソースの種類')}: {title(type)}</button>
        <div id={ids + 'menu'} className={menuOpen ? 'azure-arm-menu-list is-open' : 'azure-arm-menu-list'}>
          <label><span className="azure-visually-hidden">{t('Search resource types', 'リソースの種類を検索')}</span>
            <input type="search" placeholder={t('Search', '検索')} value={menuFilter} onChange={e => setMenuFilter(e.target.value)}/></label>
          {providers.map(p => {
            const listed = visible.filter(x => x.provider === p);
            return listed.length ? <React.Fragment key={p.namespace}><h2>{p.title}</h2>
              {listed.map(x => <button type="button" key={x.type} aria-current={x.type === typeName ? 'page' : undefined} disabled={busy}
                className={x.parent ? 'azure-arm-child' : undefined} onClick={() => { setNotice(''); setMenuOpen(false); setTypeName(x.type); }}>{title(x)}</button>)}</React.Fragment> : null;
          })}
          <p>{t('Only reviewed resource types appear here.', 'レビュー済みのリソースの種類のみ表示します。')}</p>
        </div>
      </aside>
      <div className="azure-work-pane">
        <p className="azure-local-banner">{t('Independent local simulator. Resources are configuration metadata in this environment; no Azure resource is created.', '独立したローカルシミュレーターです。リソースはこの環境の構成メタデータで、Azure リソースは作成しません。')}</p>
        {error && <div className="azure-message azure-error" role="alert"><strong>{t('Operation failed', '操作に失敗しました')}</strong><p>{error}</p></div>}
        {notice && <div className="azure-message" role="status">{notice}</div>}
        {scoped && <div className="azure-arm-scope">
          <label htmlFor={ids + 'group'}>{t('Resource group', 'リソース グループ')}</label>
          <select id={ids + 'group'} value={group} disabled={busy || loading} onChange={e => { setGroup(e.target.value); load({group: e.target.value}); }}>
            {!parentType && <option value="">{t('All resource groups', 'すべてのリソース グループ')}</option>}
            {groups.map(g => <option key={g.name} value={g.name}>{g.name}</option>)}
          </select>
          {parentType && <><label htmlFor={ids + 'parent'}>{title(parentType)}</label>
            <select id={ids + 'parent'} value={parent} disabled={busy || loading || !parents.length} onChange={e => { setParent(e.target.value); load({parent: e.target.value}); }}>
              {parents.map(p => <option key={p.name} value={p.name}>{p.name}</option>)}
            </select></>}
        </div>}
        <div className="azure-command-bar" aria-label={t('Resource actions', 'リソース操作')}>
          <button type="button" disabled={busy || loading || !canCreate} onClick={() => open('create')}>+ {t('Create', '作成')}</button>
          <button type="button" disabled={busy || loading} onClick={() => { setNotice(''); load(); }}>↻ {t('Refresh', '最新の情報に更新')}</button>
          <button type="button" disabled={busy || !selected} onClick={() => { setError(''); setNotice(''); setConfirming(true); }}>{t('Delete', '削除')}</button>
          <a className="azure-arm-reference" href="/_simulator/docs/azure-catalog.html" target="_blank" rel="noreferrer">{t('API reference', 'API リファレンス')}</a>
        </div>
        {confirming && selected && <div className="azure-message azure-arm-confirm" role="group" aria-labelledby={ids + 'confirm'}>
          <p id={ids + 'confirm'}><strong>{t('Delete ', '削除: ') + selected.name}</strong> {type.type === GROUP_TYPE || types.some(x => x.parent === type.type)
            ? t('This also permanently deletes every resource it contains.', '含まれるすべてのリソースも完全に削除されます。') : t('This cannot be undone.', 'この操作は元に戻せません。')}</p>
          <button type="button" className="azure-primary" disabled={busy} onClick={remove}>{t('Confirm delete', '削除を確定')}</button>
          <button type="button" disabled={busy} onClick={() => setConfirming(false)}>{t('Cancel', 'キャンセル')}</button>
        </div>}
        <p className="azure-result-count" role="status">{loading ? t('Loading resources', 'リソースを読み込み中') : t(`${items.length} on this page`, `このページに ${items.length} 件`)}</p>
        <div className="azure-table-scroll"><table aria-label={title(type)} aria-busy={loading}>
          <thead><tr><th scope="col"><span className="azure-visually-hidden">{t('Select', '選択')}</span></th><th scope="col">{t('Name', '名前')}</th>
            {scoped && <th scope="col">{t('Resource group', 'リソース グループ')}</th>}<th scope="col">{t('Location', '場所')}</th></tr></thead>
          <tbody>{items.map(item => <tr key={item.id} className={selected?.id === item.id ? 'azure-selected' : ''}>
            <td><input type="checkbox" aria-label={t('Select ', '選択: ') + item.name} checked={selected?.id === item.id} disabled={busy}
              onChange={e => { setConfirming(false); setSelected(e.target.checked ? item : null); }}/></td>
            <td>{type.type === GROUP_TYPE ? <a className="azure-row-link" href="#group" onClick={e => { e.preventDefault(); if (!busy) inspect(item); }}>{item.name}</a>
              : <button type="button" className="azure-row-link" disabled={busy} onClick={() => inspect(item)}>{item.name}</button>}</td>
            {scoped && <td>{groupOf(item.id)}</td>}<td>{item.location || '—'}</td></tr>)}</tbody>
        </table></div>
        {!loading && !items.length && <p className="azure-empty">{parentType && !parent
          ? t('Create a parent resource first.', '先に親リソースを作成してください。')
          : scoped && !groups.length ? t('Create a resource group first.', '先にリソース グループを作成してください。')
            : t('No resources on this page. Select Create to add one.', 'このページにリソースはありません。作成を選んで追加してください。')}</p>}
        <div className="azure-pagination"><button type="button" disabled={busy || loading} onClick={() => load()}>{t('First page', '最初のページ')}</button>
          <button type="button" disabled={busy || loading || !next} onClick={nextPage}>{t('Next page', '次のページ')}</button></div>
        {type && <details className="azure-limits"><summary>{t('Local support and limits', 'ローカルでの対応範囲と制限')}</summary>
          <p>{type.provider.namespace} {type.provider.apiVersion} · {t('up to', '上限')} {type.limit}</p>
          {type.provider.bounds.map(b => <p key={b} lang="en">{b}</p>)}</details>}
      </div>
    </div>
    <dialog ref={dialog} className="azure-action-pane" aria-labelledby={ids + 'pane'} onCancel={e => { e.preventDefault(); closePane(); }}>
      <header><h2 id={ids + 'pane'}>{pane === 'create' ? t('Create ', '作成: ') + title(type) : selected?.name || ''}</h2>
        <div className="azure-pane-header-controls"><label className="azure-pane-language">Language / 言語<select value={locale} onChange={e => setLocale(e.target.value)}><option value="en">English</option><option value="ja">日本語</option></select></label>
          <button type="button" disabled={busy} aria-label={t('Close pane', 'ペインを閉じる')} onClick={closePane}>×</button></div></header>
      {pane === 'detail' ? <><div className="azure-pane-body" aria-busy={busy}>
        {busy && <p role="status">{t('Loading resource…', 'リソースを読み込み中…')}</p>}
        {error && <p className="azure-message azure-error" role="alert">{error}</p>}
        {detail && <><dl><dt>{t('Resource ID', 'リソース ID')}</dt><dd>{detail.id}</dd><dt>{t('Type', '種類')}</dt><dd>{detail.type || type.type}</dd>
          {detail.location && <><dt>{t('Location', '場所')}</dt><dd>{detail.location}</dd></>}
          {detail.properties?.provisioningState && <><dt>{t('Provisioning state', 'プロビジョニング状態')}</dt><dd>{detail.properties.provisioningState}</dd></>}</dl>
          <h3>JSON</h3><pre className="azure-arm-json" tabIndex={0} aria-label={t('Resource JSON', 'リソースの JSON')}>{JSON.stringify(detail, null, 2)}</pre></>}
      </div><footer><button type="button" disabled={busy} onClick={closePane}>{t('Close', '閉じる')}</button></footer></>
        : pane === 'create' ? <form onSubmit={create}><div className="azure-pane-body">
          {error && <p className="azure-message azure-error" role="alert">{error}</p>}
          <label>{t('Name', '名前')} <span aria-hidden="true">*</span><input ref={firstField} required value={name} disabled={busy} onChange={e => setName(e.target.value)}/></label>
          {type.type !== GROUP_TYPE && <p>{t('Resource group', 'リソース グループ')}: <strong>{group}</strong>{parentType && <> · {title(parentType)}: <strong>{parent}</strong></>}</p>}
          {type.location && <label>{t('Region', 'リージョン')}<select value={location} disabled={busy} onChange={e => setLocation(e.target.value)}>
            {descriptors.locations.map(l => <option key={l} value={l}>{l}</option>)}</select></label>}
          {type.fields.map(field => field.type === 'boolean'
            ? <label key={field.path} className="azure-arm-check"><input type="checkbox" checked={form[field.path] === true} disabled={busy} onChange={e => setForm(f => ({...f, [field.path]: e.target.checked}))}/> {field.path}</label>
            : <label key={field.path}>{field.path}{field.required ? <span aria-hidden="true"> *</span> : ' (' + t('optional', '任意') + ')'}
              {field.enum ? <select value={form[field.path] ?? ''} required={field.required} disabled={busy} onChange={e => setForm(f => ({...f, [field.path]: e.target.value}))}>
                <option value="">{t('Choose', '選択')}</option>{field.enum.map(value => <option key={value} value={value}>{value}</option>)}</select>
                : <input value={form[field.path] ?? ''} required={field.required} disabled={busy} inputMode={field.type === 'integer' ? 'numeric' : undefined}
                  onChange={e => setForm(f => ({...f, [field.path]: e.target.value}))}/>}
              {field.type.startsWith('array:') && <span className="azure-arm-hint">{t('Separate values with commas.', '値はカンマで区切ります。')}</span>}</label>)}
          <p className="azure-arm-hint">{t('Other official fields are available through SDKs and the API reference.', 'その他の公式フィールドは SDK と API リファレンスで指定できます。')}</p>
        </div><footer><button type="submit" className="azure-primary" disabled={busy || !name}>{busy ? t('Working…', '処理中…') : t('Create', '作成')}</button>
          <button type="button" disabled={busy} onClick={closePane}>{t('Cancel', 'キャンセル')}</button></footer></form> : null}
    </dialog>
  </section>;
}
