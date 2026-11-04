import React, {useEffect, useId, useRef, useState} from 'react';
import './gcp-storage.css';

const API = '/_simulator/providers/gcp/storage/v1';
const UPLOAD = '/_simulator/providers/gcp/upload/storage/v1';
const MAX_FILE = 1 << 20;
const locations = ['US', 'EU', 'US-EAST1', 'US-WEST1', 'ASIA-NORTHEAST1', 'EUROPE-WEST1'];
const objectURL = (bucket, name) => `${API}/b/${encodeURIComponent(bucket)}/o/${encodeURIComponent(name)}`;
const displayBytes = value => `${Number(value || 0).toLocaleString()} B`;
const errorText = error => error?.message || 'The Cloud Storage request failed. Try again.';

// Root supplies its authenticated provider-neutral request seam (raw Response).
// No actor, AWS identity, OAuth credential or project authority is minted here.
export default function GCPStorageView({request, project = 'tenkacloud-local'}) {
  const id = useId();
  const client = useRef(request); client.current = request;
  const live = useRef(true), controller = useRef(new AbortController()), epoch = useRef(0);
  const [bucket, setBucket] = useState(null), [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false);
  const [error, setError] = useState(''), [notice, setNotice] = useState('');
  const [pageTokens, setPageTokens] = useState(['']), [next, setNext] = useState('');
  const [refresh, setRefresh] = useState(0), [panel, setPanel] = useState(null);
  const [name, setName] = useState(''), [location, setLocation] = useState('US');
  const [file, setFile] = useState(null), [target, setTarget] = useState(null), [confirmation, setConfirmation] = useState('');
  const pageToken = pageTokens[pageTokens.length - 1];

  useEffect(() => {
    live.current = true;
    const currentController = new AbortController(); controller.current = currentController;
    setBucket(null); setRows([]); setPanel(null); setPageTokens(['']); setError(''); setNotice(''); setBusy(false);
    ++epoch.current;
    return () => { live.current = false; currentController.abort(); ++epoch.current; };
  }, [project]);

  useEffect(() => {
    const revision = ++epoch.current, abort = new AbortController();
    const params = new URLSearchParams({maxResults: '100'});
    if (!bucket) params.set('project', project);
    if (pageToken) params.set('pageToken', pageToken);
    const path = bucket ? `${API}/b/${encodeURIComponent(bucket.name)}/o` : `${API}/b`;
    setLoading(true); setRows([]); setNext(''); setError('');
    (async () => {
      try {
        const response = await client.current(`${path}?${params}`, {signal: abort.signal});
        const data = await response.json();
        if (!Array.isArray(data.items)) throw Error('The server returned an invalid Cloud Storage list.');
        if (live.current && revision === epoch.current) { setRows(data.items); setNext(data.nextPageToken || ''); }
      } catch (e) {
        if (!abort.signal.aborted && live.current && revision === epoch.current) setError(errorText(e));
      } finally { if (live.current && revision === epoch.current) setLoading(false); }
    })();
    return () => abort.abort();
  }, [bucket, project, pageToken, refresh]);

  function navigate(value) {
    ++epoch.current; setBucket(value); setPageTokens(['']); setPanel(null); setRows([]); setError(''); setNotice(''); setRefresh(n => n + 1);
  }
  function open(value, item = null) {
    setPanel(value); setTarget(item); setConfirmation(''); setName(''); setFile(null); setLocation('US'); setError(''); setNotice('');
  }
  async function mutate(action) {
    const revision = epoch.current, activeController = controller.current;
    setBusy(true); setError(''); setNotice('');
    try { await action(() => live.current && revision === epoch.current); }
    catch (e) { if (live.current && revision === epoch.current && e.name !== 'AbortError') setError(errorText(e)); }
    finally { if (live.current && activeController === controller.current) setBusy(false); }
  }
  async function createBucket(event) {
    event.preventDefault();
    await mutate(async current => {
      await client.current(`${API}/b?${new URLSearchParams({project})}`, {
        method: 'POST', signal: controller.current.signal, headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name, location, storageClass: 'STANDARD'})
      });
      if (current()) { setPanel(null); setPageTokens(['']); setRefresh(n => n + 1); setNotice(`Bucket ${name} created.`); }
    });
  }
  async function uploadFile(event) {
    event.preventDefault();
    await mutate(async current => {
      if (!file || file.size > MAX_FILE) throw Error('Choose one text file of at most 1 MiB.');
      if (!name || name === '.' || name === '..' || /[\u0000\r\n]/.test(name) || new TextEncoder().encode(name).length > 1024) throw Error('Enter an object name of 1–1024 UTF-8 bytes without NUL, CR or LF.');
      const bytes = await file.arrayBuffer();
      try { new TextDecoder('utf-8', {fatal: true}).decode(bytes); } catch { throw Error('This console uploads UTF-8 text files only.'); }
      if (!current()) return;
      await client.current(`${UPLOAD}/b/${encodeURIComponent(bucket.name)}/o?${new URLSearchParams({uploadType: 'media', name, ifGenerationMatch: '0'})}`, {
        method: 'POST', signal: controller.current.signal, headers: {'Content-Type': 'text/plain; charset=utf-8'}, body: bytes
      });
      if (current()) { setPanel(null); setPageTokens(['']); setRefresh(n => n + 1); setNotice(`Object ${name} uploaded. Existing objects were not overwritten.`); }
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
        setPanel(null); setPageTokens(['']); setRefresh(n => n + 1);
        setNotice(`${panel === 'delete-object' ? 'Object' : 'Bucket'} ${target.name} deleted.`);
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
      setNotice(`Download requested for ${item.name}.`);
    });
  }
  const disabled = loading || busy;
  const deleting = panel === 'delete-object' || panel === 'delete-bucket';
  return <section className="gcp-storage" aria-label="Cloud Storage" aria-busy={busy}>
    <div className="gcp-storage-context"><span>Google Cloud · local simulator</span><span>Project: <strong>{project}</strong></span></div>
    <nav aria-label="Cloud Storage breadcrumbs" className="gcp-storage-breadcrumbs">
      <button type="button" disabled={busy} onClick={() => navigate(null)}>Cloud Storage</button><span aria-hidden="true">/</span>
      {bucket ? <><button type="button" disabled={busy} onClick={() => navigate(null)}>Buckets</button><span aria-hidden="true">/</span><span>{bucket.name}</span></> : <span>Buckets</span>}
    </nav>
    <div className="gcp-storage-title"><h1>{panel === 'create' ? 'Create a bucket' : bucket ? 'Bucket details' : 'Buckets'}</h1><span className="gcp-storage-badge">Independent simulation</span></div>
    <p className="gcp-storage-limits">Local Cloud Storage JSON API subset. No real Google Cloud resources or credentials. Standard storage only; no IAM/ACL editor, resumable uploads, billing, retention or version history.</p>
    {error && <div className="gcp-storage-error" role="alert">{error}<p>403 means the selected exercise identity is not authorized. Changes are only confirmed after the server succeeds.</p></div>}
    {notice && <p className="gcp-storage-notice" role="status">{notice}</p>}
    {panel === 'create' ? <form className="gcp-storage-form" onSubmit={createBucket}>
      <h2>Name your bucket</h2><label htmlFor={`${id}-name`}>Bucket name</label>
      <input id={`${id}-name`} autoFocus required minLength={3} maxLength={222} value={name} onChange={e => setName(e.target.value)} disabled={busy} aria-describedby={`${id}-bucket-help`}/>
      <p id={`${id}-bucket-help`}>Names are globally unique within this local environment. Use lowercase letters, numbers, hyphens, underscores or dots; the server validates reserved names.</p>
      <h2>Choose where to store your data</h2><label htmlFor={`${id}-location`}>Location</label>
      <select id={`${id}-location`} value={location} onChange={e => setLocation(e.target.value)} disabled={busy}>{locations.map(value => <option key={value}>{value}</option>)}</select>
      <p>Location is metadata in this simulator; no geographic replication is performed.</p>
      <h2>Choose a storage class</h2><p>Standard. Other storage classes and Autoclass are unsupported.</p>
      <p>Access control and data-protection settings are not simulated by this form. The host's exercise authorization still applies.</p>
      <div className="gcp-storage-actions"><button className="gcp-storage-primary" disabled={busy} type="submit">{busy ? 'Creating…' : 'Create'}</button><button type="button" disabled={busy} onClick={() => setPanel(null)}>Cancel</button></div>
    </form> : panel === 'upload' ? <form className="gcp-storage-form" onSubmit={uploadFile}>
      <h2>Upload files</h2><p>Choose one UTF-8 text file, at most 1 MiB. This console uses media upload and never overwrites an existing object.</p>
      <label htmlFor={`${id}-file`}>Text file</label><input id={`${id}-file`} type="file" autoFocus required disabled={busy} accept="text/*,.txt,.json,.csv,.md" onChange={e => {const value = e.target.files?.[0] || null; setFile(value); setName(value?.name || '');}}/>
      <label htmlFor={`${id}-object`}>Object name</label><input id={`${id}-object`} value={name} onChange={e => setName(e.target.value)} required disabled={busy} aria-describedby={`${id}-object-help`}/>
      <p id={`${id}-object-help`}>Slashes and literal percent characters remain part of the exact object name. Maximum 1024 UTF-8 bytes.</p>
      <div className="gcp-storage-actions"><button className="gcp-storage-primary" type="submit" disabled={busy || !file}>{busy ? 'Uploading…' : 'Upload'}</button><button type="button" disabled={busy} onClick={() => setPanel(null)}>Cancel</button></div>
    </form> : deleting ? <form className="gcp-storage-form" onSubmit={remove}>
      <h2>Delete {panel === 'delete-object' ? 'object' : 'bucket'}</h2><p className="gcp-storage-name">{target.name}</p>
      <p>{panel === 'delete-object' ? 'This deletes the selected live generation. A changed object will be refused; refresh and review it.' : 'The bucket must be empty. This action does not delete its objects for you.'}</p>
      <label htmlFor={`${id}-confirm`}>Type the exact {panel === 'delete-object' ? 'object' : 'bucket'} name to confirm</label><input id={`${id}-confirm`} autoFocus value={confirmation} onChange={e => setConfirmation(e.target.value)} disabled={busy} autoComplete="off"/>
      <div className="gcp-storage-actions"><button type="submit" className="gcp-storage-danger" disabled={busy || confirmation !== target.name}>{busy ? 'Deleting…' : 'Delete'}</button><button type="button" disabled={busy} onClick={() => setPanel(null)}>Cancel</button></div>
    </form> : <>
      {bucket && <><h2 className="gcp-storage-name">{bucket.name}</h2><div className="gcp-storage-summary"><span>Location: {bucket.location}</span><span>Storage class: Standard</span></div><div className="gcp-storage-section">Objects</div></>}
      <div className="gcp-storage-toolbar"><button className="gcp-storage-primary" disabled={disabled} onClick={() => open(bucket ? 'upload' : 'create')}>{bucket ? 'Upload files' : 'Create bucket'}</button><button disabled={disabled} onClick={() => setRefresh(n => n + 1)}>Refresh</button>{bucket && <button disabled={disabled} onClick={() => {navigate(null);}}>Back to buckets</button>}</div>
      <p role="status">{loading ? 'Loading…' : `${rows.length} ${bucket ? 'objects' : 'buckets'} on this page`}</p>
      <div className="gcp-storage-table-scroll" role="region" aria-label={bucket ? 'Objects table' : 'Buckets table'} tabIndex={0}>
        <table><caption>{bucket ? `Objects in ${bucket.name}` : `Buckets in project ${project}`}</caption><thead><tr><th scope="col">Name</th>{bucket ? <><th scope="col">Size</th><th scope="col">Content type</th><th scope="col">Generation</th></> : <><th scope="col">Location</th><th scope="col">Storage class</th></>}<th scope="col">Actions</th></tr></thead>
        <tbody>{rows.map(item => <tr key={item.name}><th scope="row">{bucket ? <span className="gcp-storage-name">{item.name}</span> : <button className="gcp-storage-link" disabled={busy} onClick={() => navigate(item)}>{item.name}</button>}</th>
          {bucket ? <><td>{displayBytes(item.size)}</td><td>{item.contentType}</td><td>{item.generation}</td></> : <><td>{item.location}</td><td>{item.storageClass}</td></>}
          <td><div className="gcp-storage-row-actions">{bucket && <button disabled={disabled} onClick={() => download(item)} aria-label={`Download ${item.name}`}>Download</button>}<button disabled={disabled} onClick={() => open(bucket ? 'delete-object' : 'delete-bucket', item)} aria-label={`Delete ${bucket ? 'object' : 'bucket'} ${item.name}`}>Delete</button></div></td></tr>)}</tbody></table>
      </div>
      {!loading && !error && rows.length === 0 && <p>No {bucket ? 'objects' : 'buckets'} to display. {bucket ? 'Upload a text file to get started.' : 'Create a bucket to get started.'}</p>}
      <nav className="gcp-storage-pagination" aria-label={bucket ? 'Object pages' : 'Bucket pages'}><button disabled={disabled || pageTokens.length === 1} onClick={() => setPageTokens(tokens => tokens.slice(0, -1))}>Previous page</button><span>Page {pageTokens.length}</span><button disabled={disabled || !next} onClick={() => setPageTokens(tokens => [...tokens, next])}>Next page</button></nav>
    </>}
  </section>;
}
