import React, {useEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FileUpload from '@cloudscape-design/components/file-upload';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import TextFilter from '@cloudscape-design/components/text-filter';
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
  const [container,setContainer]=useState(''),[rows,setRows]=useState([]),[next,setNext]=useState(''),[selection,setSelection]=useState([]),[filter,setFilter]=useState('');
  const [busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState(''),[modal,setModal]=useState(''),[name,setName]=useState(''),[confirm,setConfirm]=useState(''),[files,setFiles]=useState([]),[overwrite,setOverwrite]=useState('');
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
  const open=kind=>{setError('');setModal(kind);setName('');setConfirm('');setFiles([]);setOverwrite('');};
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
  return <div className="azure-blob-view"><ContentLayout header={<Header variant="h1">{container||'Containers'}</Header>}>
    <SpaceBetween size="l">
      <BreadcrumbGroup ariaLabel="Storage navigation" items={[{text:account,href:'#account'},{text:'Containers',href:'#containers'},...(container?[{text:container,href:'#current'}]:[])]} onFollow={e=>{e.preventDefault();if(!busy&&e.detail.href!=='#current')load('');}}/>
      <Alert type="info">Azure Blob Storage · Independent local simulator. Account {account} is host-configured. No Azure subscription or cloud resources are created.</Alert>
      {formError&&<Alert type="error" header="Operation failed">{formError}</Alert>}{notice&&<Alert type="success">{notice}</Alert>}
      <Table loading={busy} loadingText="Loading storage resources" selectionType="single" selectedItems={selection} onSelectionChange={({detail})=>setSelection(detail.selectedItems)} trackBy="name"
        ariaLabels={{tableLabel:container?'Blobs':'Containers',selectionGroupLabel:container?'Select a blob':'Select a container',itemSelectionLabel:(_,{name:n})=>'Select '+n}}
        items={rows.filter(row=>row.name.toLowerCase().includes(filter.toLowerCase()))}
        columnDefinitions={[{id:'name',header:'Name',cell:row=>container?<span className="azure-blob-name">{row.name}</span>:<Link href="#container" onFollow={e=>{e.preventDefault();if(!busy)load(row.name);}}>{row.name}</Link>},{id:'modified',header:'Last modified',cell:row=>row.modified},...(container?[{id:'type',header:'Blob type',cell:row=>row.type},{id:'size',header:'Size (bytes)',cell:row=>row.size}]:[{id:'access',header:'Anonymous access level',cell:()=> 'Private (local subject required)'}])]}
        header={<Header counter={'('+rows.length+' on this page)'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel="Refresh storage resources" disabled={busy||!validAccount} onClick={()=>load()}/>{container&&<Button disabled={busy||!selected} onClick={download}>Download</Button>}<Button disabled={busy||!selected} onClick={()=>open(container?'delete-blob':'delete-container')}>Delete</Button><Button variant="primary" disabled={busy||!validAccount} onClick={()=>open(container?'upload':'create')}>{container?'Upload':'+ Container'}</Button></SpaceBetween>}>{container?'Blobs':'Containers'}</Header>}
        filter={<TextFilter filteringText={filter} filteringAriaLabel="Filter this page by name" filteringPlaceholder="Filter this page by name" onChange={({detail})=>setFilter(detail.filteringText)}/>}
        empty={<Box textAlign="center" padding="l">{container?'No blobs on this page. Upload a block blob to get started.':'No containers on this page. Create a private container to get started.'}</Box>}/>
      <SpaceBetween direction="horizontal" size="xs"><Button disabled={busy||!validAccount} onClick={()=>load()}>First page</Button><Button disabled={busy||!next} onClick={()=>load(container,next)}>Next page</Button></SpaceBetween>
      <Container header={<Header variant="h2">Local support and limits</Header>}>Block blobs up to 8 MiB. No snapshots, versions, soft delete, tiers, SAS, Entra sign-in or ARM account creation. Container deletion immediately removes all contained blobs. Dot-segment blob names require a native client because browsers normalize their paths. Lists are live pages; this page filter does not search later pages.</Container>
    </SpaceBetween>
    <Modal visible={!!modal} closeAriaLabel="Close storage action" onDismiss={()=>{if(!busy)setModal('');}} header={modal==='create'?'New container':modal==='upload'?'Upload blob':modal==='delete-container'?'Delete container':'Delete blob'}>
      <form onSubmit={e=>{e.preventDefault();if(modal==='create')create();else if(modal==='upload')upload();else remove();}}><Form errorText={error} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={busy} onClick={()=>setModal('')} formAction="none">Cancel</Button><Button variant="primary" loading={busy} formAction="submit" disabled={modal==='create'?!validContainer:modal==='upload'?(!files[0]||files[0].size>MAX_BLOB||!name||(overwrite&&overwrite!=='replace')):confirm!==selected?.name}>{modal==='create'?'Create':modal==='upload'?'Upload':'Delete'}</Button></SpaceBetween>}>
        <SpaceBetween size="m">{modal==='create'?<><FormField label="Name" description="3–63 lowercase letters, numbers or hyphens; no consecutive hyphens."><Input autoFocus value={name} onChange={({detail})=>setName(detail.value)} disabled={busy}/></FormField><FormField label="Anonymous access level"><Box>Private (no anonymous access). Changing access policy is unsupported.</Box></FormField></>:modal==='upload'?<><FormField label="Files" description="One block blob, maximum 8 MiB."><FileUpload value={files} onChange={({detail})=>{setFiles(detail.value.slice(0,1));setName(detail.value[0]?.name||'');}} i18nStrings={{uploadButtonText:()=> 'Browse for files',dropzoneText:()=> 'Drop a file to upload',removeFileAriaLabel:()=> 'Remove file',limitShowFewer:'Show fewer files',limitShowMore:'Show more files',errorIconAriaLabel:'Error'}}/></FormField><FormField label="Blob name" description="Use / to include virtual path segments. Standalone . and .. segments cannot be addressed by this browser."><Input value={name} onChange={({detail})=>setName(detail.value)} disabled={busy}/></FormField><FormField label="Replace existing blob" description="Default is create only. Type replace to replace a currently existing blob, including its metadata. Replacement requires that the target exists."><Input value={overwrite} onChange={({detail})=>setOverwrite(detail.value)} disabled={busy}/></FormField></>:<><Alert type="warning">{modal==='delete-container'?'This permanently deletes this container and ALL its blobs, including blobs on other pages. Soft delete and recovery are unsupported.':'This permanently deletes the selected blob. Snapshots and soft delete are unsupported.'}</Alert><FormField label={'Type '+(selected?.name||'')+' to confirm deletion'}><Input autoFocus value={confirm} onChange={({detail})=>setConfirm(detail.value)} disabled={busy}/></FormField></>}</SpaceBetween>
      </Form></form>
    </Modal>
  </ContentLayout></div>;
}
