import React, {useEffect, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import './azure-blob.css';

const VERSION='2023-11-03', MAX_BLOB=8*1024*1024;
const children=(node,name)=>[...node.children].filter(x=>x.localName===name);
const child=(node,name)=>children(node,name)[0];
const value=(node,name)=>child(node,name)?.textContent||'';
async function boundedBytes(response, limit, signal) {
  if (!response.body) return new Uint8Array();
  const reader=response.body.getReader(), parts=[];let size=0;
  try {
    for (;;) {
      if(signal.aborted)throw Error('Aborted');
      const {value:part,done}=await reader.read();if(done)break;
      size+=part.byteLength;if(size>limit)throw Error('LocalDisplayLimit');parts.push(part);
    }
    const data=new Uint8Array(size);let offset=0;
    for(const part of parts){data.set(part,offset);offset+=part.byteLength;}return data;
  } finally {void reader.cancel().catch(()=>{});reader.releaseLock();}
}
async function listing(response, blobs, signal) {
  const text=new TextDecoder('utf-8',{fatal:true}).decode(await boundedBytes(response,1024*1024,signal));
  if(/<!DOCTYPE|<!ENTITY/i.test(text))throw Error('UnsafeXML');
  const doc=new DOMParser().parseFromString(text,'application/xml');
  if(doc.querySelector('parsererror')||doc.documentElement?.localName!=='EnumerationResults')throw Error('InvalidXML');
  let count=0;const walk=(node,depth)=>{if(++count>10000||depth>16)throw Error('LocalDisplayLimit');for(const n of node.childNodes)walk(n,depth+1);};walk(doc.documentElement,0);
  const group=child(doc.documentElement,blobs?'Blobs':'Containers');if(!group)throw Error('InvalidXML');
  const rows=children(group,blobs?'Blob':'Container').map(node=>{const props=child(node,'Properties');if(!props)throw Error('InvalidXML');return {name:value(node,'Name'),modified:value(props,'Last-Modified'),etag:value(props,'Etag'),size:value(props,'Content-Length'),type:value(props,'BlobType')};});
  if(rows.length>100||rows.some(x=>!x.name))throw Error('InvalidXML');return {rows,next:value(doc.documentElement,'NextMarker')};
}
function errorText(error){
  const code=/^([A-Za-z][A-Za-z0-9]+|[0-9]{3}):/.exec(String(error?.message||''))?.[1];
  if(code==='403'||code==='AuthorizationPermissionMismatch'||code==='AuthorizationFailure')return 'Access denied. This Azure exercise subject does not have permission for this operation.';
  if(code==='404'||code==='ContainerNotFound'||code==='BlobNotFound')return 'The resource was not found. Refresh the list; it may have been removed.';
  if(code==='409'||code==='ContainerAlreadyExists')return 'The container already exists. Choose another name or refresh.';
  if(code==='412'||code==='ConditionNotMet')return 'The blob changed after you selected it. Refresh before retrying.';
  return 'The request did not complete. Refresh and check your host session, permissions and the documented local API limits.';
}
export default function AzureBlobView({request,account='tenkacloudlocal'}) {
  const {locale,setLocale,t}=useLocale();
  const pane=useRef(null), opener=useRef(null), firstField=useRef(null);
  const [menuFilter,setMenuFilter]=useState('');
  const [properties,setProperties]=useState(null);
  const [container,setContainer]=useState(''),[rows,setRows]=useState([]),[next,setNext]=useState(''),[selection,setSelection]=useState([]),[filter,setFilter]=useState('');
  const [busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState(''),[modal,setModal]=useState(''),[name,setName]=useState(''),[confirm,setConfirm]=useState(''),[files,setFiles]=useState([]),[overwrite,setOverwrite]=useState('');
  useEffect(()=>{const node=pane.current;if(!node)return;if(modal&&!node.open){opener.current=document.activeElement;node.showModal();firstField.current?.focus();}else if(!modal&&node.open){node.close();opener.current?.focus();}},[modal]);
  const lifecycle=useRef({active:true,epoch:0,controller:null,busy:false}),downloadURLs=useRef(new Set());
  const validAccount=/^[a-z0-9]{3,24}$/.test(account);
  const base='/_simulator/providers/azure/'+encodeURIComponent(account);
  const containerPath=c=>base+'/'+encodeURIComponent(c);
  const blobPath=(c,n)=>{
    if(!n||[...n].length>1024||/[\u0000-\u001f\u007f]/.test(n))throw Error('InvalidBlobName');
    // Browser URL parsing normalizes even percent-encoded standalone dot segments.
    if(n.split('/').some(part=>part==='.'||part==='..'))throw Error('BrowserDotSegment');
    return containerPath(c)+'/'+n.split('/').map(encodeURIComponent).join('/');
  };
  async function perform(task) {
    const life=lifecycle.current;if(!life.active||life.busy||!validAccount)return;
    life.busy=true;const epoch=++life.epoch,controller=new AbortController();life.controller=controller;setBusy(true);setError('');setNotice('');
    const current=()=>life.active&&life.epoch===epoch&&!controller.signal.aborted;
    const timer=setTimeout(()=>controller.abort(),30000);
    const native=async(path,options={})=>{
      const response=await request(path,{...options,signal:controller.signal,headers:{'x-ms-version':VERSION,...options.headers}});
      if(!current())throw Error('Aborted');if(!response.ok)throw Error(response.status+': NativeRequestFailed');return response;
    };
    try{await task(native,controller.signal,current);}catch(e){if(life.active&&life.epoch===epoch)setError(e?.message==='BrowserDotSegment'?'This browser cannot safely address a standalone dot path segment. Use a native client for this exact blob name.':errorText(e));}
    finally{clearTimeout(timer);if(life.active&&life.epoch===epoch){life.busy=false;life.controller=null;setBusy(false);}}
  }
  async function fetchPage(native,signal,current,c,marker='') {
    const query=new URLSearchParams(c?{restype:'container',comp:'list',maxresults:'100'}:{comp:'list',maxresults:'100'});if(marker)query.set('marker',marker);
    const page=await listing(await native((c?containerPath(c):base)+'?'+query),!!c,signal);
    if(current()){setContainer(c);setRows(page.rows);setNext(page.next);setSelection([]);setFilter('');}
  }
  const load=(c=container,marker='')=>perform((native,signal,current)=>fetchPage(native,signal,current,c,marker));
  useEffect(()=>{
    const life=lifecycle.current;life.active=true;load('');
    return()=>{life.active=false;++life.epoch;life.busy=false;life.controller?.abort();for(const u of downloadURLs.current)URL.revokeObjectURL(u);downloadURLs.current.clear();};
  },[]); // Root MUST remount with a key for every subject/environment/account change.
  const selected=selection[0];
  const open=kind=>{setError('');setProperties(null);setModal(kind);setName('');setConfirm('');setFiles([]);setOverwrite('');};
  // HEAD is authenticated by the existing request helper and bounded by perform.
  // Do not fall back to the stale list row or fetch object content on failure.
  const showProperties=(row,kind='blob')=>{
    if(lifecycle.current.busy||!row||(kind!=='blob'&&kind!=='container')||(kind==='blob'?!container:!!container))return;
    setSelection([row]);open(kind==='container'?'container-properties':'properties');
    return perform(async(native,signal,current)=>{
      const target=kind==='container'?containerPath(row.name)+'?restype=container':blobPath(container,row.name);
      const response=await native(target,{method:'HEAD'});
      const headers=response.headers;
      const required=(key,limit)=>{const text=headers.get(key);if(!text||text.length>limit)throw Error('InvalidProperties');return text;};
      const metadata=[...headers].filter(([key])=>key.startsWith('x-ms-meta-')).map(([key,value])=>[key.slice(10),value]);
      if(metadata.length>32||new TextEncoder().encode(JSON.stringify(Object.fromEntries(metadata))).length>8192)throw Error('LocalDisplayLimit');
      const detail={kind,name:row.name,etag:required('etag',128),modified:required('last-modified',128),metadata};
      // Container HEAD has no blob type, content type or size contract. Never
      // manufacture these fields from a transport Content-Length or list row.
      if(kind==='blob'){
        const size=required('content-length',16);
        if(!/^(0|[1-9][0-9]*)$/.test(size)||Number(size)>MAX_BLOB)throw Error('InvalidProperties');
        Object.assign(detail,{size,type:required('x-ms-blob-type',32),contentType:required('content-type',256)});
      }
      if(current())setProperties(detail);
    });
  };
  const create=()=>perform(async(native,signal,current)=>{
    if(!/^(?!.*--)[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/.test(name))throw Error('InvalidContainerName');
    await native(containerPath(name)+'?restype=container',{method:'PUT'});await fetchPage(native,signal,current,'');if(current()){setModal('');setNotice('Container created.');}
  });
  const upload=()=>perform(async(native,signal,current)=>{
    const file=files[0];if(!file||file.size>MAX_BLOB||(overwrite&&overwrite!=='replace'))throw Error('LocalDisplayLimit');
    await native(blobPath(container,name),{method:'PUT',headers:{'x-ms-blob-type':'BlockBlob','Content-Type':file.type||'application/octet-stream',...(overwrite==='replace'?{'If-Match':'*'}:{'If-None-Match':'*'})},body:file});
    await fetchPage(native,signal,current,container);if(current()){setModal('');setNotice('Block blob uploaded.');}
  });
  const remove=()=>perform(async(native,signal,current)=>{
    const removingContainer=modal==='delete-container';
    if(!selected||confirm!==selected.name)throw Error('ConfirmationRequired');
    await native(removingContainer?containerPath(selected.name)+'?restype=container':blobPath(container,selected.name),{method:'DELETE',headers:removingContainer?{}:{'If-Match':selected.etag}});
    await fetchPage(native,signal,current,removingContainer?'':container);if(current()){setModal('');setNotice(removingContainer?'Container and all its blobs deleted.':'Blob deleted.');}
  });
  const download=()=>perform(async(native,signal,current)=>{
    const bytes=await boundedBytes(await native(blobPath(container,selected.name)),MAX_BLOB,signal);if(!current())return;
    const url=URL.createObjectURL(new Blob([bytes],{type:'application/octet-stream'}));downloadURLs.current.add(url);
    const a=document.createElement('a');a.href=url;a.download=selected.name.split('/').pop()||'blob';a.click();
    setTimeout(()=>{URL.revokeObjectURL(url);downloadURLs.current.delete(url);},1000);
  });
  const validContainer=/^(?!.*--)[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/.test(name);
  const formError=!validAccount?'This host has not configured a valid local storage account.':error;
  // Only our finite static messages are localized; never resource names or API values.
  const presentation=text=>{switch(text){
    case "Access denied. This Azure exercise subject does not have permission for this operation.":return t("Access denied. This Azure exercise subject does not have permission for this operation.","アクセスが拒否されました。この Azure 演習の主体には操作の権限がありません。");
    case "The resource was not found. Refresh the list; it may have been removed.":return t("The resource was not found. Refresh the list; it may have been removed.","リソースが見つかりません。一覧を更新してください。削除された可能性があります。");
    case "The container already exists. Choose another name or refresh.":return t("The container already exists. Choose another name or refresh.","コンテナーは既に存在します。別の名前を選ぶか更新してください。");
    case "The blob changed after you selected it. Refresh before retrying.":return t("The blob changed after you selected it. Refresh before retrying.","選択後に BLOB が変更されました。更新してから再試行してください。");
    case "The request did not complete. Refresh and check your host session, permissions and the documented local API limits.":return t("The request did not complete. Refresh and check your host session, permissions and the documented local API limits.","要求を完了できませんでした。更新し、ホストセッション、権限、ローカル API の制限を確認してください。");
    case "This browser cannot safely address a standalone dot path segment. Use a native client for this exact blob name.":return t("This browser cannot safely address a standalone dot path segment. Use a native client for this exact blob name.","このブラウザーでは単独のドットパスを安全に指定できません。この名前にはネイティブクライアントを使用してください。");
    case "This host has not configured a valid local storage account.":return t("This host has not configured a valid local storage account.","このホストには有効なローカルストレージアカウントが設定されていません。");
    case "Container created.":return t("Container created.","コンテナーを作成しました。");
    case "Block blob uploaded.":return t("Block blob uploaded.","ブロック BLOB をアップロードしました。");
    case "Container and all its blobs deleted.":return t("Container and all its blobs deleted.","コンテナーと内部のすべての BLOB を削除しました。");
    case "Blob deleted.":return t("Blob deleted.","BLOB を削除しました。");
    default:return text;
  }};
  const propertyPane=modal==='properties'||modal==='container-properties';
  const closePane=()=>{
    if(propertyPane){
      const life=lifecycle.current;++life.epoch;life.controller?.abort();life.controller=null;life.busy=false;setBusy(false);setProperties(null);setError('');setModal('');
    }else if(!busy){setProperties(null);setModal('');}
  };
  const title=modal==='create'?t('New container','新しいコンテナー'):modal==='upload'?t('Upload blob','BLOB のアップロード'):modal==='container-properties'?t('Container properties','コンテナーのプロパティ'):modal==='properties'?t('Blob properties','BLOB のプロパティ'):modal==='delete-container'?t('Delete container','コンテナーの削除'):t('Delete blob','BLOB の削除');
  return <section className="azure-blob-view" aria-label="Azure Blob Storage">
    <nav className="azure-breadcrumbs" aria-label={t('Storage navigation','ストレージのナビゲーション')}><a href="#account" onClick={e=>{e.preventDefault();if(!busy)load('');}}>{account}</a><span aria-hidden="true">›</span><a href="#containers" onClick={e=>{e.preventDefault();if(!busy)load('');}}>{t('Containers','コンテナー')}</a>{container&&<><span aria-hidden="true">›</span><span>{container}</span></>}</nav>
    <header className="azure-resource-heading"><div className="azure-resource-symbol" aria-hidden="true">▤</div><div><h1>{container||account}{!container&&<span className="azure-resource-section"> | {t('Containers','コンテナー')}</span>}</h1><p>{container?t('Container','コンテナー'):t('Storage account','ストレージ アカウント')}</p></div></header>
    <div className="azure-resource-layout">
      <aside className="azure-resource-menu" aria-label={t('Storage account menu','ストレージ アカウント メニュー')}>
        <label><span className="azure-visually-hidden">{t('Search resource menu','リソース メニューを検索')}</span><input type="search" placeholder={t('Search','検索')} onChange={e=>setMenuFilter(e.target.value)} value={menuFilter}/></label>
        {(!menuFilter||'containers'.includes(menuFilter.toLowerCase())||'コンテナー'.includes(menuFilter))&&<><h2>{t('Data storage','データ ストレージ')}</h2><button type="button" aria-current="page" disabled={busy} onClick={()=>load('')}>▤ {t('Containers','コンテナー')}</button></>}
        <p>{t('Other storage services and account management are not implemented.','ほかのストレージ サービスとアカウント管理は未対応です。')}</p>
      </aside>
      <div className="azure-work-pane">
        <p className="azure-local-banner">{t('Independent local simulator. This account is host-configured; no Azure resources are created.','独立したローカルシミュレーターです。アカウントはホスト設定で、Azure リソースは作成しません。')}</p>
        {formError&&<div className="azure-message azure-error" role="alert"><strong>{t('Operation failed','操作に失敗しました')}</strong><p>{presentation(formError)}</p></div>}
        {notice&&<div className="azure-message" role="status">{presentation(notice)}</div>}
        <div className="azure-command-bar" aria-label={t('Storage actions','ストレージ操作')}>
          <button type="button" disabled={busy||!validAccount} onClick={()=>open(container?'upload':'create')}>{container?t('Upload','アップロード'):t('+ Container','+ コンテナー')}</button>
          <button type="button" disabled={busy||!validAccount} onClick={()=>load()}>↻ {t('Refresh','最新の情報に更新')}</button>
          {container&&<><button type="button" disabled={busy||!selected} onClick={download}>↓ {t('Download','ダウンロード')}</button><button type="button" disabled={busy||!selected} onClick={()=>showProperties(selected)}>{t('Properties','プロパティ')}</button></>}
          {!container&&<details className="azure-container-more" onKeyDown={event=>{if(event.key==='Escape'){event.preventDefault();event.currentTarget.open=false;event.currentTarget.querySelector('summary').focus();}}}>
            <summary aria-disabled={busy||!selected} tabIndex={busy||!selected?-1:0} onClick={event=>{if(busy||!selected)event.preventDefault();}}>{t('More','その他')} <span aria-hidden="true">⋯</span></summary>
            <div><button type="button" disabled={busy||!selected} onClick={event=>{const menu=event.currentTarget.closest('details');menu.open=false;menu.querySelector('summary').focus();showProperties(selected,'container');}}>{t('Container properties','コンテナーのプロパティ')}</button></div>
          </details>}
          <button type="button" disabled={busy||!selected} onClick={()=>open(container?'delete-blob':'delete-container')}>{t('Delete','削除')}</button>
        </div>
        <label className="azure-page-filter"><span className="azure-visually-hidden">{t('Filter this page by name','このページを名前で絞り込み')}</span><input type="search" placeholder={t('Filter this page by name','このページを名前で絞り込み')} value={filter} onChange={e=>setFilter(e.target.value)}/></label>
        <p className="azure-result-count" role="status">{busy?t('Loading storage resources','ストレージ リソースを読み込み中'):t(`${rows.length} on this page`, `このページに ${rows.length} 件`)}</p>
        <div className="azure-table-scroll"><table aria-label={container?t('Blobs','BLOB'):t('Containers','コンテナー')} aria-busy={busy}>
          <thead><tr><th scope="col"><span className="azure-visually-hidden">{t('Select','選択')}</span></th><th scope="col">{t('Name','名前')}</th><th scope="col">{t('Last modified','最終更新日時')}</th>{container?<><th scope="col">{t('Blob type','BLOB の種類')}</th><th scope="col">{t('Size (bytes)','サイズ (バイト)')}</th></>:<th scope="col">{t('Anonymous access level','匿名アクセス レベル')}</th>}</tr></thead>
          <tbody>{rows.filter(row=>row.name.toLowerCase().includes(filter.toLowerCase())).map(row=><tr key={row.name} className={selected?.name===row.name?'azure-selected':''}><td><input type="checkbox" aria-label={t('Select ','選択: ')+row.name} checked={selected?.name===row.name} disabled={busy} onChange={e=>setSelection(e.target.checked?[row]:[])}/></td><td>{container?<button className="azure-row-link" type="button" disabled={busy} onClick={()=>showProperties(row)}>{row.name}</button>:<a className="azure-row-link" href="#container" onClick={e=>{e.preventDefault();if(!busy)load(row.name);}}>{row.name}</a>}</td><td>{row.modified}</td>{container?<><td>{row.type}</td><td>{row.size}</td></>:<td>{t('Private (local subject required)','非公開 (ローカルの主体が必要)')}</td>}</tr>)}</tbody>
        </table></div>
        {!busy&&!rows.length&&<p className="azure-empty">{container?t('No blobs on this page. Upload a block blob to get started.','このページに BLOB はありません。ブロック BLOB をアップロードしてください。'):t('No containers on this page. Create a private container to get started.','このページにコンテナーはありません。非公開のコンテナーを作成してください。')}</p>}
        <div className="azure-pagination"><button type="button" disabled={busy||!validAccount} onClick={()=>load()}>{t('First page','最初のページ')}</button><button type="button" disabled={busy||!next} onClick={()=>load(container,next)}>{t('Next page','次のページ')}</button></div>
        <details className="azure-limits"><summary>{t('Local support and limits','ローカルでの対応範囲と制限')}</summary><p>{t('Block blobs up to 8 MiB. No snapshots, versions, soft delete, tiers, SAS, Entra sign-in or ARM account creation. Container deletion removes all its blobs. Dot-segment names require a native client. Filtering applies only to the current page.','ブロック BLOB は最大 8 MiB です。スナップショット、バージョン、論理削除、階層、SAS、Entra 認証、ARM アカウント作成は未対応です。コンテナー削除で内部の BLOB も削除します。ドットセグメント名にはネイティブクライアントが必要です。絞り込みは現在のページのみ対象です。')}</p></details>
      </div>
    </div>
    <dialog ref={pane} className="azure-action-pane" aria-labelledby="azure-pane-title" onCancel={e=>{e.preventDefault();closePane();}}>
      <header><h2 id="azure-pane-title">{title}</h2><div className="azure-pane-header-controls"><label className="azure-pane-language">Language / 言語<select value={locale} onChange={e=>setLocale(e.target.value)}><option value="en">English</option><option value="ja">日本語</option></select></label><button type="button" disabled={busy&&!propertyPane} aria-label={t('Close storage action','ストレージ操作を閉じる')} onClick={closePane}>×</button></div></header>
      {propertyPane?<><div className="azure-pane-body" aria-busy={busy}>
        {busy&&<p role="status">{modal==='container-properties'?t('Loading container properties…','コンテナーのプロパティを読み込み中…'):t('Loading blob properties…','BLOB のプロパティを読み込み中…')}</p>}
        {error&&<p className="azure-message azure-error" role="alert">{presentation(error)}</p>}
        {properties&&properties.kind===(modal==='container-properties'?'container':'blob')&&<><dl><dt>{t('Name','名前')}</dt><dd>{properties.name}</dd><dt>{t('Last modified','最終更新日時')}</dt><dd>{properties.modified}</dd><dt>ETag</dt><dd>{properties.etag}</dd>{properties.kind==='blob'&&<><dt>{t('Blob type','BLOB の種類')}</dt><dd>{properties.type}</dd><dt>{t('Size (bytes)','サイズ (バイト)')}</dt><dd>{properties.size}</dd><dt>{t('Content type','コンテンツの種類')}</dt><dd>{properties.contentType}</dd></>}</dl>
        <h3>{t('Metadata','メタデータ')}</h3>{properties.metadata.length?<dl>{properties.metadata.map(([key,value])=><React.Fragment key={key}><dt>{key}</dt><dd>{value}</dd></React.Fragment>)}</dl>:<p>{t('No metadata.','メタデータはありません。')}</p>}
        <p>{t('Properties were fetched from the native HEAD response when this pane opened. Content is not previewed.','このペインを開いたときのネイティブ HEAD 応答を表示しています。内容のプレビューは行いません。')}</p></>}
      </div><footer><button type="button" onClick={closePane}>{t('Close','閉じる')}</button></footer></>:<form onSubmit={e=>{e.preventDefault();if(!busy){if(modal==='create')create();else if(modal==='upload')upload();else remove();}}}>
        <div className="azure-pane-body">{error&&<p className="azure-message azure-error" role="alert">{presentation(error)}</p>}
          {modal==='create'?<><label>{t('Name','名前')} <span aria-hidden="true">*</span><input ref={firstField} autoFocus required value={name} onChange={e=>setName(e.target.value)} disabled={busy}/></label><p>{t('3–63 lowercase letters, numbers or hyphens; no consecutive hyphens.','小文字、数字、ハイフンの 3～63 文字。連続したハイフンは使用できません。')}</p><label>{t('Anonymous access level','匿名アクセス レベル')}<input readOnly value={t('Private (no anonymous access)','非公開 (匿名アクセスなし)')}/></label><p className="azure-message">{t('Only local authenticated subjects can access data. Changing access policy is unsupported.','ローカルで認証された主体のみアクセスできます。アクセスポリシーの変更は未対応です。')}</p></>:modal==='upload'?<><label>{t('Files','ファイル')}<input ref={firstField} type="file" disabled={busy} onChange={e=>{const f=e.target.files?.[0];setFiles(f?[f]:[]);setName(f?.name||'');}}/></label><p>{t('One block blob, maximum 8 MiB.','ブロック BLOB 1 件、最大 8 MiB。')}</p><label>{t('Blob name','BLOB 名')}<input required value={name} onChange={e=>setName(e.target.value)} disabled={busy}/></label><p>{t('Use / for virtual paths. Standalone . and .. segments cannot be addressed by this browser.','仮想パスには / を使用します。単独の . と .. はこのブラウザーから指定できません。')}</p><label>{t('Replace existing blob','既存 BLOB の置換')}<input value={overwrite} onChange={e=>setOverwrite(e.target.value)} disabled={busy}/></label><p>{t('Create only by default. Type replace to replace an existing blob and its metadata. The target must already exist.','既定は新規作成のみです。既存 BLOB とメタデータを置き換えるには replace と入力します。対象が存在する必要があります。')}</p></>:<><p className="azure-message">{modal==='delete-container'?t('Permanently delete this container and ALL its blobs, including other pages. Recovery is unsupported.','このコンテナーと別ページを含むすべての BLOB を完全に削除します。復元は未対応です。'):t('Permanently delete this blob. Soft delete is unsupported.','この BLOB を完全に削除します。論理削除は未対応です。')}</p><label>{t('Type ','削除の確認: ')+(selected?.name||'')+t(' to confirm deletion',' と入力')}<input ref={firstField} autoFocus value={confirm} onChange={e=>setConfirm(e.target.value)} disabled={busy}/></label></>}
        </div><footer><button type="submit" className="azure-primary" disabled={busy||(modal==='create'?!validContainer:modal==='upload'?(!files[0]||files[0].size>MAX_BLOB||!name||(overwrite&&overwrite!=='replace')):confirm!==selected?.name)}>{busy?t('Working…','処理中…'):modal==='create'?t('Create','作成'):modal==='upload'?t('Upload','アップロード'):t('Delete','削除')}</button><button type="button" disabled={busy} onClick={closePane}>{t('Cancel','キャンセル')}</button></footer>
      </form>}
    </dialog>
  </section>;
}
