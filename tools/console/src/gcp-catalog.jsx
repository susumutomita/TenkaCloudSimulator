import React, {useEffect, useId, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import './gcp-storage.css';

// Generic Google API catalog view. Requests use each API's actual REST paths
// on this simulator; the descriptors only say which methods exist and how
// their official paths are shaped.
const segments = path => path.split('/').filter(Boolean);
// The method path before "projects" is the version prefix ("v1"); it is
// empty for flat APIs whose endpoint already ends with the version.
export function pathPrefix(method) {
  const s = segments(method.path), start = s.indexOf('projects');
  return s.slice(0, Math.max(start, 0)).join('/');
}
// The collection segment is the last segment of the create or list path, as
// "forwardingRules" for Compute Engine globalForwardingRules.
export function collectionSegment(method) {
  const s = segments(method.path);
  return s[s.length - 1].split(':')[0];
}
function join(api, method, name) {
  const prefix = pathPrefix(method);
  return api.endpoint + (prefix ? prefix + '/' : '') + name.split('/').map(encodeURIComponent).join('/');
}
export function collectionURL(api, method, parent) { return join(api, method, parent + '/' + collectionSegment(method)); }
export function resourceURL(api, method, name) { return join(api, method, name); }
export function unwrap(response) { return response && response.done === true && response.response ? response.response : response; }
export function parentChain(api, collection) {
  const chain = [];
  for (let c = collection; c?.parent; c = api.collections.find(x => x.id === c.parent)) chain.unshift(api.collections.find(x => x.id === c.parent));
  return chain;
}
// A listed resource's full name: short-name collections (Cloud Logging
// sinks, Compute Engine) list the bare ID under the listed parent.
export function itemName(collection, method, parent, item) {
  return collection.shortName ? parent + '/' + collectionSegment(method) + '/' + item.name : item.name;
}
const placeKinds = {locations: 'location', regions: 'region', zones: 'zone'};
// The parent of a top-level collection comes from its official list (or
// create) path: the project, then locations/{location}, regions/{region},
// zones/{zone} or literal segments such as locations/global or global.
// Returns the place it needs and a builder, or null for a path this view
// cannot fill.
export function rootTemplate(collection) {
  const method = collection.methods.list || collection.methods.create;
  if (!method) return null;
  const s = segments(method.path), start = s.indexOf('projects');
  if (start < 0) return null;
  const parts = s.slice(start, -1);
  if (!parts[1]?.startsWith('{')) return null;
  let place = '';
  for (let i = 2; i < parts.length; i++) {
    if (!parts[i].startsWith('{')) continue;
    if (place || !placeKinds[parts[i - 1]]) return null;
    place = placeKinds[parts[i - 1]];
  }
  if (place === 'location' && collection.zonal) place = 'zone';
  return {place, build: (project, value) => parts.map((p, i) => !p.startsWith('{') ? p : i === 1 ? project : value).join('/')};
}

export default function GCPCatalogView({request, catalog}) {
  const {t} = useLocale();
  const [descriptors, setDescriptors] = useState({state: 'loading', apis: [], error: ''}), [attempt, setAttempt] = useState(0);
  const [apiName, setApiName] = useState(''), [collectionId, setCollectionId] = useState('');
  const [places, setPlaces] = useState({}), [levels, setLevels] = useState([]), [listScope, setListScope] = useState('');
  const [implicitId, setImplicitId] = useState(''), [implicitDraft, setImplicitDraft] = useState('');
  const [items, setItems] = useState([]), [detail, setDetail] = useState(null), [form, setForm] = useState({}), [childId, setChildId] = useState('');
  const [pending, setPending] = useState(''), [loading, setLoading] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const revision = useRef(0), alive = useRef(true), ids = useId();
  const apis = descriptors.apis;
  const api = apis.find(a => a.name === apiName) || apis[0];
  const collection = api?.collections.find(c => c.id === collectionId) || api?.collections[0];
  const chain = api && collection ? parentChain(api, collection) : [];
  const template = chain[0] || collection ? rootTemplate(chain[0] || collection) : null;
  const placeKind = template?.place || '';
  const locations = catalog?.locations || [];
  const options = placeKind === 'zone' ? catalog?.zones || [] : placeKind === 'region' ? locations.filter(l => l !== 'global') : locations;
  // Each API remembers its own location, region and zone choice.
  const placeKey = api?.name + ':' + placeKind;
  const place = !placeKind ? '' : options.includes(places[placeKey]) ? places[placeKey] : options.find(l => l !== 'global') || options[0] || '';
  const root = template ? template.build(catalog?.project, place) : '';
  const listMethod = collection?.methods.list;
  const blocked = !collection ? '' : !template || chain.some(c => c.implicit)
    ? t('The console cannot build this resource type\'s path. Use an SDK or the API reference.', 'コンソールはこのリソースの種類のパスを組み立てられません。SDK か API リファレンスを使ってください。')
    : !listMethod || chain.some(c => !c.methods.list)
      ? t('This resource type or its parent has no list method, so the console cannot show it. Use an SDK or the API reference.', 'このリソースの種類か親には一覧メソッドがないため、コンソールでは表示できません。SDK か API リファレンスを使ってください。') : '';
  const readOnly = !collection?.methods.create ? '' : collection.implicit
    ? t('The console lists and deletes these resources under the ' + collection.implicit + ' ID you enter, but cannot create them: the create request needs fields the form cannot collect. Use an SDK or the API reference to create one.',
      'コンソールでは、入力した ' + collection.implicit + ' の ID の下でこのリソースを一覧・削除できます。作成はできません。作成リクエストに、フォームでは入力できないフィールドが必要なためです。作成には SDK か API リファレンスを使ってください。')
    : collection.unformed?.length
      ? t('The console cannot create this resource: its create request needs ' + collection.unformed.join(', ') + ', which the form cannot collect. Use an SDK or the API reference.',
        'コンソールではこのリソースを作成できません。作成リクエストに必要な ' + collection.unformed.join(', ') + ' を、フォームでは入力できないためです。SDK か API リファレンスを使ってください。') : '';
  const fresh = () => ++revision.current, valid = epoch => alive.current && epoch === revision.current;
  useEffect(() => () => { alive.current = false; revision.current++; }, []);
  useEffect(() => {
    let live = true;
    setDescriptors(d => ({...d, state: 'loading', error: ''}));
    (async () => {
      try {
        const data = await (await request(catalog.descriptors)).json();
        if (live) setDescriptors({state: 'ready', apis: data.apis || [], error: ''});
      } catch (e) { if (live) setDescriptors({state: 'error', apis: [], error: e.message}); }
    })();
    return () => { live = false; };
  }, [catalog?.descriptors, attempt]);
  async function call(url, options = {}) {
    const response = await request(url, {...options, headers: {'Content-Type': 'application/json', ...options.headers}});
    const text = await response.text();
    return text ? JSON.parse(text) : {};
  }
  // A selection passes its values explicitly: state updates are not yet
  // visible to this render's closure. prefer maps a parent collection ID to
  // the parent to keep; other levels keep their earlier choice when listed.
  async function load({prefer = Object.fromEntries(levels.map(l => [l.id, l.selected])), implicit = implicitId} = {}) {
    const epoch = fresh(); setError(''); setDetail(null);
    const empty = () => { setListScope(''); setItems([]); };
    if (!api || !collection || blocked) { setLevels([]); empty(); return; }
    setLoading(true);
    try {
      let scope = root;
      const next = [];
      for (const level of chain) {
        const list = level.methods.list;
        const page = await call(collectionURL(api, list, scope));
        if (!valid(epoch)) return;
        const names = (page[list.items] || []).map(row => itemName(level, list, scope, row));
        const selected = names.includes(prefer[level.id]) ? prefer[level.id] : names[0] || '';
        next.push({id: level.id, names, selected});
        scope = selected;
        if (!selected) break;
      }
      setLevels(next);
      if (chain.length && !scope) { empty(); return; }
      if (collection.implicit) {
        if (!implicit) { empty(); return; }
        scope += '/' + collection.implicit + '/' + implicit;
      }
      const page = await call(collectionURL(api, listMethod, scope));
      if (valid(epoch)) { setListScope(scope); setItems(page[listMethod.items] || []); }
    } catch (e) { if (valid(epoch)) { empty(); setError(e.message); } } finally { if (valid(epoch)) setLoading(false); }
  }
  useEffect(() => { if (descriptors.state === 'ready') load(); }, [descriptors.state, api?.name, collection?.id, place]);
  function choose(index, value) {
    const prefer = Object.fromEntries(levels.slice(0, index).map(l => [l.id, l.selected]));
    load({prefer: {...prefer, [levels[index].id]: value}});
  }
  async function create(event) {
    event.preventDefault();
    const method = collection.methods.create, scope = listScope; fresh(); setBusy(true); setError(''); setNotice('');
    let created = false;
    try {
      const body = {};
      for (const field of collection.fields || []) {
        const raw = form[field.name];
        if (raw === undefined || raw === '') continue;
        body[field.name] = field.type === 'boolean' ? raw === 'true' : field.type === 'integer' ? Number(raw) : String(raw);
      }
      let query = '';
      if (collection.idParam) query = '?' + new URLSearchParams({[collection.idParam]: childId});
      else if (collection.idField) body[collection.idField] = collection.fullName ? scope + '/' + collectionSegment(method) + '/' + childId : childId;
      await call(collectionURL(api, method, scope) + query, {method: 'POST', body: JSON.stringify(body)});
      created = true;
    } catch (e) { if (alive.current) setError(e.message); } finally { if (alive.current) setBusy(false); }
    // Reload only after an acknowledged write so a failure stays visible.
    if (created && alive.current) { await load(); if (alive.current) { setNotice(t('Resource created.', 'リソースを作成しました。')); setForm({}); setChildId(''); } }
  }
  async function remove(item) {
    setPending(''); fresh(); setBusy(true); setError(''); setNotice('');
    let removed = false;
    try { await call(resourceURL(api, collection.methods.delete, itemName(collection, listMethod, listScope, item)), {method: 'DELETE'}); removed = true; }
    catch (e) { if (alive.current) setError(e.message); } finally { if (alive.current) setBusy(false); }
    if (removed && alive.current) { await load(); if (alive.current) setNotice(t('Resource deleted.', 'リソースを削除しました。')); }
  }
  async function inspect(item) {
    const epoch = fresh(); setError('');
    if (!collection.methods.get) { setDetail(item); return; }
    try { const value = await call(resourceURL(api, collection.methods.get, itemName(collection, listMethod, listScope, item))); if (valid(epoch)) setDetail(unwrap(value)); }
    catch (e) { if (valid(epoch)) setError(e.message); }
  }
  if (descriptors.state === 'loading') return <p role="status" className="gcp-storage-notice">{t('Loading Google API descriptors', 'Google API の記述を読み込み中')}</p>;
  if (descriptors.state === 'error') return <div className="gcp-storage">
    <p role="alert" className="gcp-storage-error">{t('Google API descriptors could not be loaded: ', 'Google API の記述を読み込めませんでした: ') + descriptors.error}</p>
    <button type="button" onClick={() => setAttempt(n => n + 1)}>{t('Retry', '再試行')}</button>
  </div>;
  if (!apis.length) return <p className="gcp-storage-notice">{t('No Google API catalog services are enabled.', '有効な Google API カタログサービスはありません。')}</p>;
  const short = item => String(item.name || item.id || '').split('/').pop();
  const placeLabel = placeKind === 'zone' ? t('Zone', 'ゾーン') : placeKind === 'region' ? t('Region', 'リージョン') : t('Location', 'ロケーション');
  const idName = collection?.idParam || collection?.idField;
  const canCreate = collection?.methods.create && !readOnly && !blocked && listScope;
  return <section className="gcp-storage" aria-labelledby={ids + 'title'}>
    <div className="gcp-storage-toolbar">
      <h1 id={ids + 'title'} className="gcp-storage-title">{t('Google Cloud service catalog', 'Google Cloud サービスカタログ')}</h1>
      <button type="button" disabled={busy || loading} onClick={() => { setNotice(''); load(); }}>{t('Refresh', '更新')}</button>
      <a className="gcp-storage-link" href="/_simulator/docs/gcp-catalog.html" target="_blank" rel="noreferrer">{t('API reference', 'API リファレンス')}</a>
    </div>
    <div className="gcp-storage-form">
      <label htmlFor={ids + 'api'}>{t('API', 'API')}</label>
      <select id={ids + 'api'} value={api?.name} disabled={busy} onChange={e => { setApiName(e.target.value); setCollectionId(''); setLevels([]); setImplicitId(''); setImplicitDraft(''); }}>
        {apis.map(a => <option key={a.name} value={a.name}>{a.title}</option>)}
      </select>
      <label htmlFor={ids + 'collection'}>{t('Resource type', 'リソースの種類')}</label>
      <select id={ids + 'collection'} value={collection?.id} disabled={busy} onChange={e => { setCollectionId(e.target.value); setImplicitId(''); setImplicitDraft(''); }}>
        {api?.collections.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
      </select>
      {placeKind && <><label htmlFor={ids + 'place'}>{placeLabel}</label>
        <select id={ids + 'place'} value={place} disabled={busy} onChange={e => setPlaces(p => ({...p, [placeKey]: e.target.value}))}>{options.map(l => <option key={l}>{l}</option>)}</select></>}
      {levels.map((level, index) => <React.Fragment key={level.id}><label htmlFor={ids + 'parent-' + level.id}>{level.id}</label>
        <select id={ids + 'parent-' + level.id} value={level.selected} disabled={busy || !level.names.length} onChange={e => choose(index, e.target.value)}>
          {level.names.map(name => <option key={name} value={name}>{name.split('/').pop()}</option>)}
        </select></React.Fragment>)}
    </div>
    {collection?.implicit && !blocked && <form className="gcp-storage-form" onSubmit={e => { e.preventDefault(); setImplicitId(implicitDraft); load({implicit: implicitDraft}); }}>
      <label htmlFor={ids + 'implicit'}>{collection.implicit + ' ID'}</label>
      <input id={ids + 'implicit'} required value={implicitDraft} onChange={e => setImplicitDraft(e.target.value)}/>
      <button type="submit" disabled={busy || loading}>{t('Show resources', 'リソースを表示')}</button>
    </form>}
    {api?.bounds.map(b => <p key={b} className="gcp-storage-limits">{b}</p>)}
    {blocked && <p className="gcp-storage-notice">{blocked}</p>}
    {readOnly && !blocked && <p className="gcp-storage-notice">{readOnly}</p>}
    {error && <p role="alert" className="gcp-storage-error">{error}</p>}
    {notice && <p role="status" className="gcp-storage-notice">{notice}</p>}
    {!blocked && (detail ? <div className="gcp-storage-metadata">
      <h2>{short(detail)}</h2>
      <pre className="gcp-catalog-json">{JSON.stringify(detail, null, 2)}</pre>
      <button type="button" onClick={() => setDetail(null)}>{t('Back to list', '一覧に戻る')}</button>
    </div> : <div className="gcp-storage-table-scroll">
      <table className="gcp-catalog-table" aria-busy={loading}>
        <caption>{collection?.id} ({items.length})</caption>
        <thead><tr><th scope="col">{t('Name', '名前')}</th><th scope="col">{t('Actions', '操作')}</th></tr></thead>
        <tbody>{items.length ? items.map((item, index) => <tr key={item.name || index}>
          <td><button type="button" className="gcp-storage-name" onClick={() => inspect(item)}>{short(item)}</button></td>
          <td className="gcp-storage-row-actions">{collection.methods.delete && (pending === item.name
            ? <><button type="button" className="gcp-storage-danger" disabled={busy} onClick={() => remove(item)}>{t('Confirm delete ', '削除を確定: ') + short(item)}</button>
              <button type="button" onClick={() => setPending('')}>{t('Cancel', 'キャンセル')}</button></>
            : <button type="button" className="gcp-storage-danger" disabled={busy} aria-label={t('Delete ', '削除: ') + short(item)} onClick={() => setPending(item.name)}>{t('Delete', '削除')}</button>)}</td>
        </tr>) : <tr><td colSpan="2">{loading ? t('Loading', '読み込み中') : collection?.implicit && !implicitId ? t('Enter an ID to list resources', 'ID を入力すると一覧を表示します') : t('No resources', 'リソースはありません')}</td></tr>}</tbody>
      </table>
    </div>)}
    {canCreate && <form className="gcp-storage-form" onSubmit={create} aria-labelledby={ids + 'create'}>
      <h2 id={ids + 'create'}>{t('Create', '作成')} {collection.id}</h2>
      {idName && <><label htmlFor={ids + 'id'}>{idName}</label><input id={ids + 'id'} required value={childId} aria-describedby={collection.fullName ? ids + 'id-hint' : undefined} onChange={e => setChildId(e.target.value)}/>
        {collection.fullName && <p id={ids + 'id-hint'} className="gcp-storage-limits">{t('Sent as ', '送信する値: ') + listScope + '/' + collectionSegment(collection.methods.create) + '/' + (childId || 'ID')}</p>}</>}
      {(collection.fields || []).map(field => <React.Fragment key={field.name}><label htmlFor={ids + field.name}>{field.name}{field.required ? '' : ' (' + t('optional', '任意') + ')'}</label>
        {field.type === 'boolean' || field.enum
          ? <select id={ids + field.name} value={form[field.name] || ''} onChange={e => setForm(f => ({...f, [field.name]: e.target.value}))}><option value="">{field.type === 'boolean' ? t('Not set', '指定しない') : t('Choose', '選択')}</option>{(field.enum || ['true', 'false']).map(v => <option key={v}>{v}</option>)}</select>
          : <input id={ids + field.name} inputMode={field.type === 'integer' ? 'numeric' : undefined} value={form[field.name] ?? ''} onChange={e => setForm(f => ({...f, [field.name]: e.target.value}))}/>}</React.Fragment>)}
      <button type="submit" className="gcp-storage-primary" disabled={busy}>{t('Create', '作成')}</button>
    </form>}
  </section>;
}
