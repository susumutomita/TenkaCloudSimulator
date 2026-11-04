import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@cloudscape-design/global-styles/index.css';
import './console.css';
import AppLayout from '@cloudscape-design/components/app-layout';
import TopNavigation from '@cloudscape-design/components/top-navigation';
import SideNavigation from '@cloudscape-design/components/side-navigation';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Table from '@cloudscape-design/components/table';
import Header from '@cloudscape-design/components/header';
import Button from '@cloudscape-design/components/button';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Container from '@cloudscape-design/components/container';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import TextFilter from '@cloudscape-design/components/text-filter';
import Pagination from '@cloudscape-design/components/pagination';
import Modal from '@cloudscape-design/components/modal';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Textarea from '@cloudscape-design/components/textarea';
import Select from '@cloudscape-design/components/select';
import FileUpload from '@cloudscape-design/components/file-upload';
import Link from '@cloudscape-design/components/link';
import HelpPanel from '@cloudscape-design/components/help-panel';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Tabs from '@cloudscape-design/components/tabs';
import RadioGroup from '@cloudscape-design/components/radio-group';
import LambdaView from './lambda.jsx';
import SQSView from './sqs.jsx';
import IAMView from './iam.jsx';
import APIGatewayView from './apigateway.jsx';
import S3Settings from './s3-settings.jsx';
import Toggle from '@cloudscape-design/components/toggle';
import GCPStorageView from './gcp-storage.jsx';
import AzureBlobView from './azure-blob.jsx';

const identifier = text => <span className="resource-identifier">{text}</span>;
const regions = ['us-east-1','ap-northeast-1','us-west-2','eu-west-1'];
const readXML = (text) => new DOMParser().parseFromString(text,'application/xml');
const value = (node,name) => node.getElementsByTagName(name)[0]?.textContent || '';
const nodes = (node,name) => [...node.getElementsByTagName(name)];
const keyPath = (bucket,key) => {
  if(key==='.' || key==='..') throw Error('A standalone dot key cannot be addressed safely in this browser. Use an S3 SDK for this key.');
  return '/'+bucket+'/'+encodeURIComponent(key);
};
function Login(){
  const [token,setToken]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  return <ContentLayout header={<Header variant="h1">Open your local sandbox</Header>}><Container><form onSubmit={async e=>{e.preventDefault();setBusy(true);try{const r=await fetch('/_simulator/login',{method:'POST',body:new URLSearchParams({token})});if(!r.ok)throw Error('Invalid or expired access token');location.assign('/_simulator/console/');}catch(e){setError(e.message)}finally{setBusy(false)}}}><Form errorText={error} actions={<Button variant="primary" loading={busy} formAction="submit">Open sandbox</Button>}><SpaceBetween size="l"><Alert type="info">Use the access token supplied by your host. This is an independent local simulator.</Alert><FormField label="Access token"><Input value={token} type="password" onChange={({detail})=>setToken(detail.value)} autoComplete="off"/></FormField></SpaceBetween></Form></form></Container></ContentLayout>;
}
function Console(){
  const routeService=()=> /^#\/(sqs)/.test(location.hash)?'sqs':/^#\/gcp-storage/.test(location.hash)?'gcp-storage':/^#\/azure-blob/.test(location.hash)?'azure-blob':/^#\/(apigateway)/.test(location.hash)?'apigateway':/^#\/(parameters|ssm)/.test(location.hash)?'ssm':/^#\/(stacks|cfn)/.test(location.hash)?'cfn':/^#\/(lambda|functions)/.test(location.hash)?'lambda':/^#\/(iam|roles)/.test(location.hash)?'iam':'s3';
  const initial=routeService();
  const [cloudStorage,setCloudStorage]=useState({});
  const [service,setService]=useState(initial),[bucket,setBucket]=useState(null),[items,setItems]=useState([]),[selected,setSelected]=useState([]),[filter,setFilter]=useState(''),[page,setPage]=useState(1),[sort,setSort]=useState({sortingField:'name'}),[descending,setDescending]=useState(false);
  const [region,setRegion]=useState('us-east-1'),[principal,setPrincipal]=useState('simulator-admin'),[persistent,setPersistent]=useState(false),[busy,setBusy]=useState(true),[ready,setReady]=useState(false),[identitySelection,setIdentitySelection]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState(''),[modal,setModal]=useState(null),[form,setForm]=useState({}),[modalError,setModalError]=useState(''),[saving,setSaving]=useState(false),[navOpen,setNavOpen]=useState(innerWidth>900),[toolsOpen,setToolsOpen]=useState(false),[detail,setDetail]=useState(null);
  const generation=useRef(0), file=useRef(null), opener=useRef(null);
  const [navigationVersion,setNavigationVersion]=useState(0);
  const [authenticationRequired,setAuthenticationRequired]=useState(false),[accessError,setAccessError]=useState('');
  const [showVersions,setShowVersions]=useState(false),[bucketTab,setBucketTab]=useState("objects");
  const change=(key,v)=>setForm(old=>({...old,[key]:v}));
  const close=()=>{setModal(null);requestAnimationFrame(()=>{const previous=opener.current;if(!previous)return;if(previous.node.isConnected){previous.node.focus();return}const match=[...document.querySelectorAll('button')].find(b=>(b.getAttribute('aria-label')||b.textContent.trim())===previous.label&&!b.disabled&&b.getClientRects().length);match?.focus()})};
  const open=(kind,values={})=>{if(saving)return;opener.current={node:document.activeElement,label:document.activeElement?.getAttribute('aria-label')||document.activeElement?.textContent.trim()};setModalError('');setForm(values);file.current=null;setModal(kind)};
  async function request(path,options={},identity=principal){
    const r=await fetch(path,{...options,headers:{'X-Simulator-Principal':identity,...options.headers}});
    if(r.status===401){location.assign('/_simulator/login');throw Error('Sign in to continue.');}
    if(!r.ok){const text=await r.text();let message=value(readXML(text),'Message');try{const j=JSON.parse(text);message=j.message||(typeof j.error==='object'?j.error?.message:j.error)||message}catch{};throw Error((value(readXML(text),'Code')||r.status)+': '+(message||text));}
    return r;
  }
  async function signOut(){
    if(saving||!authenticationRequired)return;
    setSaving(true);setAccessError('');
    try{
      const response=await fetch('/_simulator/logout',{method:'POST',credentials:'same-origin'});
      if(response.status!==204)throw Error('The server did not confirm sign out ('+response.status+').');
      location.replace('/_simulator/login');
    }catch{setAccessError('Sign out was not confirmed. Your session may still be active. Try Sign out again.');setSaving(false)}
  }
  async function invokeRequest(path,options={}){
    const response=await fetch(path,{...options,headers:{'X-Simulator-Principal':principal,...options.headers}});
    // A guest application may legitimately return 401. Check host authentication
    // on its own protected endpoint before treating that status as an expired session.
    if(response.status===401)await request('/_simulator/v1/capabilities');
    return response;
  }
  const ssm=async(op,input)=> (await request('/',{method:'POST',headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'AmazonSSM.'+op,'X-Simulator-Region':region},body:JSON.stringify(input)})).json();
  const cfn=async(op,input={})=>readXML(await (await request('/',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Region':region},body:new URLSearchParams({Action:op,Version:'2010-05-15',...input})})).text());
  async function load(){
    const revision=++generation.current;setBusy(true);setError('');setSelected([]);
    try{
      let list=[];
      if(service==='s3'){
        if(bucket){let token='',versionToken='';do{const query=showVersions?'versions&max-keys=1000'+(token?'&key-marker='+encodeURIComponent(token)+'&version-id-marker='+encodeURIComponent(versionToken):''):'list-type=2&max-keys=1000'+(token?'&continuation-token='+encodeURIComponent(token):'');const xml=readXML(await(await request('/'+bucket+'?'+query)).text());const entries=showVersions?[...xml.documentElement.children].filter(n=>n.localName==='Version'||n.localName==='DeleteMarker'):nodes(xml,'Contents');list.push(...entries.map(n=>({name:value(n,'Key'),size:Number(value(n,'Size')),modified:value(n,'LastModified'),etag:value(n,'ETag'),...(showVersions?{versionId:value(n,'VersionId'),marker:n.localName==='DeleteMarker',latest:value(n,'IsLatest')==='true',id:value(n,'Key')+'\u0000'+value(n,'VersionId')}:{} )})));token=value(xml,showVersions?'NextKeyMarker':'NextContinuationToken');versionToken=value(xml,'NextVersionIdMarker');}while(token&&revision===generation.current)}
        else{const xml=readXML(await(await request('/')).text());list=nodes(xml,'Bucket').map(n=>({name:value(n,'Name'),region:value(n,'BucketRegion'),created:value(n,'CreationDate')})).filter(b=>b.region===region);}
      }else if(service==='ssm'){
        let token;do{const result=await ssm('DescribeParameters',token?{NextToken:token}:{});list.push(...result.Parameters.map(p=>({...p,name:p.Name,type:p.Type,version:p.Version,modified:new Date(p.LastModifiedDate*1000).toISOString()})));token=result.NextToken;}while(token&&revision===generation.current)
      }else if(service==='cfn'){const xml=await cfn('DescribeStacks');list=nodes(xml,'StackName').map(n=>({name:n.textContent,status:value(n.parentNode,'StackStatus'),created:value(n.parentNode,'CreationTime'),id:value(n.parentNode,'StackId')}));}
      if(revision===generation.current){setItems(list);setPage(1)}
    }catch(e){if(revision===generation.current){setItems([]);setError(e.message)}}finally{if(revision===generation.current)setBusy(false)}
  }
  useEffect(()=>{fetch('/_simulator/v1/capabilities').then(async r=>{if(r.status===401){location.assign('/_simulator/login');return null}setAuthenticationRequired(r.headers.get('X-Simulator-Authentication')==='required');return r.json()}).then(c=>{if(!c)return;setPrincipal(c.principal||'simulator-admin');setIdentitySelection(c.identitySelection===true);setPersistent(c.persistence==='file');setCloudStorage(c.cloudStorage||{});setReady(true)}).catch(e=>{setError(e.message);setBusy(false)})},[]);
  useEffect(()=>{if(ready){setDetail(null);setMessage('');load()}},[ready,service,bucket,region,principal,navigationVersion,showVersions]);
  function dismissResourceForm(){setModal(null);setModalError('');setForm({});file.current=null;}
  useEffect(()=>{const change=()=>{generation.current++;setNavigationVersion(v=>v+1);setService(routeService());setBucket(null);setShowVersions(false);setBucketTab('objects');setDetail(null);setFilter('');setSelected([]);dismissResourceForm();};addEventListener('hashchange',change);return()=>removeEventListener('hashchange',change);},[]);
  function navigate(next){if(saving)return;generation.current++;setNavigationVersion(v=>v+1);setService(next);setBucket(null);setShowVersions(false);setBucketTab('objects');setDetail(null);setFilter('');setSelected([]);dismissResourceForm();location.hash=next==='gcp-storage'?'/gcp-storage':next==='azure-blob'?'/azure-blob':next==='ssm'?'/parameters':next==='cfn'?'/stacks':next==='lambda'?'/lambda':next==='iam'?'/iam':next==='apigateway'?'/apigateway':next==='sqs'?'/sqs':'/s3';}
  function changeRegion(next){if(saving||next===region)return;generation.current++;setRegion(next);setBucket(null);setShowVersions(false);setBucketTab('objects');setDetail(null);setSelected([]);setFilter('');setModal(null);setModalError('');setError('');setMessage('');}
  async function inspect(item){
    if(service==='s3'&&!bucket){setBucket(item.name);setShowVersions(false);setBucketTab('objects');setFilter('');return}
    if(service==='s3'){if(item.marker){open('delete',item);return}open('object',item);return}
    const revision=generation.current;
    try{if(service==='ssm'){const p=(await ssm('GetParameter',{Name:item.name})).Parameter;if(revision===generation.current)setDetail({...p,...(item.Description!==undefined?{Description:item.Description}:{})})}else{const xml=await cfn('DescribeStacks',{StackName:item.name});if(revision===generation.current)setDetail(nodes(xml,'OutputKey').map(n=>({label:n.textContent,value:value(n.parentNode,'OutputValue')})))} }catch(e){setError(e.message)}
  }
  async function submit(){
    setSaving(true);setModalError('');
    try{
      if(modal==='create-bucket'){
        if(!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(form.name||''))throw Error('Use a DNS-compatible bucket name of 3–63 characters.');
        const body=region==='us-east-1'?'':`<CreateBucketConfiguration><LocationConstraint>${region}</LocationConstraint></CreateBucketConfiguration>`;await request('/'+form.name,{method:'PUT',body});setMessage('Bucket “'+form.name+'” created.');
      }else if(modal==='upload'){
        if(!file.current)throw Error('Choose a file.');if(file.current.size>8388608)throw Error('This file exceeds the 8 MiB simulator limit.');if(!form.name)throw Error('Enter an object key.');if(new TextEncoder().encode(form.name).length>1024)throw Error('Object keys must be at most 1024 bytes.');await request(keyPath(bucket,form.name),{method:'PUT',body:file.current,headers:{'Content-Type':file.current.type||'application/octet-stream'}});setMessage('Object uploaded.');
      }else if(modal==='delete'){
        if(form.confirmation!==form.name)throw Error('Enter the exact name to confirm deletion.');if(service==='s3')await request(bucket?keyPath(bucket,form.name)+(form.versionId?'?versionId='+encodeURIComponent(form.versionId):''):'/'+form.name,{method:'DELETE'});else if(service==='ssm')await ssm('DeleteParameter',{Name:form.name});else await cfn('DeleteStack',{StackName:form.name});setDetail(null);setMessage('Resource deleted.');
      }else if(modal==='object'){
        const r=await request(keyPath(bucket,form.name)+(form.versionId?'?versionId='+encodeURIComponent(form.versionId):''));const url=URL.createObjectURL(await r.blob());const a=document.createElement('a');a.href=url;a.download=form.name.split('/').pop()||'object';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);setModal(null);return;
      }else if(modal==='identity'){if(!/^[a-zA-Z0-9_-]{1,64}$/.test(form.principal||''))throw Error('Use a 1–64 character principal name.');setPrincipal(form.principal);setModal(null);return;
      }else if(modal==='policy'){
        let document;try{document=JSON.parse(form.policy)}catch{throw Error('Enter valid JSON.');}await request('/_simulator/v1/iam/principals/'+encodeURIComponent(form.principal)+'/policy',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(document)},'simulator-admin');setMessage('Identity policy saved.');
      }else if(modal==='parameter'){
        await ssm('PutParameter',{Name:form.name,Type:form.type||'String',Value:form.value||'',Overwrite:!!form.overwrite,...(Object.hasOwn(form,'description')?{Description:form.description}:{})});setDetail(null);setMessage('Parameter saved.');
      }
      setModal(null);await load();
    }catch(e){setModalError(e.message)}finally{setSaving(false)}
  }
  const storageScope=service==='gcp-storage'?{kind:'Project',value:cloudStorage.gcp?.project||'Project'}:service==='azure-blob'?{kind:'Storage account',value:cloudStorage.azure?.account||'Storage account'}:null;
  const scopeUtility=storageScope?{
    type:'menu-dropdown',text:storageScope.value,title:storageScope.kind,ariaLabel:storageScope.kind+' menu',
    description:'Configured local '+storageScope.kind.toLowerCase()+' (read only)',
    items:[{id:'configured-scope',text:'Configured '+storageScope.kind.toLowerCase()+': '+storageScope.value+' (read only)',disabled:true}],
    // Guard independently of presentation: the compact utility menu can emit disabled-item clicks.
    onItemClick:()=>{},
  }:{type:'menu-dropdown',text:service==='iam'?'Global':region,title:'Region',items:regions.map(r=>({id:r,text:r,disabled:saving||service==='iam'})),onItemClick:({detail})=>changeRegion(detail.id)};
  const label=service==='gcp-storage'?'Google Cloud Storage':service==='azure-blob'?'Azure Blob Storage':service==='s3'?'Amazon S3':service==='ssm'?'Systems Manager':service==='lambda'?'Lambda':service==='iam'?'IAM':service==='apigateway'?'API Gateway':service==='sqs'?'Amazon SQS':'CloudFormation';
  const title=service==='s3'?(bucket?'Objects':'General purpose buckets'):service==='ssm'?'Parameters':'Stacks';
  const primary=()=>open(service==='s3'?(bucket?'upload':'create-bucket'):'parameter',{type:'String'});
  const primaryText=service==='s3'?(bucket?'Upload':'Create bucket'):'Create parameter';
  const filtered=items.filter(i=>Object.values(i).some(v=>String(v).toLowerCase().includes(filter.toLowerCase()))).sort((a,b)=>{const key=sort.sortingField||'name';const av=a[key]??'',bv=b[key]??'';return (typeof av==='number'?av-bv:String(av).localeCompare(String(bv)))*(descending?-1:1)});
  const columns=[{id:'name',header:'Name',sortingField:'name',cell:item=><Link onFollow={e=>{e.preventDefault();inspect(item)}} href="#">{item.name}</Link>},...(service==='s3'?(bucket?[...(showVersions?[{id:'version',header:'Version ID',cell:i=>identifier(i.versionId)},{id:'type',header:'Type',cell:i=>i.marker?'Delete marker':'Object'},{id:'latest',header:'Latest version',cell:i=>i.latest?'Yes':'No'}]:[]),{id:'size',header:'Size',sortingField:'size',cell:i=>i.size+' B'},{id:'modified',header:'Last modified',cell:i=>new Date(i.modified).toLocaleString()},{id:'etag',header:'ETag',cell:i=>i.etag}]:[{id:'region',header:'AWS Region',cell:i=>i.region},{id:'created',header:'Creation date',cell:i=>new Date(i.created).toLocaleString()}]):service==='ssm'?[{id:'type',header:'Type',cell:i=>i.type},{id:'version',header:'Version',sortingField:'version',cell:i=>i.version},{id:'modified',header:'Last modified',cell:i=>new Date(i.modified).toLocaleString()}]:[{id:'status',header:'Status',cell:i=><StatusIndicator type="success">{i.status}</StatusIndicator>},{id:'created',header:'Creation date',cell:i=>new Date(i.created).toLocaleString()}])];
  const defaultPolicy=JSON.stringify({Version:'2012-10-17',Statement:[{Effect:'Allow',Action:['s3:ListAllMyBuckets','s3:ListBucket','s3:GetObject'],Resource:'*'}]},null,2);
  const modalTitle=modal==='create-bucket'?'Create bucket':modal==='upload'?'Upload':modal==='object'?'Object details':modal==='identity'?'Choose identity':modal==='policy'?'Manage identity policy':modal==='parameter'?(form.overwrite?'Edit parameter':'Create parameter'):(bucket?(form.versionId?'Delete object version':'Delete object'):service==='ssm'?'Delete parameter':service==='cfn'?'Delete stack':'Delete bucket');
  const pageForm=['create-bucket','upload','parameter'].includes(modal);
  const actionText=modal==='parameter'?(form.overwrite?'Save changes':'Create parameter'):primaryText;
  const fields=<SpaceBetween size="l">
    {(modal==='create-bucket'||modal==='upload'||modal==='parameter')&&<FormField label={modal==='create-bucket'?'Bucket name':modal==='upload'?'Object key':'Name'} constraintText={modal==='upload'?'Maximum 1024 UTF-8 bytes. Slashes and dot segments are literal key data.':modal==='parameter'?'Use a name such as /team/hello. Only Standard text parameters are supported.':'3–63 lowercase letters, numbers, dots and hyphens.'}><Input autoFocus value={form.name||''} readOnly={!!form.overwrite} onChange={({detail})=>change('name',detail.value)}/></FormField>}
    {modal==='create-bucket'&&<FormField label="Region"><Select disabled={saving} selectedOption={{label:region,value:region}} options={regions.map(r=>({label:r,value:r}))} onChange={({detail})=>setRegion(detail.selectedOption.value)}/></FormField>}
    {modal==='upload'&&<FormField label="Choose a file"><FileUpload value={file.current?[file.current]:[]} onChange={({detail})=>{file.current=detail.value[0];setForm(f=>({...f,name:detail.value[0]?.name||f.name}))}} showFileSize i18nStrings={{uploadButtonText:()=> 'Add files',removeFileAriaLabel:i=>'Remove file '+i}} constraintText="Maximum 8 MiB. Uploads change the local SQLite state."/></FormField>}
    {modal==='parameter'&&<><FormField label="Description"><Input value={form.description||''} onChange={({detail})=>change('description',detail.value)}/></FormField><FormField label="Parameter tier"><RadioGroup value="Standard" items={[{value:'Standard',label:'Standard'},{value:'Advanced',label:'Advanced',disabled:true,description:'Not implemented in this simulator'}]}/></FormField><FormField label="Type"><Select disabled={!!form.overwrite} selectedOption={{label:form.type||'String',value:form.type||'String'}} options={[{label:'String',value:'String'},{label:'StringList',value:'StringList'},{label:'SecureString',value:'SecureString',disabled:true,description:'KMS is not implemented'}]} onChange={({detail})=>change('type',detail.selectedOption.value)}/></FormField><FormField label="Data type"><Select disabled selectedOption={{label:"text",value:"text"}} options={[{label:"text",value:"text"}]}/></FormField><FormField label="Value"><Textarea rows={5} value={form.value||''} onChange={({detail})=>change('value',detail.value)}/></FormField></>}
    {(modal==='identity'||modal==='policy')&&<FormField label="Principal name"><Input autoFocus value={form.principal||''} onChange={({detail})=>change('principal',detail.value)}/></FormField>}
    {modal==='policy'&&<FormField label="Identity policy JSON"><Textarea rows={12} value={form.policy||''} onChange={({detail})=>change('policy',detail.value)}/></FormField>}
    {modal==='object'&&<KeyValuePairs columns={1} items={[{label:'Key',value:identifier(form.name)},...(form.versionId?[{label:'Version ID',value:identifier(form.versionId)}]:[]),{label:'Size',value:form.size+' B'},{label:'ETag',value:form.etag}]}/>}
    {modal==='delete'&&<><Alert type="warning">Delete “{identifier(form.name)}”? {bucket?(form.versionId?'This permanently deletes the selected version and cannot be undone.':'A versioned bucket adds a delete marker; an unversioned object is permanently deleted.'):'Buckets must contain no objects, versions or delete markers.'}</Alert><FormField label={identifier('Type '+form.name+' to confirm')}><Input autoFocus value={form.confirmation||''} onChange={({detail})=>change('confirmation',detail.value)}/></FormField></>}
  </SpaceBetween>;
  return <>
    <div id="navigation"><TopNavigation visualContext="top-navigation" identity={{title:'TenkaCloud Simulator',href:'/_simulator/console/'}} utilities={[{type:'menu-dropdown',text:'Services',title:'Services',items:[{id:'s3',text:'S3',disabled:saving},{id:'ssm',text:'Systems Manager',disabled:saving},{id:'lambda',text:'Lambda',disabled:saving},{id:'iam',text:'IAM',disabled:saving},{id:'apigateway',text:'API Gateway',disabled:saving},{id:'sqs',text:'SQS',disabled:saving},{id:'cfn',text:'CloudFormation',disabled:saving},{id:'gcp-storage',text:'Google Cloud · Cloud Storage',disabled:saving||!cloudStorage.gcp},{id:'azure-blob',text:'Azure · Blob Storage',disabled:saving||!cloudStorage.azure}],onItemClick:({detail})=>navigate(detail.id)},scopeUtility,{type:'menu-dropdown',text:principal,title:'Account',ariaLabel:'Account menu',iconName:'user-profile',disableUtilityCollapse:true,description:authenticationRequired?'Local browser session':'Authentication disabled',items:[...(identitySelection?[{id:'identity',text:'Choose identity',disabled:saving}]:[]),{id:'signout',text:authenticationRequired?'Sign out':'Sign out (authentication disabled)',disabled:saving||!authenticationRequired}],onItemClick:({detail})=>{if(detail.id==='identity')open('identity',{principal});else if(detail.id==='signout')signOut()}},{type:'button',text:'API reference',href:'/_simulator/docs/'}]} i18nStrings={{overflowMenuTriggerText:'More',overflowMenuTitleText:'Utilities',overflowMenuBackIconAriaLabel:'Back',overflowMenuDismissIconAriaLabel:'Close'}}/></div>
    <AppLayout notifications={accessError?<Alert type="error" header="Could not sign out">{accessError}</Alert>:null} ariaLabels={{navigation:"Service navigation",navigationToggle:"Toggle service navigation",navigationClose:"Close service navigation",tools:"Help panel",toolsToggle:"Toggle help panel",toolsClose:"Close help panel"}} headerSelector="#navigation" contentType={pageForm?"form":"table"} navigationOpen={navOpen} onNavigationChange={({detail})=>setNavOpen(detail.open)} toolsOpen={toolsOpen} onToolsChange={({detail})=>setToolsOpen(detail.open)} tools={<HelpPanel header={<Header variant="h2">Simulator scope</Header>}><SpaceBetween size="m"><p>Independent local simulator. Not affiliated with AWS. No real cloud resources are created.</p><p>S3 basic operations, Standard SSM text parameters, Python 3.12 Lambda functions and limited CloudFormation stacks using S3 buckets, SSM parameters, inline Python functions and IAM identity policies.</p><p>Google Cloud Storage and Azure Blob Storage have bounded local owner-only scopes. Google IAM, Azure RBAC, federation and cloud account provisioning are not implemented.</p><Button onClick={()=>open('policy',{principal:'reader',policy:defaultPolicy})}>Manage identity policy</Button><Link href="/_simulator/licenses/CONSOLE-NOTICES.txt">Console licenses</Link></SpaceBetween></HelpPanel>}
      navigation={<SideNavigation header={{text:label,href:'#'}} activeHref={'#/'+service} onFollow={e=>{e.preventDefault();const v=e.detail.href;if(v==='#/policy'){open('policy',{principal:'reader',policy:defaultPolicy})}else navigate(v==='#'?service:v.slice(2))}} items={[...(service==='gcp-storage'?[{type:'link',text:'Buckets',href:'#/gcp-storage'}]:service==='azure-blob'?[{type:'link',text:'Containers',href:'#/azure-blob'}]:service==='s3'?[{type:'link',text:'General purpose buckets',href:'#/s3'}]:service==='ssm'?[{type:'link',text:'Parameter Store',href:'#/ssm'}]:service==='lambda'?[{type:'link',text:'Functions',href:'#/lambda'}]:service==='iam'?[{type:'link',text:'Roles',href:'#/iam'}]:service==='apigateway'?[{type:'link',text:'APIs',href:'#/apigateway'}]:service==='sqs'?[{type:'link',text:'Queues',href:'#/sqs'}]:[{type:'link',text:'Stacks',href:'#/cfn'}]),...(identitySelection?[{type:'divider'},{type:'section',text:'Simulator controls',items:[{type:'link',text:'Identity policies',href:'#/policy'}]}]:[])]}/>} breadcrumbs={<BreadcrumbGroup items={[{text:'Services',href:'#/s3'},{text:label,href:'#/'+service},...(bucket?[{text:bucket,href:'#/s3'}]:[]),...(service==='ssm'?[{text:'Parameter Store',href:'#/ssm'},...(detail?[{text:detail.Name,href:'#/ssm'}]:[])]:[])]} onFollow={e=>{e.preventDefault();navigate(e.detail.href.slice(2))}} ariaLabel="Breadcrumbs"/>}
      content={service==='sqs'?<SQSView key={region+'|'+principal+'|'+navigationVersion} region={region} request={request} onSaving={setSaving}/>:service==='gcp-storage'?<GCPStorageView key={'gcp:'+principal+':'+cloudStorage.gcp?.project} request={request} project={cloudStorage.gcp?.project}/>:service==='azure-blob'?<AzureBlobView key={'azure:'+principal+':'+cloudStorage.azure?.account} request={request} account={cloudStorage.azure?.account}/>:service==='apigateway'?<APIGatewayView key={region+principal+navigationVersion} region={region} request={request} invokeRequest={invokeRequest} onSaving={setSaving}/>:service==='iam'?<IAMView key={principal+navigationVersion} request={request} onSaving={setSaving} onLambda={()=>navigate('lambda')}/>:service==='lambda'?<LambdaView key={region+principal+navigationVersion} region={region} request={request} onSaving={setSaving} onIAM={()=>navigate('iam')}/>:pageForm?<ContentLayout header={<Header variant="h1">{modalTitle}</Header>}><form onSubmit={e=>{e.preventDefault();submit()}}><Form errorText={modalError} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={close}>Cancel</Button><Button variant="primary" formAction="submit" loading={saving}>{actionText}</Button></SpaceBetween>}><Container header={<Header variant="h2">{modal==='create-bucket'?'General configuration':modal==='upload'?'Files and folders':'Parameter details'}</Header>}><SpaceBetween size="l"><Alert type="info">Only the documented simulator subset is available. Unsupported AWS settings cannot be enabled.</Alert>{fields}</SpaceBetween></Container></Form></form></ContentLayout>:service==='ssm'&&detail?<ContentLayout header={<Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs"><Button onClick={()=>open('parameter',{name:detail.Name,type:detail.Type,value:detail.Value,...(detail.Description!==undefined?{description:detail.Description}:{}),overwrite:true})}>Edit parameter</Button><Button onClick={()=>open('delete',{name:detail.Name})}>Delete parameter</Button></SpaceBetween>}>{identifier(detail.Name)}</Header>}><SpaceBetween size="l"><Alert type="info">Independent local simulation. Standard text parameters only.</Alert><Tabs activeTabId="overview" tabs={[{id:'overview',label:'Overview'},{id:'history',label:'History',disabled:true},{id:'tags',label:'Tags',disabled:true}]}/><Container header={<Header variant="h2">Parameter details</Header>}><SpaceBetween size="l"><KeyValuePairs columns={2} minColumnWidth={280} items={[{label:'Name',value:identifier(detail.Name)},{label:'ARN',value:identifier(detail.ARN)},{label:'Type',value:detail.Type},{label:'Version',value:detail.Version},{label:'Tier',value:'Standard'},{label:'Data type',value:detail.DataType}]}/><FormField label="Value"><Textarea readOnly rows={4} value={detail.Value}/></FormField></SpaceBetween></Container></SpaceBetween></ContentLayout>:<SpaceBetween size="l">
        <Alert type="info" header="Local simulation">{persistent?'Your resources survive restart.':'Your resources reset when the process stops.'} This is an independent simulator, not the AWS console.</Alert>
        {error&&items.length>0&&<Alert type="error">{error}</Alert>}
        {message&&<Alert type="success" dismissible onDismiss={()=>setMessage('')}>{message}</Alert>}
        {service==='ssm'&&<Container header={<Header variant="h2">Open a parameter by name</Header>}><Form actions={<Button onClick={()=>inspect({name:form.lookup||''})}>Open parameter</Button>}><FormField label="Parameter name" description="Use a direct name when your identity cannot list parameters."><Input value={form.lookup||''} placeholder="/team/hello" onChange={({detail})=>change('lookup',detail.value)}/></FormField></Form></Container>}
        {bucket&&<><Header variant="h1">{identifier(bucket)}</Header><Tabs tabs={[{id:'objects',label:'Objects'},{id:'properties',label:'Properties'},{id:'permissions',label:'Permissions'},{id:'metrics',label:'Metrics',disabled:true},{id:'management',label:'Management',disabled:true}]} activeTabId={bucketTab} onChange={({detail})=>{if(saving)return;setBucketTab(detail.activeTabId);dismissResourceForm()}}/></>}
        {bucket&&bucketTab!=='objects'?<S3Settings key={bucket+principal+bucketTab} bucket={bucket} tab={bucketTab} request={request} onSaving={setSaving}/>:<>
        <Table variant="full-page" stickyHeader loading={busy} loadingText="Loading resources" trackBy={showVersions&&bucket?"id":"name"} selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>setSelected(detail.selectedItems)} ariaLabels={{selectionGroupLabel:'Resource selection',itemSelectionLabel:(_data,item)=>'Select '+item.name,tableLabel:title}} columnDefinitions={columns} items={filtered.slice((page-1)*10,page*10)} sortingColumn={sort} sortingDescending={descending} onSortingChange={({detail})=>{setSort(detail.sortingColumn);setDescending(detail.isDescending);setSelected([])}}
          header={<Header variant="awsui-h1-sticky" counter={'('+items.length+')'} description={bucket?'Objects are identified by their complete key. The console and SDK share the same state.':service==='ssm'?'Store and read text settings through the same SSM API as SDK clients.':service==='cfn'?'Only the documented S3 bucket, SSM parameter, inline Python function and IAM identity-policy stack subset is supported.':'Create buckets and organize your local objects.'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel="Refresh resources" onClick={load}/><Button disabled={!selected.length} onClick={()=>open('delete',{...selected[0]})}>Delete</Button>{service!=='cfn'&&<Button variant="primary" onClick={primary}>{primaryText}</Button>}</SpaceBetween>}>{title}</Header>}
          filter={<SpaceBetween direction="horizontal" size="s">{bucket&&<Toggle checked={showVersions} onChange={({detail})=>{generation.current++;setShowVersions(detail.checked);setSelected([]);setFilter('')}}>Show versions</Toggle>}<TextFilter filteringText={filter} filteringPlaceholder={bucket?'Find objects by key':'Find resources by name'} filteringAriaLabel="Find resources" onChange={({detail})=>{setFilter(detail.filteringText);setPage(1);setSelected([])}} countText={filtered.length+' matches'}/></SpaceBetween>}
          pagination={<Pagination currentPageIndex={page} pagesCount={Math.max(1,Math.ceil(filtered.length/10))} onChange={({detail})=>{setPage(detail.currentPageIndex);setSelected([])}} ariaLabels={{nextPageLabel:'Next page',previousPageLabel:'Previous page',pageLabel:n=>'Page '+n}}/>}
          empty={<Box textAlign="center" margin={{vertical:'l'}}><SpaceBetween size="m">{error?<Alert type="error" header="Could not load resources">{error}</Alert>:<><Box variant="h2">{filter?'No matching resources':bucket?'This bucket is empty':service==='ssm'?'No parameters yet':service==='cfn'?'No stacks yet':'No buckets yet'}</Box><Box>{filter?'Clear your filter to see resources.':'Create a resource or use an SDK client.'}</Box>{filter?<Button onClick={()=>setFilter('')}>Clear filter</Button>:service!=='cfn'&&<Button onClick={primary}>{primaryText}</Button>}</>}</SpaceBetween></Box>}/>
        </>}
        {service==='cfn'&&detail&&<Container header={<Header variant="h2">Stack outputs</Header>}><KeyValuePairs columns={1} items={detail.map(item=>({...item,value:identifier(item.value)}))}/></Container>}
        <Box color="text-body-secondary" fontSize="body-s">{principal} · {region} · {persistent?'Persistent environment':'Temporary environment'} · No external cloud connection</Box>
      </SpaceBetween>}
    />
    {!pageForm&&<Modal visible={!!modal} onDismiss={()=>!saving&&close()} header={modalTitle} closeAriaLabel="Close dialog" footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={close}>Cancel</Button><Button variant="primary" loading={saving} onClick={submit}>{modal==='delete'?(bucket&&!form.versionId?'Delete object':'Delete permanently'):modal==='object'?'Download object':modal==='identity'?'Choose identity':modal==='policy'?'Save policy':modal==='parameter'?(form.overwrite?'Save changes':'Create parameter'):primaryText}</Button></SpaceBetween></Box>}><Form errorText={modalError}>{fields}</Form></Modal>}
  </>;
}
createRoot(document.getElementById('root')).render(location.pathname==='/_simulator/login'?<Login/>:<Console/>);
