import React, {useEffect, useId, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import './gcp-storage.css';

// Generic Google API catalog view. Requests use each API's actual REST paths
// on this simulator; the capability descriptor only says which ones exist.
export function versionPrefix(method) { return method.path.split('/')[0]; }
export function collectionURL(api, method, parent, id) {
  return api.endpoint + versionPrefix(method) + '/' + parent.split('/').map(encodeURIComponent).join('/') + '/' + id;
}
export function resourceURL(api, method, name) {
  return api.endpoint + versionPrefix(method) + '/' + name.split('/').map(encodeURIComponent).join('/');
}
export function unwrap(response) { return response && response.done === true && response.response ? response.response : response; }
export function parentChain(api, collection) {
  const chain = [];
  for (let c = collection; c?.parent; c = api.collections.find(x => x.id === c.parent)) chain.unshift(api.collections.find(x => x.id === c.parent));
  return chain;
}

export default function GCPCatalogView({request, catalog}) {
  const {t} = useLocale();
  const apis = catalog?.apis || [];
  const [apiName, setApiName] = useState(apis[0]?.name || ''), [collectionId, setCollectionId] = useState('');
  const [location, setLocation] = useState((catalog?.locations || []).find(l => l !== 'global') || 'global');
  const [parents, setParents] = useState([]), [parent, setParent] = useState('');
  const [items, setItems] = useState([]), [detail, setDetail] = useState(null), [form, setForm] = useState({}), [childId, setChildId] = useState('');
  const [pending, setPending] = useState(''), [loading, setLoading] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const revision = useRef(0), alive = useRef(true), ids = useId();
  const api = apis.find(a => a.name === apiName);
  const collection = api?.collections.find(c => c.id === collectionId) || api?.collections[0];
  const chain = api && collection ? parentChain(api, collection) : [];
  const root = 'projects/' + catalog?.project + (api?.scope === 'location' ? '/locations/' + location : '');
  const fresh = () => ++revision.current, valid = epoch => alive.current && epoch === revision.current;
  useEffect(() => () => { alive.current = false; revision.current++; }, []);
  async function call(url, options = {}) {
    const response = await request(url, {...options, headers: {'Content-Type': 'application/json', ...options.headers}});
    const text = await response.text();
    return text ? JSON.parse(text) : {};
  }
  // A parent selection passes its value explicitly: the state update is not
  // yet visible to this render's closure.
  async function load(preferred = parent) {
    if (!api || !collection) return;
    const epoch = fresh(); setLoading(true); setError(''); setDetail(null);
    try {
      let scope = root;
      if (chain.length) {
        const top = chain[chain.length - 1], list = top.methods.list;
        const grand = chain.length > 1 ? null : root;
        if (!grand) { setParents([]); setItems([]); return; }
        const page = await call(collectionURL(api, list, root, top.id));
        if (!valid(epoch)) return;
        const rows = page[list.items] || [];
        setParents(rows);
        scope = rows.some(r => r.name === preferred) ? preferred : (rows[0]?.name || '');
        setParent(scope);
        if (!scope) { setItems([]); return; }
      }
      const list = collection.methods.list;
      const page = await call(collectionURL(api, list, scope, collection.id));
      if (valid(epoch)) setItems(page[list.items] || []);
    } catch (e) { if (valid(epoch)) { setItems([]); setError(e.message); } } finally { if (valid(epoch)) setLoading(false); }
  }
  useEffect(() => { load(); }, [apiName, collectionId, location]);
  async function create(event) {
    event.preventDefault();
    const method = collection.methods.create; fresh(); setBusy(true); setError(''); setNotice('');
    let created = false;
    try {
      const body = {};
      for (const field of collection.fields || []) {
        const raw = form[field.name];
        if (raw === undefined || raw === '') continue;
        body[field.name] = field.type === 'boolean' ? raw === true : field.type === 'integer' ? Number(raw) : String(raw);
      }
      const scope = chain.length ? parent : root;
      const query = collection.idParam ? '?' + new URLSearchParams({[collection.idParam]: childId}) : '';
      await call(collectionURL(api, method, scope, collection.id) + query, {method: 'POST', body: JSON.stringify(body)});
      created = true;
    } catch (e) { if (alive.current) setError(e.message); } finally { if (alive.current) setBusy(false); }
    // Reload only after an acknowledged write so a failure stays visible.
    if (created && alive.current) { await load(); if (alive.current) { setNotice(t('Resource created.', 'リソースを作成しました。')); setForm({}); setChildId(''); } }
  }
  async function remove(item) {
    setPending(''); fresh(); setBusy(true); setError(''); setNotice('');
    let removed = false;
    try { await call(resourceURL(api, collection.methods.delete, item.name), {method: 'DELETE'}); removed = true; }
    catch (e) { if (alive.current) setError(e.message); } finally { if (alive.current) setBusy(false); }
    if (removed && alive.current) { await load(); if (alive.current) setNotice(t('Resource deleted.', 'リソースを削除しました。')); }
  }
  async function inspect(item) {
    const epoch = fresh(); setError('');
    try { const value = await call(resourceURL(api, collection.methods.get, item.name)); if (valid(epoch)) setDetail(unwrap(value)); }
    catch (e) { if (valid(epoch)) setError(e.message); }
  }
  if (!apis.length) return <p className="gcp-storage-notice">{t('No Google API catalog services are enabled.', '有効な Google API カタログサービスはありません。')}</p>;
  const short = name => name.split('/').pop();
  return <section className="gcp-storage" aria-labelledby={ids + 'title'}>
    <div className="gcp-storage-toolbar">
      <h1 id={ids + 'title'} className="gcp-storage-title">{t('Google Cloud service catalog', 'Google Cloud サービスカタログ')}</h1>
      <button type="button" disabled={busy || loading} onClick={() => { setNotice(''); load(); }}>{t('Refresh', '更新')}</button>
      <a className="gcp-storage-link" href="/_simulator/docs/gcp-catalog.html" target="_blank" rel="noreferrer">{t('API reference', 'API リファレンス')}</a>
    </div>
    <div className="gcp-storage-form">
      <label htmlFor={ids + 'api'}>{t('API', 'API')}</label>
      <select id={ids + 'api'} value={apiName} disabled={busy} onChange={e => { setApiName(e.target.value); setCollectionId(''); setParent(''); }}>
        {apis.map(a => <option key={a.name} value={a.name}>{a.title}</option>)}
      </select>
      {api?.scope === 'location' && <><label htmlFor={ids + 'location'}>{t('Location', 'ロケーション')}</label>
        <select id={ids + 'location'} value={location} disabled={busy} onChange={e => setLocation(e.target.value)}>{(catalog.locations || []).map(l => <option key={l}>{l}</option>)}</select></>}
      <label htmlFor={ids + 'collection'}>{t('Resource type', 'リソースの種類')}</label>
      <select id={ids + 'collection'} value={collection?.id} disabled={busy} onChange={e => { setCollectionId(e.target.value); setParent(''); }}>
        {api?.collections.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
      </select>
      {chain.length > 0 && <><label htmlFor={ids + 'parent'}>{chain[chain.length - 1].id}</label>
        <select id={ids + 'parent'} value={parent} disabled={busy || !parents.length} onChange={e => { setParent(e.target.value); load(e.target.value); }}>
          {parents.map(p => <option key={p.name} value={p.name}>{short(p.name)}</option>)}
        </select></>}
    </div>
    {api?.bounds.map(b => <p key={b} className="gcp-storage-limits">{b}</p>)}
    {error && <p role="alert" className="gcp-storage-error">{error}</p>}
    {notice && <p role="status" className="gcp-storage-notice">{notice}</p>}
    {detail ? <div className="gcp-storage-metadata">
      <h2>{short(detail.name || '')}</h2>
      <pre className="gcp-catalog-json">{JSON.stringify(detail, null, 2)}</pre>
      <button type="button" onClick={() => setDetail(null)}>{t('Back to list', '一覧に戻る')}</button>
    </div> : <div className="gcp-storage-table-scroll">
      <table className="gcp-catalog-table" aria-busy={loading}>
        <caption>{collection?.id} ({items.length})</caption>
        <thead><tr><th scope="col">{t('Name', '名前')}</th><th scope="col">{t('Actions', '操作')}</th></tr></thead>
        <tbody>{items.length ? items.map(item => <tr key={item.name}>
          <td><button type="button" className="gcp-storage-name" onClick={() => inspect(item)}>{short(item.name)}</button></td>
          <td className="gcp-storage-row-actions">{collection.methods.delete && (pending === item.name
            ? <><button type="button" className="gcp-storage-danger" disabled={busy} onClick={() => remove(item)}>{t('Confirm delete ', '削除を確定: ') + short(item.name)}</button>
              <button type="button" onClick={() => setPending('')}>{t('Cancel', 'キャンセル')}</button></>
            : <button type="button" className="gcp-storage-danger" disabled={busy} aria-label={t('Delete ', '削除: ') + short(item.name)} onClick={() => setPending(item.name)}>{t('Delete', '削除')}</button>)}</td>
        </tr>) : <tr><td colSpan="2">{loading ? t('Loading', '読み込み中') : t('No resources', 'リソースはありません')}</td></tr>}</tbody>
      </table>
    </div>}
    {collection?.methods.create && (!chain.length || parent) && <form className="gcp-storage-form" onSubmit={create} aria-labelledby={ids + 'create'}>
      <h2 id={ids + 'create'}>{t('Create', '作成')} {collection.id}</h2>
      {collection.idParam && <><label htmlFor={ids + 'id'}>{collection.idParam}</label><input id={ids + 'id'} required value={childId} onChange={e => setChildId(e.target.value)}/></>}
      {(collection.fields || []).map(field => field.type === 'boolean'
        ? <label key={field.name}><input type="checkbox" checked={form[field.name] === true} onChange={e => setForm(f => ({...f, [field.name]: e.target.checked}))}/> {field.name}</label>
        : <React.Fragment key={field.name}><label htmlFor={ids + field.name}>{field.name}{field.required ? '' : ' (' + t('optional', '任意') + ')'}</label>
          {field.enum ? <select id={ids + field.name} value={form[field.name] || ''} onChange={e => setForm(f => ({...f, [field.name]: e.target.value}))}><option value="">{t('Choose', '選択')}</option>{field.enum.map(v => <option key={v}>{v}</option>)}</select>
            : <input id={ids + field.name} inputMode={field.type === 'integer' ? 'numeric' : undefined} value={form[field.name] ?? ''} onChange={e => setForm(f => ({...f, [field.name]: e.target.value}))}/>}</React.Fragment>)}
      <button type="submit" className="gcp-storage-primary" disabled={busy}>{t('Create', '作成')}</button>
    </form>}
  </section>;
}
