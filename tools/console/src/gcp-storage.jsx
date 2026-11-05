import React, {useContext, useEffect, useId, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {GCPConsoleContext} from './gcp-console-shell.jsx';
import './gcp-storage.css';

const API = '/_simulator/providers/gcp/storage/v1';
const UPLOAD = '/_simulator/providers/gcp/upload/storage/v1';
const MAX_FILE = 1 << 20;
const locations = ['US', 'EU', 'US-EAST1', 'US-WEST1', 'ASIA-NORTHEAST1', 'EUROPE-WEST1'];
const objectURL = (bucket, name) => `${API}/b/${encodeURIComponent(bucket)}/o/${encodeURIComponent(name)}`;

const clientErrors = {
  request: ['The Cloud Storage request failed. Try again.', 'Cloud Storage リクエストに失敗しました。再試行してください。'],
  list: ['The server returned an invalid Cloud Storage list.', 'サーバーからの Cloud Storage 一覧が不正です。'],
  file: ['Choose one text file of at most 1 MiB.', '1 MiB 以下のテキストファイルを1つ選択してください。'],
  name: ['Enter an object name of 1–1024 UTF-8 bytes without NUL, CR or LF.', 'NUL・改行を含まない1〜1024 UTF-8バイトのオブジェクト名を入力してください。'],
  encoding: ['This console uploads UTF-8 text files only.', 'このコンソールは UTF-8 テキストファイルのみアップロードできます。'],
  metadata: ['The server returned invalid object metadata.', 'サーバーからのオブジェクト情報が不正です。']
};
class StorageViewError extends Error {
  constructor(code) { super(clientErrors[code][0]); this.code = code; }
}
function captureError(error) {
  if (error instanceof StorageViewError) return {kind: 'client', code: error.code};
  // Native errors and API codes are opaque: never translate or pattern-match them.
  return typeof error?.message === 'string' && error.message
    ? {kind: 'server', message: error.message} : {kind: 'client', code: 'request'};
}
function noticeText(notice, t) {
  switch (notice.kind) {
    case 'bucket-created': return t(`Bucket ${notice.name} created.`, `バケット ${notice.name} を作成しました。`);
    case 'object-uploaded': return t(`Object ${notice.name} uploaded. Existing objects were not overwritten.`, `オブジェクト ${notice.name} をアップロードしました。既存オブジェクトは上書きしていません。`);
    case 'object-deleted': return t(`Object ${notice.name} deleted.`, `オブジェクト ${notice.name} を削除しました。`);
    case 'bucket-deleted': return t(`Bucket ${notice.name} deleted.`, `バケット ${notice.name} を削除しました。`);
    case 'download': return t(`Download requested for ${notice.name}.`, `${notice.name} のダウンロードを開始しました。`);
    default: return '';
  }
}

// Root supplies its authenticated provider-neutral request seam (raw Response).
// No actor, AWS identity, OAuth credential or project authority is minted here.
export default function GCPStorageView({request, project = 'tenkacloud-local'}) {
  const id = useId();
  const {locale, t} = useLocale();
  const shell = useContext(GCPConsoleContext), navigateRef = useRef(null);
  const displayBytes = value => `${Number(value || 0).toLocaleString(locale === 'ja' ? 'ja-JP' : 'en-US')} B`;
  const [object, setObject] = useState(null), [step, setStep] = useState(0);
  const [locationType, setLocationType] = useState('multi-region');
  const title = useRef(null), stepTitle = useRef(null), opener = useRef(null), firstFocus = useRef(true);
  const steps = [t('Get started', 'はじめに'), t('Choose where to store your data', 'データの保存場所を選択'), t('Choose how to store your data', 'データの保存方法を選択'), t('Choose how to control access to objects', 'オブジェクトへのアクセスを制御'), t('Choose how to protect object data', 'オブジェクトデータの保護')];
  useEffect(() => { shell?.setProject(project); }, [shell, project]);
  useEffect(() => shell?.registerNavigation(() => navigateRef.current?.(null)), [shell]);
  const client = useRef(request); client.current = request;
  const live = useRef(true), controller = useRef(new AbortController()), epoch = useRef(0);
  const [bucket, setBucket] = useState(null), [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false);
  const [error, setError] = useState(null), [notice, setNotice] = useState(null);
  const [pageTokens, setPageTokens] = useState(['']), [next, setNext] = useState('');
  const [refresh, setRefresh] = useState(0), [panel, setPanel] = useState(null);
  const [name, setName] = useState(''), [location, setLocation] = useState('US');
  const [file, setFile] = useState(null), [target, setTarget] = useState(null), [confirmation, setConfirmation] = useState('');
  const pageToken = pageTokens[pageTokens.length - 1];
  useEffect(() => {
    if (firstFocus.current) { firstFocus.current = false; return; }
    const target = panel === 'create' ? stepTitle.current : !panel && !object && opener.current?.isConnected ? opener.current : title.current;
    target?.focus();
  }, [panel, step, object]); // Language changes do not move focus or reset draft state.

  useEffect(() => {
    live.current = true;
    const currentController = new AbortController(); controller.current = currentController;
    setBucket(null); setObject(null); setRows([]); setPanel(null); setPageTokens(['']); setError(null); setNotice(null); setBusy(false);
    ++epoch.current;
    return () => { live.current = false; currentController.abort(); ++epoch.current; };
  }, [project]);

  useEffect(() => {
    const revision = ++epoch.current, abort = new AbortController();
    const params = new URLSearchParams({maxResults: '100'});
    if (!bucket) params.set('project', project);
    if (pageToken) params.set('pageToken', pageToken);
    const path = bucket ? `${API}/b/${encodeURIComponent(bucket.name)}/o` : `${API}/b`;
    setLoading(true); setRows([]); setNext(''); setError(null);
    (async () => {
      try {
        const response = await client.current(`${path}?${params}`, {signal: abort.signal});
        const data = await response.json();
        if (!Array.isArray(data.items)) throw new StorageViewError('list');
        if (live.current && revision === epoch.current) { setRows(data.items); setNext(data.nextPageToken || ''); }
      } catch (e) {
        if (!abort.signal.aborted && live.current && revision === epoch.current) setError(captureError(e));
      } finally { if (live.current && revision === epoch.current) setLoading(false); }
    })();
    return () => abort.abort();
  }, [bucket, project, pageToken, refresh]);

  function navigate(value) {
    ++epoch.current; setObject(null); setBucket(value); setPageTokens(['']); setPanel(null); setRows([]); setError(null); setNotice(null); setRefresh(n => n + 1);
  }
  navigateRef.current = navigate;
  function open(value, item = null) {
    opener.current = document.activeElement;
    setStep(0); setLocationType('multi-region'); setPanel(value); setTarget(item); setConfirmation(''); setName(''); setFile(null); setLocation('US'); setError(null); setNotice(null);
  }
  async function mutate(action) {
    const revision = epoch.current, activeController = controller.current;
    setBusy(true); setError(null); setNotice(null);
    try { await action(() => live.current && revision === epoch.current); }
    catch (e) { if (live.current && revision === epoch.current && e.name !== 'AbortError') setError(captureError(e)); }
    finally { if (live.current && activeController === controller.current) setBusy(false); }
  }
  async function createBucket(event) {
    event.preventDefault();
    if (step < steps.length - 1) { setStep(value => value + 1); return; }
    await mutate(async current => {
      await client.current(`${API}/b?${new URLSearchParams({project})}`, {
        method: 'POST', signal: controller.current.signal, headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name, location, storageClass: 'STANDARD'})
      });
      if (current()) { setPanel(null); setPageTokens(['']); setRefresh(n => n + 1); setNotice({kind: 'bucket-created', name}); }
    });
  }
  async function uploadFile(event) {
    event.preventDefault();
    await mutate(async current => {
      if (!file || file.size > MAX_FILE) throw new StorageViewError('file');
      if (!name || name === '.' || name === '..' || /[\u0000\r\n]/.test(name) || new TextEncoder().encode(name).length > 1024) throw new StorageViewError('name');
      const bytes = await file.arrayBuffer();
      try { new TextDecoder('utf-8', {fatal: true}).decode(bytes); } catch { throw new StorageViewError('encoding'); }
      if (!current()) return;
      await client.current(`${UPLOAD}/b/${encodeURIComponent(bucket.name)}/o?${new URLSearchParams({uploadType: 'media', name, ifGenerationMatch: '0'})}`, {
        method: 'POST', signal: controller.current.signal, headers: {'Content-Type': 'text/plain; charset=utf-8'}, body: bytes
      });
      if (current()) { setPanel(null); setPageTokens(['']); setRefresh(n => n + 1); setNotice({kind: 'object-uploaded', name}); }
    });
  }
  async function remove(event) {
    event.preventDefault();
    if (confirmation !== target.name) return;
    await mutate(async current => {
      const path = panel === 'delete-object'
        ? `${objectURL(bucket.name, target.name)}?${new URLSearchParams({ifGenerationMatch: target.generation})}`
        : `${API}/b/${encodeURIComponent(target.name)}`;
      await client.current(path, {method: 'DELETE', signal: controller.current.signal});
      if (current()) {
        if (panel === 'delete-object') setObject(null);
        setPanel(null); setPageTokens(['']); setRefresh(n => n + 1);
        setNotice({kind: panel === 'delete-object' ? 'object-deleted' : 'bucket-deleted', name: target.name});
      }
    });
  }
  async function download(item) {
    await mutate(async current => {
      const response = await client.current(`${objectURL(bucket.name, item.name)}?${new URLSearchParams({alt: 'media', ifGenerationMatch: item.generation})}`, {signal: controller.current.signal});
      const blob = await response.blob();
      if (!current()) return;
      const url = URL.createObjectURL(blob), anchor = document.createElement('a');
      anchor.href = url; anchor.download = item.name.split('/').pop() || 'object'; anchor.hidden = true;
      document.body.appendChild(anchor); anchor.click(); anchor.remove();
      // Give the browser time to begin its download before revoking this local URL.
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
      setNotice({kind: 'download', name: item.name});
    });
  }
  async function inspect(item) {
    await mutate(async current => {
      const response = await client.current(`${objectURL(bucket.name, item.name)}?${new URLSearchParams({ifGenerationMatch: item.generation})}`, {signal: controller.current.signal});
      const data = await response.json();
      if (data.name !== item.name || typeof data.generation !== 'string') throw new StorageViewError('metadata');
      if (current()) { setObject(data); setPanel(null); setError(null); setNotice(null); }
    });
  }
  const disabled = loading || busy;
  const deleting = panel === 'delete-object' || panel === 'delete-bucket';
  const availableLocations = locations.filter(value => locationType === 'multi-region' ? ['US', 'EU'].includes(value) : !['US', 'EU'].includes(value));
  return <section className="gcp-storage" aria-label="Cloud Storage" aria-busy={busy}>
    <nav aria-label={t('Cloud Storage breadcrumbs', 'Cloud Storage パンくず')} className="gcp-storage-breadcrumbs">
      <button type="button" disabled={busy} onClick={() => navigate(null)}>Cloud Storage</button><span aria-hidden="true">/</span>
      <button type="button" disabled={busy} onClick={() => navigate(null)} aria-current={!bucket && !panel ? 'page' : undefined}>{t('Buckets', 'バケット')}</button>
      {bucket && <><span aria-hidden="true">/</span><button type="button" disabled={busy} onClick={() => {setObject(null); setPanel(null); setError(null);}} aria-current={!object && !panel ? 'page' : undefined}>{bucket.name}</button></>}
      {object && <><span aria-hidden="true">/</span><span aria-current="page">{object.name}</span></>}
    </nav>
    <div className="gcp-storage-title"><h1 ref={title} tabIndex={-1}>{panel === 'create' ? t('Create a bucket', 'バケットを作成') : object ? t('Object details', 'オブジェクトの詳細') : bucket ? t('Bucket details', 'バケットの詳細') : t('Buckets', 'バケット')}</h1><span className="gcp-storage-badge">{t('Independent simulation', '独立シミュレーター')}</span></div>
    <p className="gcp-storage-limits">{t('Local JSON API subset. No real Google Cloud resources. Standard storage only; IAM/ACL editing, billing, retention, version history and project switching are unavailable.', 'ローカル JSON API の対応範囲です。実際の Google Cloud リソースは作成しません。Standard のみ対応し、IAM・ACL編集、課金、保持、履歴、プロジェクト切替は未対応です。')}</p>
    {error && <div className="gcp-storage-error" role="alert">{error.kind === 'client' ? t(...clientErrors[error.code]) : error.message}<p>{t('403 means the selected exercise identity is not authorized. Only successful server responses confirm changes.', '403 は選択した演習IDに権限がないことを示します。サーバーが成功した変更だけを確定表示します。')}</p></div>}
    {notice && <p className="gcp-storage-notice" role="status">{noticeText(notice, t)}</p>}
    {panel === 'create' ? <form className="gcp-storage-form gcp-storage-wizard" onSubmit={createBucket}>
      <ol className="gcp-storage-steps" aria-label={t('Bucket creation steps', 'バケット作成の手順')}>{steps.map((label, index) => <li key={index} aria-current={step === index ? 'step' : undefined}><span className="gcp-storage-step-number">{index + 1}</span><button type="button" disabled={busy || index > step} onClick={() => setStep(index)}>{label}</button></li>)}</ol>
      <div className="gcp-storage-step-content"><h2 ref={stepTitle} tabIndex={-1}>{steps[step]}</h2>
      {step === 0 && <><label htmlFor={`${id}-name`}>{t('Bucket name', 'バケット名')}</label><input id={`${id}-name`} required minLength={3} maxLength={222} value={name} onChange={e => setName(e.target.value)} disabled={busy} aria-describedby={`${id}-bucket-help`}/><p id={`${id}-bucket-help`}>{t('Names are unique within this local environment. Use lowercase letters, numbers, hyphens, underscores or dots. The server validates reserved names.', 'ローカル環境内で一意の名前にします。小文字、数字、ハイフン、アンダースコア、ドットを使用できます。予約名はサーバーで検証します。')}</p></>}
      {step === 1 && <><fieldset disabled={busy}><legend>{t('Location type', 'ロケーションのタイプ')}</legend>{[['multi-region', t('Multi-region', 'マルチリージョン')], ['region', t('Region', 'リージョン')]].map(([value, label]) => <label className="gcp-storage-radio" key={value}><input type="radio" name={`${id}-location-type`} checked={locationType === value} onChange={() => {setLocationType(value); setLocation(value === 'multi-region' ? 'US' : 'US-EAST1');}}/>{label}</label>)}</fieldset><label htmlFor={`${id}-location`}>{t('Location', 'ロケーション')}</label><select id={`${id}-location`} value={location} onChange={e => setLocation(e.target.value)} disabled={busy}>{availableLocations.map(value => <option key={value}>{value}</option>)}</select><p>{t('Location is saved metadata only. There is no geographic replication, dual-region or turbo replication.', 'ロケーションは保存されるメタデータです。実際の地理分散、デュアルリージョン、高速レプリケーションは未対応です。')}</p></>}
      {step === 2 && <><h3>{t('Default storage class', 'デフォルトのストレージクラス')}</h3><p>Standard</p><p>{t('Autoclass, other storage classes, hierarchical namespace and caches are not implemented.', 'Autoclass、他のストレージクラス、階層型名前空間、キャッシュは未対応です。')}</p></>}
      {step === 3 && <><h3>{t('Access control', 'アクセス制御')}</h3><p>{t('The configured local project owner controls access. Google IAM, public access settings, ACLs and IP filters cannot be configured here.', '設定済みのローカルプロジェクト所有者がアクセスを制御します。Google IAM、公開アクセス、ACL、IPフィルターは設定できません。')}</p></>}
      {step === 4 && <><h3>{t('Data protection', 'データ保護')}</h3><p>{t('Soft delete, object retention, Object Versioning, encryption options and recovery are not implemented. Deletion is immediate in this simulator.', '論理削除、オブジェクト保持、Object Versioning、暗号化オプション、復元は未対応です。このシミュレーターでは即時削除します。')}</p><dl className="gcp-storage-metadata"><dt>{t('Bucket name', 'バケット名')}</dt><dd>{name}</dd><dt>{t('Location', 'ロケーション')}</dt><dd>{location}</dd><dt>{t('Storage class', 'ストレージクラス')}</dt><dd>Standard</dd></dl></>}
      <div className="gcp-storage-actions"><button className="gcp-storage-primary" disabled={busy} type="submit">{busy ? t('Creating…', '作成中…') : step < 4 ? t('Continue', '続行') : t('Create', '作成')}</button>{step > 0 && <button type="button" disabled={busy} onClick={() => setStep(value => value - 1)}>{t('Back', '戻る')}</button>}<button type="button" disabled={busy} onClick={() => setPanel(null)}>{t('Cancel', 'キャンセル')}</button></div></div>
    </form> : panel === 'upload' ? <form className="gcp-storage-form" onSubmit={uploadFile}>
      <h2>{t('Upload files', 'ファイルをアップロード')}</h2><p>{t('One UTF-8 text file, at most 1 MiB. Existing objects are never overwritten.', '1 MiB 以下の UTF-8 テキストファイルを1つ選択します。既存オブジェクトは上書きしません。')}</p>
      <label htmlFor={`${id}-file`}>{t('Text file', 'テキストファイル')}</label><input id={`${id}-file`} type="file" autoFocus required disabled={busy} accept="text/*,.txt,.json,.csv,.md" onChange={e => {const value = e.target.files?.[0] || null; setFile(value); setName(value?.name || '');}}/>
      <label htmlFor={`${id}-object`}>{t('Object name', 'オブジェクト名')}</label><input id={`${id}-object`} value={name} onChange={e => setName(e.target.value)} required disabled={busy} aria-describedby={`${id}-object-help`}/><p id={`${id}-object-help`}>{t('Slashes and literal percent characters are exact name content. Maximum 1024 UTF-8 bytes.', 'スラッシュとパーセント文字も名前の一部です。上限は1024 UTF-8バイトです。')}</p>
      <div className="gcp-storage-actions"><button className="gcp-storage-primary" type="submit" disabled={busy || !file}>{busy ? t('Uploading…', 'アップロード中…') : t('Upload', 'アップロード')}</button><button type="button" disabled={busy} onClick={() => setPanel(null)}>{t('Cancel', 'キャンセル')}</button></div>
    </form> : deleting ? <form className="gcp-storage-form" onSubmit={remove}>
      <h2>{panel === 'delete-object' ? t('Delete object', 'オブジェクトを削除') : t('Delete bucket', 'バケットを削除')}</h2><p className="gcp-storage-name">{target.name}</p><p>{panel === 'delete-object' ? t('Only the selected live generation is deleted. A changed object is refused.', '選択した現行世代のみ削除します。変更済みのオブジェクトは拒否されます。') : t('The bucket must be empty. Its objects are not deleted for you.', 'バケットは空である必要があります。内部のオブジェクトを自動削除しません。')}</p>
      <label htmlFor={`${id}-confirm`}>{panel === 'delete-object' ? t('Type the exact object name to confirm', '確認のため正確なオブジェクト名を入力') : t('Type the exact bucket name to confirm', '確認のため正確なバケット名を入力')}</label><input id={`${id}-confirm`} autoFocus value={confirmation} onChange={e => setConfirmation(e.target.value)} disabled={busy} autoComplete="off"/>
      <div className="gcp-storage-actions"><button type="submit" className="gcp-storage-danger" disabled={busy || confirmation !== target.name}>{busy ? t('Deleting…', '削除中…') : t('Delete', '削除')}</button><button type="button" disabled={busy} onClick={() => setPanel(null)}>{t('Cancel', 'キャンセル')}</button></div>
    </form> : object ? <>
      <h2 className="gcp-storage-name">{object.name}</h2><div className="gcp-storage-toolbar"><button disabled={busy} onClick={() => download(object)}>{t('Download', 'ダウンロード')}</button><button disabled={busy} onClick={() => open('delete-object', object)}>{t('Delete', '削除')}</button><button disabled={busy} onClick={() => setObject(null)}>{t('Back to objects', 'オブジェクト一覧に戻る')}</button></div>
      <h3>{t('Metadata', 'メタデータ')}</h3><dl className="gcp-storage-metadata">{[[t('Name', '名前'), object.name], [t('Bucket', 'バケット'), object.bucket], [t('Size', 'サイズ'), displayBytes(object.size)], [t('Content type', 'コンテンツタイプ'), object.contentType], [t('Generation', '世代'), object.generation], [t('Created', '作成日時'), object.timeCreated], [t('Updated', '更新日時'), object.updated]].map(([label, value]) => <React.Fragment key={label}><dt>{label}</dt><dd>{value ?? '—'}</dd></React.Fragment>)}</dl><p>{t('Read-only live metadata. Metadata editing, access grants and previous versions are unavailable.', '実データの読み取り専用メタデータです。編集、アクセス権付与、旧世代の操作は未対応です。')}</p>
    </> : <>
      {bucket && <><h2 className="gcp-storage-name">{bucket.name}</h2><div className="gcp-storage-summary"><span>{t('Location', 'ロケーション')}: {bucket.location}</span><span>{t('Storage class', 'ストレージクラス')}: Standard</span></div><h3 className="gcp-storage-section">{t('Objects', 'オブジェクト')}</h3></>}
      <div className="gcp-storage-toolbar">{bucket ? <details className="gcp-storage-upload-menu"><summary>{t('Upload', 'アップロード')}</summary><button type="button" disabled={disabled} onClick={event => {event.currentTarget.closest('details').open = false; open('upload');}}>{t('Upload files', 'ファイルをアップロード')}</button></details> : <button className="gcp-storage-primary" disabled={disabled} onClick={() => open('create')}>{t('Create bucket', 'バケットを作成')}</button>}<button disabled={disabled} onClick={() => setRefresh(n => n + 1)}>{t('Refresh', '更新')}</button>{bucket && <button disabled={disabled} onClick={() => navigate(null)}>{t('Back to buckets', 'バケット一覧に戻る')}</button>}</div>
      <p role="status">{loading ? t('Loading…', '読み込み中…') : t(`${rows.length} ${bucket ? 'objects' : 'buckets'} on this page`, `このページに${rows.length}件の${bucket ? 'オブジェクト' : 'バケット'}`)}</p>
      <div className="gcp-storage-table-scroll" role="region" aria-label={bucket ? t('Objects table', 'オブジェクト一覧') : t('Buckets table', 'バケット一覧')} tabIndex={0}>
        <table><caption>{bucket ? t(`Objects in ${bucket.name}`, `${bucket.name} のオブジェクト`) : t(`Buckets in project ${project}`, `プロジェクト ${project} のバケット`)}</caption><thead><tr><th scope="col">{t('Name', '名前')}</th>{bucket ? <><th scope="col">{t('Size', 'サイズ')}</th><th scope="col">{t('Content type', 'コンテンツタイプ')}</th><th scope="col">{t('Generation', '世代')}</th></> : <><th scope="col">{t('Location', 'ロケーション')}</th><th scope="col">{t('Storage class', 'ストレージクラス')}</th></>}<th scope="col">{t('Actions', '操作')}</th></tr></thead>
        <tbody>{rows.map(item => <tr key={item.name}><th scope="row"><button className="gcp-storage-link" disabled={busy} onClick={() => bucket ? inspect(item) : navigate(item)}>{item.name}</button></th>{bucket ? <><td>{displayBytes(item.size)}</td><td>{item.contentType}</td><td>{item.generation}</td></> : <><td>{item.location}</td><td>{item.storageClass}</td></>}
          <td><div className="gcp-storage-row-actions">{bucket && <button disabled={disabled} onClick={() => download(item)} aria-label={t(`Download ${item.name}`, `${item.name} をダウンロード`)}>{t('Download', 'ダウンロード')}</button>}<button disabled={disabled} onClick={() => open(bucket ? 'delete-object' : 'delete-bucket', item)} aria-label={t(`Delete ${bucket ? 'object' : 'bucket'} ${item.name}`, `${bucket ? 'オブジェクト' : 'バケット'} ${item.name} を削除`)}>{t('Delete', '削除')}</button></div></td></tr>)}</tbody></table>
      </div>
      {!loading && !error && rows.length === 0 && <p>{bucket ? t('No objects to display. Upload a text file to get started.', '表示するオブジェクトがありません。テキストファイルをアップロードしてください。') : t('No buckets to display. Create a bucket to get started.', '表示するバケットがありません。バケットを作成してください。')}</p>}
      <nav className="gcp-storage-pagination" aria-label={bucket ? t('Object pages', 'オブジェクトのページ') : t('Bucket pages', 'バケットのページ')}><button disabled={disabled || pageTokens.length === 1} onClick={() => setPageTokens(tokens => tokens.slice(0, -1))}>{t('Previous page', '前のページ')}</button><span>{t(`Page ${pageTokens.length}`, `${pageTokens.length}ページ`)}</span><button disabled={disabled || !next} onClick={() => setPageTokens(tokens => [...tokens, next])}>{t('Next page', '次のページ')}</button></nav>
    </>}
  </section>;
}
