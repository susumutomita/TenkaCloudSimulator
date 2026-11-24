import React, {useContext, useEffect, useId, useMemo, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {GCPConsoleContext} from './gcp-console-shell.jsx';
import {cloudDNS, zoneBody} from './gcp-dns-requests.mjs';
import './gcp-storage.css';

const blankZone = {name: '', dnsName: '', description: ''};

export default function GCPDNSView({request, project}) {
  return <ZoneScreen key={project} request={request} project={project}/>;
}

function ZoneScreen({request, project}) {
  const {t} = useLocale();
  const id = useId();
  const shell = useContext(GCPConsoleContext);
  const api = useMemo(() => cloudDNS(request, project), [request, project]);
  const [page, setPage] = useState('list');
  const [zones, setZones] = useState([]);
  const [selected, setSelected] = useState('');
  const [detail, setDetail] = useState(null);
  const [form, setForm] = useState(blankZone);
  const [error, setError] = useState('');
  const [created, setCreated] = useState('');
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const current = useRef(null);
  const writing = useRef(false);

  function cancelRead() { current.current?.abort(); current.current = null; }
  function begin() {
    cancelRead();
    const handle = new AbortController();
    current.current = handle;
    setError('');
    return handle;
  }
  const active = handle => current.current === handle && !handle.signal.aborted;

  async function loadZones() {
    if (writing.current) return;
    const handle = begin();
    setPage('list'); setZones([]); setSelected(''); setDetail(null);
    setLoading(true); setCreated('');
    try {
      const rows = await api.list(handle.signal);
      if (active(handle)) setZones(rows);
    } catch (cause) {
      if (active(handle)) setError(cause.message);
    } finally {
      if (active(handle)) setLoading(false);
    }
  }

  async function choose(zoneID) {
    if (writing.current) return;
    const handle = begin();
    setPage('detail'); setSelected(zoneID); setDetail(null); setLoading(true);
    try {
      const data = await api.details(zoneID, handle.signal);
      if (active(handle)) setDetail(data);
    } catch (cause) {
      if (active(handle)) setError(cause.message);
    } finally {
      if (active(handle)) setLoading(false);
    }
  }

  useEffect(() => {
    loadZones();
    return () => { cancelRead(); writing.current = false; };
  }, [api]);
  useEffect(() => { shell?.setProject(project); }, [shell, project]);
  useEffect(() => shell?.registerNavigation(loadZones), [shell, api]);

  function showForm() {
    if (writing.current) return;
    cancelRead();
    setPage('create'); setForm(blankZone); setCreated(''); setError(''); setLoading(false);
  }

  async function submit(event) {
    event.preventDefault();
    if (writing.current) return;
    const messages = {
      name: t('Use a lowercase zone name of 1–63 characters.', 'ゾーン名は小文字で始まる1〜63文字で入力してください。'),
      dnsName: t('Enter a valid lowercase DNS suffix, such as example.com.', 'example.com のように有効な小文字のDNSサフィックスを入力してください。'),
      description: t('Description must fit within 1024 UTF-8 bytes.', '説明はUTF-8で1024バイト以内にしてください。'),
    };
    try { zoneBody(form); } catch (cause) { setError(messages[cause.message]); return; }
    const handle = begin();
    writing.current = true; setBusy(true);
    let zone;
    try { zone = await api.create(form, handle.signal); }
    catch (cause) { if (active(handle)) setError(cause.message); }
    finally {
      if (active(handle)) { writing.current = false; setBusy(false); }
    }
    if (!active(handle) || !zone) return;
    if (typeof zone.id !== 'string' || !/^[1-9][0-9]{0,18}$/.test(zone.id) || zone.name !== form.name) {
      setError(t('The server response could not be confirmed. Check the zone list before trying again.',
        'サーバーの応答を確認できませんでした。再試行する前にゾーン一覧を確認してください。'));
      return;
    }
    setCreated(zone.name);
    await choose(zone.id);
  }

  const field = key => ({value: form[key], onChange: e => setForm({...form, [key]: e.target.value})});
  return <section className="gcp-storage gcp-dns" aria-labelledby={id + '-title'}>
    <div className="gcp-storage-toolbar">
      <h1 className="gcp-storage-title" id={id + '-title'}>Cloud DNS</h1>
      <a className="gcp-storage-link" href="/_simulator/docs/gcp-dns.html">{t('API reference', 'API リファレンス')}</a>
    </div>
    <p className="gcp-storage-limits">
      {t('Public zone metadata only. Nameservers are simulated; there is no DNS serving or propagation.',
        'パブリックゾーンのメタデータのみ対応します。ネームサーバーは模擬データで、DNS応答や伝播は行いません。')}
    </p>
    {error && <p role="alert" className="gcp-storage-error">{error}</p>}
    {created && <p role="status" className="gcp-storage-notice">{t('Created zone: ', '作成したゾーン: ')}{created}</p>}
    {loading && <p role="status">{t('Loading Cloud DNS', 'Cloud DNS を読み込み中')}</p>}
    {page === 'list' && <>
      <div className="gcp-storage-toolbar">
        <h2 id={id + '-zones'}>{t('Zones', 'ゾーン')}</h2>
        <button type="button" disabled={loading} onClick={loadZones}>{t('Refresh', '更新')}</button>
        <button type="button" className="gcp-storage-primary" onClick={showForm}>{t('Create zone', 'ゾーンを作成')}</button>
      </div>
      <div className="gcp-storage-table-scroll" role="region" aria-labelledby={id + '-zones'} tabIndex={0}>
        <table aria-busy={loading}>
          <thead><tr><th scope="col">{t('Zone name', 'ゾーン名')}</th><th scope="col">{t('DNS name', 'DNS 名')}</th>
            <th scope="col">{t('Type', 'タイプ')}</th><th scope="col">{t('Description', '説明')}</th></tr></thead>
          <tbody>{zones.map(zone => <tr key={zone.id}>
            <th scope="row"><button type="button" className="gcp-storage-link" onClick={() => { setCreated(''); choose(zone.id); }}>{zone.name}</button></th>
            <td>{zone.dnsName}</td><td>{t('Public', 'パブリック')}</td><td>{zone.description}</td>
          </tr>)}</tbody>
        </table>
      </div>
      {!loading && !error && zones.length === 0 && <p>{t('No zones', 'ゾーンはありません')}</p>}
    </>}
    {page === 'create' && <form className="gcp-storage-form" onSubmit={submit} aria-labelledby={id + '-create'} aria-busy={busy}>
      <h2 id={id + '-create'}>{t('Create a DNS zone', 'DNS ゾーンを作成')}</h2>
      <fieldset disabled={busy}>
        <label htmlFor={id + '-type'}>{t('Zone type', 'ゾーンタイプ')}</label>
        <select id={id + '-type'} defaultValue="public">
          <option value="public">{t('Public', 'パブリック')}</option>
          <option value="private" disabled>{t('Private (not supported)', 'プライベート（未対応）')}</option>
        </select>
        <label htmlFor={id + '-name'}>{t('Zone name', 'ゾーン名')}</label>
        <input id={id + '-name'} required maxLength={63} placeholder="my-new-zone" {...field('name')}/>
        <label htmlFor={id + '-suffix'}>{t('DNS name', 'DNS 名')}</label>
        <input id={id + '-suffix'} required maxLength={253} placeholder="example.com" {...field('dnsName')}/>
        <label htmlFor={id + '-description'}>{t('Description', '説明')}</label>
        <input id={id + '-description'} maxLength={1024} {...field('description')}/>
        <label htmlFor={id + '-dnssec'}>DNSSEC</label>
        <select id={id + '-dnssec'} defaultValue="off">
          <option value="off">{t('Off', 'オフ')}</option>
          <option disabled>{t('On (not supported)', 'オン（未対応）')}</option>
          <option disabled>{t('Transfer (not supported)', '移管（未対応）')}</option>
        </select>
        <div className="gcp-storage-actions">
          <button type="button" onClick={loadZones}>{t('Cancel', 'キャンセル')}</button>
          <button type="submit" className="gcp-storage-primary">{t('Create', '作成')}</button>
        </div>
      </fieldset>
    </form>}
    {page === 'detail' && <>
      <div className="gcp-storage-toolbar">
        <button type="button" className="gcp-storage-link" onClick={loadZones}>{t('Back to zones', 'ゾーン一覧に戻る')}</button>
        <h2>{t('Zone details', 'ゾーンの詳細')}{detail ? ': ' + detail.zone.name : ''}</h2>
        <button type="button" disabled={loading} onClick={() => choose(selected)}>{t('Refresh', '更新')}</button>
      </div>
      {detail && <>
        <dl className="gcp-storage-metadata">
          <dt>{t('DNS name', 'DNS 名')}</dt><dd>{detail.zone.dnsName}</dd>
          <dt>{t('Zone type', 'ゾーンタイプ')}</dt><dd>{t('Public', 'パブリック')}</dd>
          <dt>{t('Description', '説明')}</dt><dd>{detail.zone.description || '—'}</dd>
          <dt>{t('Name servers (simulated)', 'ネームサーバー（模擬）')}</dt><dd>{detail.zone.nameServers.join(', ')}</dd>
        </dl>
        <h3 id={id + '-records'}>{t('Record sets', 'レコードセット')}</h3>
        <div className="gcp-storage-table-scroll" role="region" aria-labelledby={id + '-records'} tabIndex={0}>
          <table>
            <thead><tr><th scope="col">{t('DNS name', 'DNS 名')}</th><th scope="col">{t('Type', 'タイプ')}</th>
              <th scope="col">TTL</th><th scope="col">{t('Data', 'データ')}</th></tr></thead>
            <tbody>{detail.records.map(record => <tr key={record.name + '/' + record.type}>
              <th scope="row">{record.name}</th><td>{record.type}</td><td>{record.ttl}</td><td>{record.rrdatas.join(', ')}</td>
            </tr>)}</tbody>
          </table>
        </div>
      </>}
    </>}
  </section>;
}
