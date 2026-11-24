import React, {useContext, useEffect, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {SakuraNavigation} from './sakura-console.jsx';
import {readSakuraServers, sakuraServerEndpoint} from './sakura-server-read.mjs';

export default function SakuraServers({request, capability, contextKey}) {
  const {t} = useLocale();
  const endpoint = sakuraServerEndpoint(capability);
  if (!endpoint || typeof request !== 'function' || typeof contextKey !== 'string' || !contextKey) {
    return <section><h1>{t('Servers', 'サーバ')}</h1><p role="status">{t('The local Server metadata capability is unavailable.', 'ローカルサーバ情報の機能は利用できません。')}</p></section>;
  }
  return <SakuraServerSession key={JSON.stringify([contextKey, endpoint])} request={request}
    capability={capability}/>;
}

function SakuraServerSession({request, capability}) {
  const {t} = useLocale();
  const shell = useContext(SakuraNavigation);
  const title = useRef(null);
  const [target, setTarget] = useState({id: '', revision: 0});
  const [selection, setSelection] = useState('');
  const [result, setResult] = useState(null);

  function show(id, focus = true) {
    setSelection('');
    setTarget(previous => ({id, revision: previous.revision + 1}));
    if (focus) title.current?.focus();
  }
  useEffect(() => shell?.registerList(() => show('')), [shell]);
  useEffect(() => {
    const controller = new AbortController();
    readSakuraServers(request, capability, target.id, controller.signal)
      .then(data => {
        if (!controller.signal.aborted) setResult({target, request, capability, data});
      }, () => {
        if (!controller.signal.aborted) setResult({target, request, capability, failed: true});
      });
    return () => controller.abort();
  }, [request, target, capability]);
  const current = result?.target === target && result.request === request && result.capability === capability ? result : null;
  const loading = current === null;
  const detail = target.id ? current?.data : null;
  const rows = target.id ? [] : current?.data || [];
  const selected = rows.find(server => server.ID === selection);
  const plan = server => `${server.CPU} vCPU / ${server.MemoryMB} MB`;

  return <section className="sakura-disks" aria-busy={loading}>
    <div className="sakura-title">
      <div><p className="sakura-breadcrumb">Sakura Cloud / {t('Servers', 'サーバ')}</p>
        <h1 ref={title} tabIndex={-1}>{target.id ? t('Server information', 'サーバ情報') : t('Servers', 'サーバ')}</h1>
      </div>
      <span className="sakura-local-badge">{t('Metadata only', 'メタデータのみ')}</span>
    </div>
    <p className="sakura-boundary">{t('Configured zone', '設定済みゾーン')}: <strong>{capability.zone}</strong>. {t('Read-only local metadata. VM operations and status are not represented.', '読み取り専用のローカル情報です。VM の操作や稼働状態は再現しません。')}</p>
    <div className="sakura-toolbar">
      {target.id ? <button type="button" onClick={() => show('')}>{t('Back to Servers', 'サーバ一覧へ')}</button> :
        <button type="button" disabled={!selected} onClick={() => selected && show(selected.ID)}>{t('Details', '詳細')}</button>}
      <button type="button" disabled={loading} onClick={() => show(target.id, false)}>{t('Refresh', '更新')}</button>
    </div>
    {current?.failed && <p className="sakura-error" role="alert">{t('Could not read valid Server metadata. Use Refresh to try again.', 'サーバ情報を読み取れませんでした。「更新」で再試行できます。')}</p>}
    {loading && <p role="status">{t('Reading Server metadata…', 'サーバ情報を読み取り中…')}</p>}
    {!target.id && !loading && !current?.failed && <div className="sakura-table-wrap">
      <table><caption>{t('Saved local Server configurations', '保存済みローカルサーバ構成')}</caption>
        <thead><tr><th scope="col">{t('Select', '選択')}</th><th scope="col">{t('Name', '名前')}</th>
          <th scope="col">{t('Resource ID', 'リソースID')}</th><th scope="col">{t('Plan', 'プラン')}</th></tr></thead>
        <tbody>{rows.map(server => <tr key={server.ID} onDoubleClick={() => show(server.ID)}>
          <td><input type="checkbox" checked={selection === server.ID}
            aria-label={t('Select Server ', 'サーバを選択 ')+server.ID}
            onChange={event => setSelection(event.target.checked ? server.ID : '')}/></td>
          <td><button type="button" className="sakura-name" onClick={() => show(server.ID)}>{server.Name}</button></td>
          <td>{server.ID}</td><td>{plan(server)}</td>
        </tr>)}</tbody>
      </table>
      {rows.length === 0 && <p>{t('No Server configurations', 'サーバ構成はありません')}</p>}
    </div>}
    {detail && <dl className="sakura-properties">
      <dt>{t('Resource ID', 'リソースID')}</dt><dd>{detail.ID}</dd>
      <dt>{t('Name', '名前')}</dt><dd>{detail.Name}</dd>
      <dt>{t('Description', '説明')}</dt><dd>{detail.Description || '—'}</dd>
      <dt>{t('Plan', 'プラン')}</dt><dd>{plan(detail)}</dd>
      <dt>{t('Created', '作成日時')}</dt><dd>{detail.CreatedAt}</dd>
      <dt>{t('Modified', '修正日時')}</dt><dd>{detail.ModifiedAt}</dd>
    </dl>}
  </section>;
}
