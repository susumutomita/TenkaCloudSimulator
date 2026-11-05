import React, {useContext, useEffect, useId, useLayoutEffect, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {SakuraNavigation} from './sakura-console.jsx';

const marker = 'Basic ' + btoa('TCSIMSAKURA:dummy-local-only');
const endpointPattern = /^\/_simulator\/providers\/sakura\/cloud\/zone\/([a-z0-9][a-z0-9-]{0,31})\/api\/cloud\/1\.1\/disk$/;
const diskID = value => typeof value === 'string' && /^[1-9][0-9]*$/.test(value);
const validText = (value, min, max) => typeof value === 'string' && Array.from(value).length >= min && Array.from(value).length <= max && !/[\p{Cc}]/u.test(value);
const blankDraft = () => ({name: '', description: '', confirmation: ''});
const emptyView = key => ({key, rows: [], detail: null, selected: '', panel: null, target: '', draft: blankDraft(), notice: null, error: null, loading: true, busy: false});
class LocalViewError extends Error { constructor(code) { super(code); this.code = code; } }
function savedDisk(value) {
  if (!value || !diskID(value.ID) || typeof value.Name !== 'string' || typeof value.Description !== 'string' || value.SizeMB !== 20480 || String(value.Plan?.ID) !== '4' || value.Connection !== 'virtio' || 'Availability' in value) throw new LocalViewError('projection');
  return value;
}

// Root supplies a capability-validated fixed configuration and its existing
// authenticated raw-Response request seam. contextKey changes on auth/exercise
// identity/navigation authority changes, never merely a language preference.
// No identity override argument or opaque credential is selected/read here.
export default function SakuraDisks({request, configuration, contextKey}) {
  const {t} = useLocale(), id = useId(), shell = useContext(SakuraNavigation);
  const endpoint = configuration?.endpoint || '';
  const match = endpointPattern.exec(endpoint);
  const configured = !!match && match[1] === configuration?.zone && typeof contextKey === 'string' && contextKey.length > 0;
  const key = JSON.stringify([endpoint, contextKey, configuration?.environment, configuration?.project, configuration?.zone]);
  const [view, setView] = useState(() => emptyView(key));
  const [refresh, setRefresh] = useState(0), [location, setLocation] = useState({key, resource: ''});
  const resource = location.key === key ? location.resource : '';
  const setResource = value => setLocation({key, resource: value});
  const state = view.key === key ? view : emptyView(key);
  const lifetime = useRef(0), reads = useRef(0), navigation = useRef(0), writeTicket = useRef(null), live = useRef(false), pending = useRef(new Set());
  const client = useRef(request); client.current = request;
  const currentKey = useRef(key); currentKey.current = key;
  const navigate = useRef(null), title = useRef(null), opener = useRef(null);
  const active = (generation, captured) => live.current && lifetime.current === generation && currentKey.current === captured;
  const update = change => setView(old => old.key === key ? {...old, ...change} : old);
  const capture = error => error instanceof LocalViewError ? {local: error.code} : {native: typeof error?.message === 'string' ? error.message : ''};
  function errorText(error) {
    if (!error) return '';
    if ('native' in error && error.native) return error.native; // Native codes/messages are never translated.
    if (error.local === 'text') return t('Enter a name of 1–64 characters and a description of at most 512, without control characters.', '制御文字を除く1〜64文字の名前と512文字以内の説明を入力してください。');
    if (error.local === 'projection') return t('The server returned invalid Disk metadata. Retry the read; do not repeat an acknowledged write.', 'サーバーのディスク情報が不正です。読み取りを再試行し、確認済みの書き込みは繰り返さないでください。');
    return t('The request was not confirmed. Refresh saved state before trying another write.', 'リクエストを確認できませんでした。次の書き込みの前に保存状態を更新してください。');
  }
  async function call(path, options = {}) {
    const controller = new AbortController(); pending.current.add(controller);
    try {
      const response = await client.current(path, {...options, signal: controller.signal, headers: {Authorization: marker, ...(options.body !== undefined ? {'Content-Type': 'application/json'} : {})}});
      if (!response?.ok) throw new LocalViewError('request');
      return response;
    } finally { pending.current.delete(controller); }
  }
  useLayoutEffect(() => {
    ++lifetime.current; ++reads.current; ++navigation.current; writeTicket.current = null; live.current = true;
    for (const controller of pending.current) controller.abort(); pending.current.clear();
    setResource(''); setView(emptyView(key));
    return () => { live.current = false; ++lifetime.current; ++reads.current; for (const controller of pending.current) controller.abort(); pending.current.clear(); };
  }, [key, request]);
  useEffect(() => shell?.registerList(() => navigate.current?.('')), [shell]);
  useEffect(() => {
    if (!configured) return;
    const generation = lifetime.current, captured = key, revision = ++reads.current;
    update({loading: true, rows: [], detail: null, error: null});
    (async () => {
      try {
        const response = await call(resource ? endpoint + '/' + resource : endpoint + '?' + encodeURIComponent(JSON.stringify({From: 0, Count: 100})));
        const data = await response.json();
        let result;
        if (resource) {
          result = savedDisk(data.Disk);
          if (result.ID !== resource) throw new LocalViewError('projection');
        } else {
          if (!Array.isArray(data.Disks) || data.From !== 0 || data.Count !== data.Disks.length || data.Total !== data.Count || data.Total > 100) throw new LocalViewError('projection');
          result = data.Disks.map(savedDisk);
          if (new Set(result.map(d => d.ID)).size !== result.length) throw new LocalViewError('projection');
        }
        if (active(generation, captured) && revision === reads.current) update(resource ? {detail: result} : {rows: result});
      } catch (error) {
        if (active(generation, captured) && revision === reads.current && error?.name !== 'AbortError') update({error: capture(error)});
      } finally { if (active(generation, captured) && revision === reads.current) update({loading: false}); }
    })();
    return () => { ++reads.current; };
  }, [key, request, resource, refresh, configured]);
  function show(value) {
    ++reads.current; ++navigation.current;
    setResource(value); update({panel: null, draft: blankDraft(), target: '', selected: '', rows: [], detail: null, error: null, notice: null});
    setRefresh(value => value + 1); requestAnimationFrame(() => title.current?.focus());
  }
  navigate.current = show;
  function open(panel, disk = null) {
    opener.current = document.activeElement; ++navigation.current; ++reads.current;
    update({loading: false, panel, target: disk?.ID || '', draft: {name: disk?.Name || '', description: disk?.Description || '', confirmation: ''}, error: null, notice: null});
  }
  function close() { ++navigation.current; ++reads.current; update({panel: null, target: '', draft: blankDraft(), error: null}); setRefresh(v => v + 1); requestAnimationFrame(() => opener.current?.isConnected ? opener.current.focus() : title.current?.focus()); }
  async function submit(event) {
    event.preventDefault(); if (!['create','edit','delete'].includes(state.panel) || state.busy || writeTicket.current !== null || !configured || !live.current || currentKey.current !== key) return;
    const {panel, target, draft} = state;
    if ((panel === 'create' || panel === 'edit') && (!validText(draft.name, 1, 64) || !validText(draft.description, 0, 512))) { update({error: {local: 'text'}}); return; }
    if (panel === 'delete' && draft.confirmation !== target) return;
    const generation = lifetime.current, captured = key;
    const ticket = {generation, captured, navigation: navigation.current}; writeTicket.current = ticket;
    update({busy: true, error: null, notice: null});
    let acknowledged = false;
    try {
      const method = panel === 'create' ? 'POST' : panel === 'delete' ? 'DELETE' : 'PUT';
      const options = {method};
      if (panel !== 'delete') options.body = JSON.stringify({Disk: {...(panel === 'create' ? {Plan: {ID: 4}, SizeMB: 20480, Connection: 'virtio'} : {}), Name: draft.name, Description: draft.description}});
      const response = await call(endpoint + (panel === 'create' ? '' : '/' + target), options);
      acknowledged = true;
      if (!active(generation, captured)) { response.body?.cancel().catch(() => {}); return; }
      if (navigation.current !== ticket.navigation) {
        response.body?.cancel().catch(() => {});
        setRefresh(value => value + 1); // Reconcile the current view; never steal navigation.
        return;
      }
      // Commit ACK UI before reading the returned body or fetching metadata. A
      // subsequent read failure exposes only Retry read, never replay of POST/PUT.
      update({panel: null, target: '', draft: blankDraft(), selected: '', notice: panel, rows: [], detail: null, loading: true});
      let next = panel === 'delete' ? '' : target;
      if (panel === 'create') {
        const data = await response.json();
        next = savedDisk(data.Disk).ID;
      } else { response.body?.cancel().catch(() => {}); }
      if (active(generation, captured) && navigation.current === ticket.navigation) { setResource(next); setRefresh(value => value + 1); title.current?.focus(); }
    } catch (error) {
      if (active(generation, captured) && navigation.current === ticket.navigation && error?.name !== 'AbortError') {
        update({error: capture(error), ...(acknowledged ? {panel: null, draft: blankDraft(), loading: false} : {})});
      }
    } finally { if (writeTicket.current === ticket) { writeTicket.current = null; if (active(generation, captured)) update({busy: false}); } }
  }
  const selected = state.rows.find(d => d.ID === state.selected);
  const change = (field, value) => update({draft: {...state.draft, [field]: value}});
  const noticeText = state.notice === 'create' ? t('Disk creation acknowledged. Reading saved metadata.', 'ディスク作成を確認しました。保存済み情報を読み取ります。') : state.notice === 'edit' ? t('Disk update acknowledged. Reading saved metadata.', 'ディスク更新を確認しました。保存済み情報を読み取ります。') : t('Disk deletion acknowledged. Refreshing the list.', 'ディスク削除を確認しました。一覧を更新します。');
  function properties(disk) { return <dl className="sakura-properties"><dt>{t('Disk ID', 'ディスクID')}</dt><dd>{disk.ID}</dd><dt>{t('Name', '名前')}</dt><dd>{disk.Name}</dd><dt>{t('Description', '説明')}</dt><dd>{disk.Description || '—'}</dd><dt>{t('Plan ID', 'プランID')}</dt><dd>4</dd><dt>{t('Logical size', '論理サイズ')}</dt><dd>20 GiB (20480 MB)</dd><dt>{t('Connection', '接続方式')}</dt><dd>virtio</dd><dt>{t('Modified', '更新日時')}</dt><dd>{disk.ModifiedAt || '—'}</dd></dl>; }
  if (!configured) return <section className="sakura-disks"><h1>{t('Disks', 'ディスク')}</h1><p role="status">{t('This provider requires explicit host configuration and capability enablement.', 'このプロバイダーにはホスト側の明示的な設定と機能の有効化が必要です。')}</p></section>;
  return <section className="sakura-disks" aria-busy={state.loading || state.busy}>
    <div className="sakura-title"><div><p className="sakura-breadcrumb">Sakura Cloud / {t('Storage', 'ストレージ')} / {t('Disks', 'ディスク')}</p><h1 ref={title} tabIndex={-1}>{state.panel === 'create' ? t('Create Disk configuration', 'ディスク構成を作成') : resource ? t('Disk information', 'ディスク情報') : t('Disks', 'ディスク')}</h1></div><span className="sakura-local-badge">{t('Metadata only', 'メタデータのみ')}</span></div>
    <p className="sakura-boundary">{t('Local configuration only. No VM, physical storage, boot state or charges are represented.', 'ローカル構成のみ。VM・実ストレージ・起動状態・課金は再現しません。')}</p>
    {state.notice && <p className="sakura-notice" role="status">{noticeText}</p>}
    {state.error && <div className="sakura-error" role="alert"><p>{errorText(state.error)}</p>{!state.panel && <button type="button" disabled={state.busy} onClick={() => setRefresh(v => v + 1)}>{t('Retry read', '読み取りを再試行')}</button>}</div>}
    {state.panel ? <form onSubmit={submit} className="sakura-form"><fieldset disabled={state.busy}><legend>{state.panel === 'delete' ? t('Delete Disk configuration', 'ディスク構成を削除') : state.panel === 'edit' ? t('Edit Disk metadata', 'ディスク情報を編集') : t('New Disk metadata', '新しいディスク情報')}</legend>
      {state.panel === 'delete' ? <><p>{t('Only this local configuration row will be removed. This cannot be undone.', 'このローカル構成のみを削除します。元に戻せません。')}</p><label htmlFor={id+'-confirm'}>{t('Type Disk ID to delete', '削除するディスクIDを入力')}: <strong>{state.target}</strong></label><input id={id+'-confirm'} autoComplete="off" value={state.draft.confirmation} onChange={e => change('confirmation', e.target.value)}/></> : <>
        <label htmlFor={id+'-name'}>{t('Name', '名前')}</label><input id={id+'-name'} autoFocus required maxLength={128} value={state.draft.name} onChange={e => change('name', e.target.value)}/>
        <label htmlFor={id+'-description'}>{t('Description', '説明')}</label><textarea id={id+'-description'} maxLength={1024} rows={4} value={state.draft.description} onChange={e => change('description', e.target.value)}/>
        <p>{t('Fixed configuration: Plan ID 4 · 20 GiB logical · virtio. Tags, real icons, server attachment and resize are unsupported.', '固定構成: プランID 4・論理20 GiB・virtio。タグ・実アイコン・サーバ接続・サイズ変更は未対応です。')}</p>
      </>}
      <div className="sakura-actions"><button type="button" onClick={close}>{t('Cancel', 'キャンセル')}</button><button type="submit" className={state.panel === 'delete' ? 'sakura-danger' : 'sakura-primary'} disabled={state.panel === 'delete' && state.draft.confirmation !== state.target}>{state.panel === 'create' ? t('Create Disk configuration', 'ディスク構成を作成') : state.panel === 'edit' ? t('Save metadata', 'メタデータを保存') : t('Delete configuration', '構成を削除')}</button></div>
    </fieldset></form> : resource ? <><div className="sakura-toolbar"><button type="button" disabled={state.busy} onClick={() => show('')}>{t('Back to Disks', 'ディスク一覧へ')}</button><button type="button" disabled={state.busy || state.loading} onClick={() => setRefresh(v => v + 1)}>{t('Refresh metadata', 'メタデータを更新')}</button><button type="button" disabled={!state.detail || state.busy} onClick={() => open('edit', state.detail)}>{t('Edit', '編集')}</button><button type="button" disabled={!state.detail || state.busy} onClick={() => open('delete', state.detail)}>{t('Delete', '削除')}</button></div>{state.detail && properties(state.detail)}</> : <>
      <div className="sakura-toolbar"><button type="button" className="sakura-primary" disabled={state.busy} onClick={() => open('create')}>{t('Add Disk', 'ディスク追加')}</button><button type="button" disabled={state.busy || state.loading} onClick={() => setRefresh(v => v + 1)}>{t('Refresh Disks', 'ディスク一覧を更新')}</button><button type="button" disabled={!selected || state.busy} onClick={() => show(selected.ID)}>{t('Information', '情報')}</button><button type="button" disabled={!selected || state.busy} onClick={() => open('delete', selected)}>{t('Delete', '削除')}</button></div>
      <div className="sakura-table-wrap"><table><caption>{t('Saved local Disk configurations', '保存済みローカルディスク構成')}</caption><thead><tr><th scope="col">{t('Select', '選択')}</th><th scope="col">{t('Name', '名前')}</th><th scope="col">{t('Disk ID', 'ディスクID')}</th><th scope="col">{t('Logical size', '論理サイズ')}</th><th scope="col">{t('Connection', '接続方式')}</th></tr></thead><tbody>{state.rows.map(disk => <tr key={disk.ID}><td><input type="checkbox" aria-label={t('Select Disk ', 'ディスクを選択 ')+disk.ID} checked={state.selected === disk.ID} onChange={e => update({selected: e.target.checked ? disk.ID : ''})}/></td><td><button type="button" className="sakura-name" onClick={() => show(disk.ID)}>{disk.Name}</button></td><td>{disk.ID}</td><td>20 GiB</td><td>virtio</td></tr>)}</tbody></table></div>
      {!state.loading && !state.error && state.rows.length === 0 && <p>{t('No Disk configurations', 'ディスク構成はありません')}</p>}
    </>}
    {state.loading && <p role="status">{t('Reading saved Disk metadata…', '保存済みディスク情報を読み取り中…')}</p>}
  </section>;
}
