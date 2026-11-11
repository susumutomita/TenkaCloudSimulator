import SSMTerminal from './ssm-terminal.jsx';
import Login from './login.jsx';
import { LocaleProvider, useLocale } from './i18n.jsx';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@cloudscape-design/global-styles/index.css';
import './console.css';
import AppLayout from '@cloudscape-design/components/app-layout';
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
import IAMView from './iam.jsx';
import KMSView from './kms.jsx';
import SQSView from './sqs.jsx';
import DynamoDBView from './dynamodb.jsx';
import CloudWatchLogsView from './cloudwatch-logs.jsx';
import CloudFormationDetails, {CloudFormationLookup} from './cloudformation-events.jsx';
import SecretsManagerView from './secrets-manager.jsx';
import APIGatewayView from './apigateway.jsx';
import WAFv2View from './wafv2.jsx';
import S3Settings from './s3-settings.jsx';
import S3Multipart from './s3-multipart.jsx';
import Toggle from '@cloudscape-design/components/toggle';
import GCPStorageView from './gcp-storage.jsx';
import AzureBlobView from './azure-blob.jsx';
import GCPConsoleShell from './gcp-console-shell.jsx';
import AzureConsoleShell from './azure-console-shell.jsx';
import AzureARMView from './azure-arm.jsx';
import SakuraConsole from './sakura-console.jsx';
import SakuraDisks from './sakura-disks.jsx';
import GlobalNavigation from './global-navigation.jsx';
import ConsoleHome from './console-home.jsx';
import CatalogView from './catalog.jsx';
import GCPCatalogView from './gcp-catalog.jsx';
import {serviceFromHash, providerForService, serviceAvailable, hashForService, sakuraConfiguration} from './console-navigation.mjs';

const identifier = text => <span className="resource-identifier">{text}</span>;
const regions = ['us-east-1','ap-northeast-1','us-west-2','eu-west-1'];
const readXML = (text) => new DOMParser().parseFromString(text,'application/xml');
const value = (node,name) => node.getElementsByTagName(name)[0]?.textContent || '';
const nodes = (node,name) => [...node.getElementsByTagName(name)];
const keyPath = (bucket,key) => {
  if(key==='.' || key==='..') throw Error('A standalone dot key cannot be addressed safely in this browser. Use an S3 SDK for this key.');
  return '/'+bucket+'/'+encodeURIComponent(key);
};
function Console(){
  const {locale, t} = useLocale();
  // Only locally-created notices are translated; opaque server errors stay verbatim.
  const feedbackPairs = new Map([
    ['Object uploaded.','オブジェクトをアップロードしました。'],
    ['Resource deleted.','リソースを削除しました。'],
    ['Identity policy saved.','IDポリシーを保存しました。'],
    ['Parameter saved.','パラメータを保存しました。'],
    ['Sign out was not confirmed. Your session may still be active. Try Sign out again.','サインアウトを確認できませんでした。セッションが有効な可能性があります。もう一度お試しください。'],
  ]);
  function feedback(text) {
    if (feedbackPairs.has(text)) return t(text,feedbackPairs.get(text));
    return text;
  }
  function notice(message) {
    if (message?.kind==='bucket-created') return t('Bucket “'+message.name+'” created.','バケット「'+message.name+'」を作成しました。');
    return feedback(message);
  }
  const dateTime=text=>text?new Date(text).toLocaleString(locale==='ja'?'ja-JP':'en-US'):'—';

  const routeService=()=>serviceFromHash(location.hash);
  const initial=routeService();
  const [cloudStorage,setCloudStorage]=useState({}),[capabilities,setCapabilities]=useState(null);
  const [service,setService]=useState(initial),[bucket,setBucket]=useState(null),[items,setItems]=useState([]),[selected,setSelected]=useState([]),[filter,setFilter]=useState(''),[page,setPage]=useState(1),[sort,setSort]=useState({sortingField:'name'}),[descending,setDescending]=useState(false);
  const [region,setRegion]=useState(()=>new URLSearchParams(location.hash.split('?')[1]||'').get('region')||'us-east-1'),[principal,setPrincipal]=useState('simulator-admin'),[persistent,setPersistent]=useState(false),[busy,setBusy]=useState(true),[ready,setReady]=useState(false),[identitySelection,setIdentitySelection]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState(''),[modal,setModal]=useState(null),[form,setForm]=useState({}),[modalError,setModalError]=useState(''),[saving,setSaving]=useState(false),[navOpen,setNavOpen]=useState(innerWidth>900),[toolsOpen,setToolsOpen]=useState(false),[detail,setDetail]=useState(null);
  const generation=useRef(0), file=useRef(null), opener=useRef(null), parameterSelection=useRef(null), stackDetailsGeneration=useRef(0);
  const [navigationVersion,setNavigationVersion]=useState(0);
  const [authenticationRequired,setAuthenticationRequired]=useState(false),[accessError,setAccessError]=useState('');
  const [showVersions,setShowVersions]=useState(false),[bucketTab,setBucketTab]=useState("objects");
  const change=(key,v)=>setForm(old=>({...old,[key]:v}));
  const close=()=>{setModal(null);requestAnimationFrame(()=>{const previous=opener.current;if(!previous)return;if(previous.node.isConnected){previous.node.focus();return}const match=[...document.querySelectorAll('button')].find(b=>(b.getAttribute('aria-label')||b.textContent.trim())===previous.label&&!b.disabled&&b.getClientRects().length);match?.focus()})};
  const open=(kind,values={})=>{if(saving)return;opener.current={node:document.activeElement,label:document.activeElement?.getAttribute('aria-label')||document.activeElement?.textContent.trim()};setModalError('');setForm(values);file.current=null;setModal(kind)};
  const request=useCallback(async(path,options={},identity=principal)=>{
    const r=await fetch(path,{...options,headers:{'X-Simulator-Principal':identity,...options.headers}});
    if(r.status===401){location.assign('/_simulator/login');throw Error('Sign in to continue.');}
    if(!r.ok){const text=await r.text();let message=value(readXML(text),'Message');try{const j=JSON.parse(text);message=j.message||(typeof j.error==='object'?j.error?.message:j.error)||message}catch{};throw Error((value(readXML(text),'Code')||r.status)+': '+(message||text));}
    return r;
  },[principal]);
  async function signOut(){
    if(saving||!authenticationRequired)return;
    setSaving(true);setAccessError('');
    try{
      const response=await fetch('/_simulator/logout',{method:'POST',credentials:'same-origin'});
      if(response.status!==204)throw Error('The server did not confirm sign out ('+response.status+').');
      location.replace('/_simulator/login');
    }catch{setAccessError('Sign out was not confirmed. Your session may still be active. Try Sign out again.');setSaving(false)}
  }
  const invokeRequest=useCallback(async(path,options={})=>{
    const response=await fetch(path,{...options,headers:{'X-Simulator-Principal':principal,...options.headers}});
    // A guest application may legitimately return 401. Check host authentication
    // on its own protected endpoint before treating that status as an expired session.
    if(response.status===401)await request('/_simulator/v1/capabilities');
    return response;
  },[principal,request]);
  const ssm=async(op,input)=> (await request('/',{method:'POST',headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'AmazonSSM.'+op,'X-Simulator-Region':region},body:JSON.stringify(input)})).json();
  const cfn=async(op,input={})=>readXML(await (await request('/',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Region':region},body:new URLSearchParams({Action:op,Version:'2010-05-15',...input})})).text());
  async function load(){
    const revision=++generation.current;setBusy(true);setError('');setSelected([]);
    // Discovery is presentation only; unavailable routes must not issue native API calls.
    if(!serviceAvailable(service,capabilities)){setItems([]);setBusy(false);return;}
    if(providerForService(service)!=='aws'){setItems([]);setBusy(false);return;}
    try{
      let list=[];
      if(service==='s3'){
        if(bucket){let token='',versionToken='';do{const query=showVersions?'versions&max-keys=1000'+(token?'&key-marker='+encodeURIComponent(token)+'&version-id-marker='+encodeURIComponent(versionToken):''):'list-type=2&max-keys=1000'+(token?'&continuation-token='+encodeURIComponent(token):'');const xml=readXML(await(await request('/'+bucket+'?'+query)).text());const entries=showVersions?[...xml.documentElement.children].filter(n=>n.localName==='Version'||n.localName==='DeleteMarker'):nodes(xml,'Contents');list.push(...entries.map(n=>({name:value(n,'Key'),size:Number(value(n,'Size')),modified:value(n,'LastModified'),etag:value(n,'ETag'),...(showVersions?{versionId:value(n,'VersionId'),marker:n.localName==='DeleteMarker',latest:value(n,'IsLatest')==='true',id:value(n,'Key')+'\u0000'+value(n,'VersionId')}:{} )})));token=value(xml,showVersions?'NextKeyMarker':'NextContinuationToken');versionToken=value(xml,'NextVersionIdMarker');}while(token&&revision===generation.current)}
        else{const xml=readXML(await(await request('/')).text());list=nodes(xml,'Bucket').map(n=>({name:value(n,'Name'),region:value(n,'BucketRegion'),created:value(n,'CreationDate')})).filter(b=>b.region===region);}
      }else if(service==='ssm'){
        const deepParameter=new URLSearchParams(location.hash.split('?')[1]||'').get('parameter');
        if(deepParameter){const selection=parameterSelection.current;parameterSelection.current=null;const result=await ssm('GetParameter',{Name:deepParameter});if(revision===generation.current){const description=selection?.name===deepParameter&&selection.region===region&&selection.principal===principal?selection.description:undefined;setDetail({...result.Parameter,...(description!==undefined?{Description:description}:{})});setItems([]);setPage(1)}return;}
        const scopedParameter=capabilities?.consoleScope?.parameterName;
        // The handoff scope already supplies this exact name. Do not fetch its
        // value merely to render a list, and do not broaden enumeration rights.
        if(scopedParameter){setDetail(null);setItems([{name:scopedParameter}]);setPage(1);return;}
        let token;do{const result=await ssm('DescribeParameters',token?{NextToken:token}:{});list.push(...result.Parameters.map(p=>({...p,name:p.Name,type:p.Type,version:p.Version,modified:new Date(p.LastModifiedDate*1000).toISOString()})));token=result.NextToken;}while(token&&revision===generation.current)
      }else if(service==='cfn'){const xml=await cfn('DescribeStacks');list=nodes(xml,'StackName').map(n=>({name:n.textContent,status:value(n.parentNode,'StackStatus'),created:value(n.parentNode,'CreationTime'),id:value(n.parentNode,'StackId')}));}
      if(revision===generation.current){setItems(list);setPage(1)}
    }catch(e){if(revision===generation.current){setItems([]);setError(e.message)}}finally{if(revision===generation.current)setBusy(false)}
  }
  useEffect(()=>{fetch('/_simulator/v1/capabilities').then(async r=>{if(r.status===401){location.assign('/_simulator/login');return null}setAuthenticationRequired(r.headers.get('X-Simulator-Authentication')==='required');return r.json()}).then(c=>{if(!c)return;setCapabilities(c);if(c.consoleScope?.region)setRegion(c.consoleScope.region);setPrincipal(c.principal||'simulator-admin');setIdentitySelection(c.identitySelection===true);setPersistent(c.persistence==='file');setCloudStorage(c.cloudStorage||{});setReady(true)}).catch(e=>{setError(e.message);setBusy(false)})},[]);
  useEffect(()=>{if(ready){setDetail(null);setMessage('');load()}},[ready,capabilities,service,bucket,region,principal,navigationVersion,showVersions]);
  function dismissResourceForm(){setModal(null);setModalError('');setForm({});file.current=null;}
  useEffect(()=>{const change=()=>{const routedRegion=new URLSearchParams(location.hash.split('?')[1]||'').get('region');if(routedRegion)setRegion(routedRegion);generation.current++;setNavigationVersion(v=>v+1);setService(routeService());setBucket(null);setShowVersions(false);setBucketTab('objects');setDetail(null);setFilter('');setSelected([]);dismissResourceForm();};addEventListener('hashchange',change);return()=>removeEventListener('hashchange',change);},[]);
  function navigate(next){if(saving||!serviceAvailable(next,capabilities))return;const hash=hashForService(next);if(!hash)return;generation.current++;setNavigationVersion(v=>v+1);setService(next);setBucket(null);setShowVersions(false);setBucketTab('objects');setDetail(null);setFilter('');setSelected([]);dismissResourceForm();location.hash=hash;}
  function changeRegion(next){if(saving||next===region)return;if(service==='ssm')history.replaceState(null,'','#/parameters?'+new URLSearchParams({region:next}).toString());generation.current++;setRegion(next);setBucket(null);setShowVersions(false);setBucketTab('objects');setDetail(null);setSelected([]);setFilter('');setModal(null);setModalError('');setError('');setMessage('');}
  function openStackHistory(stackName){setDetail({stackName,revision:++stackDetailsGeneration.current});}
  async function inspect(item){
    if(service==='s3'&&!bucket){setBucket(item.name);setShowVersions(false);setBucketTab('objects');setFilter('');return}
    if(service==='s3'){if(item.marker){open('delete',item);return}open('object',item);return}
    try{if(service==='ssm'){parameterSelection.current={name:item.name,region,principal,description:item.Description};location.hash='/parameters?'+new URLSearchParams({region,parameter:item.name}).toString()}else{openStackHistory(item.id||item.name)} }catch(e){setError(e.message)}
  }
  async function submit(){
    setSaving(true);setModalError('');
    try{
      if(modal==='create-bucket'){
        if(!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(form.name||''))throw Error('Use a DNS-compatible bucket name of 3–63 characters.');
        const body=region==='us-east-1'?'':`<CreateBucketConfiguration><LocationConstraint>${region}</LocationConstraint></CreateBucketConfiguration>`;await request('/'+form.name,{method:'PUT',body});setMessage({kind:'bucket-created',name:form.name});
      }else if(modal==='upload'){
        if(!file.current)throw Error('Choose a file.');if(file.current.size>8388608)throw Error('This file exceeds the 8 MiB simulator limit.');if(!form.name)throw Error('Enter an object key.');if(new TextEncoder().encode(form.name).length>1024)throw Error('Object keys must be at most 1024 bytes.');await request(keyPath(bucket,form.name),{method:'PUT',body:file.current,headers:{'Content-Type':file.current.type||'application/octet-stream'}});setMessage('Object uploaded.');
      }else if(modal==='delete'){
        if(form.confirmation!==form.name)throw Error('Enter the exact name to confirm deletion.');if(service==='s3')await request(bucket?keyPath(bucket,form.name)+(form.versionId?'?versionId='+encodeURIComponent(form.versionId):''):'/'+form.name,{method:'DELETE'});else if(service==='ssm'){await ssm('DeleteParameter',{Name:form.name});history.replaceState(null,'','#/parameters?'+new URLSearchParams({region}).toString())}else await cfn('DeleteStack',{StackName:form.name});setDetail(null);setMessage('Resource deleted.');
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
  const label=service==='home'?t('Console Home','コンソールのホーム'):service==='sakura-disks'?'Sakura Cloud Disks':service==='gcp-storage'?'Google Cloud Storage':service==='azure-blob'?'Azure Blob Storage':service==='azure-arm'?'Azure Resource Manager':service==='s3'?'Amazon S3':service==='ssm'?'Systems Manager':service==='lambda'?'Lambda':service==='iam'?'IAM':service==='kms'?'Key Management Service':service==='apigateway'?'API Gateway':service==='wafv2'?'AWS WAF':service==='dynamodb'?'DynamoDB':service==='sqs'?'Amazon SQS':service==='logs'?'CloudWatch Logs':service==='secretsmanager'?'Secrets Manager':service==='cfn'?'CloudFormation':service==='catalog'?t('AWS service catalog','AWS サービスカタログ'):service==='gcp-catalog'?t('Google Cloud service catalog','Google Cloud サービスカタログ'):t('Console unavailable','コンソールは利用できません');
  const title=service==='s3'?(bucket?t('Objects','オブジェクト'):t('General purpose buckets','汎用バケット')):service==='ssm'?t('My parameters','マイパラメータ'):t('Stacks','スタック');
  const primary=()=>open(service==='s3'?(bucket?'upload':'create-bucket'):'parameter',{type:'String'});
  const primaryText=service==='s3'?(bucket?t('Upload','アップロード'):t('Create bucket','バケットを作成')):t('Create parameter','パラメータを作成');
  const filtered=items.filter(i=>Object.values(i).some(v=>String(v).toLowerCase().includes(filter.toLowerCase()))).sort((a,b)=>{const key=sort.sortingField||'name';const av=a[key]??'',bv=b[key]??'';return (typeof av==='number'?av-bv:String(av).localeCompare(String(bv)))*(descending?-1:1)});
  const columns=[{id:'name',header:t('Name','名前'),sortingField:'name',cell:item=><Link onFollow={e=>{e.preventDefault();inspect(item)}} href="#">{item.name}</Link>},...(service==='s3'?(bucket?[...(showVersions?[{id:'version',header:t('Version ID','バージョンID'),cell:i=>identifier(i.versionId)},{id:'type',header:t('Type','タイプ'),cell:i=>i.marker?t('Delete marker','削除マーカー'):t('Object','オブジェクト')},{id:'latest',header:t('Latest version','最新バージョン'),cell:i=>i.latest?t('Yes','はい'):t('No','いいえ')}]:[]),{id:'size',header:t('Size','サイズ'),sortingField:'size',cell:i=>i.size+' B'},{id:'modified',header:t('Last modified','最終更新日時'),cell:i=>dateTime(i.modified)},{id:'etag',header:'ETag',cell:i=>i.etag}]:[{id:'region',header:t('AWS Region','AWSリージョン'),cell:i=>i.region},{id:'created',header:t('Creation date','作成日時'),cell:i=>dateTime(i.created)}]):service==='ssm'?[{id:'tier',header:t('Tier','利用枠'),cell:i=>i.Tier||'—'},{id:'type',header:t('Type','種類'),cell:i=>i.type||'—'},{id:'dataType',header:t('Data type','データ型'),cell:i=>i.DataType||'—'},{id:'modified',header:t('Last modified date','最終変更日'),cell:i=>dateTime(i.modified)}]:[{id:'status',header:t('Status','状態'),cell:i=><StatusIndicator type="success">{i.status}</StatusIndicator>},{id:'created',header:t('Creation date','作成日時'),cell:i=>dateTime(i.created)}])];
  const defaultPolicy=JSON.stringify({Version:'2012-10-17',Statement:[{Effect:'Allow',Action:['s3:ListAllMyBuckets','s3:ListBucket','s3:GetObject'],Resource:'*'}]},null,2);
  const modalTitle=modal==='create-bucket'?t('Create bucket','バケットを作成'):modal==='upload'?t('Upload','アップロード'):modal==='object'?t('Object details','オブジェクトの詳細'):modal==='identity'?t('Choose identity','演習IDを選択'):modal==='policy'?t('Manage identity policy','IDポリシーを管理'):modal==='parameter'?(form.overwrite?t('Edit parameter','パラメータを編集'):t('Create parameter','パラメータを作成')):(bucket?(form.versionId?t('Delete object version','オブジェクトのバージョンを削除'):t('Delete object','オブジェクトを削除')):service==='ssm'?t('Delete parameter','パラメータを削除'):service==='cfn'?t('Delete stack','スタックを削除'):t('Delete bucket','バケットを削除'));
  const pageForm=['create-bucket','upload','parameter'].includes(modal);
  const actionText=modal==='parameter'?(form.overwrite?t('Save changes','変更を保存'):t('Create parameter','パラメータを作成')):primaryText;
  const fields=<SpaceBetween size="l">
    {(modal==='create-bucket'||modal==='upload'||modal==='parameter')&&<FormField label={modal==='create-bucket'?t('Bucket name','バケット名'):modal==='upload'?t('Object key','オブジェクトキー'):t('Name','名前')} constraintText={modal==='upload'?t("Maximum 1024 UTF-8 bytes. Slashes and dot segments are literal key data.","最大1024 UTF-8バイト。スラッシュとドットはキーの文字列として扱います。"):modal==='parameter'?t("Use a name such as /team/hello. Only Standard text parameters are supported.","/team/helloなどの名前を使用してください。Standardのテキストパラメータのみ対応しています。"):t("3–63 lowercase letters, numbers, dots and hyphens.","3〜63文字の英小文字、数字、ドット、ハイフン。")}><Input autoFocus value={form.name||''} readOnly={!!form.overwrite} onChange={({detail})=>change('name',detail.value)}/></FormField>}
    {modal==='create-bucket'&&<FormField label={t('Region','リージョン')}><Select disabled={saving} selectedOption={{label:region,value:region}} options={regions.map(r=>({label:r,value:r}))} onChange={({detail})=>setRegion(detail.selectedOption.value)}/></FormField>}
    {modal==='upload'&&<FormField label={t('Choose a file','ファイルを選択')}><FileUpload value={file.current?[file.current]:[]} onChange={({detail})=>{file.current=detail.value[0];setForm(f=>({...f,name:detail.value[0]?.name||f.name}))}} showFileSize i18nStrings={{uploadButtonText:()=> t("Add files","ファイルを追加"),removeFileAriaLabel:i=>t('Remove file ','ファイルを削除 ')+i}} constraintText={t('Maximum 8 MiB. Uploads change the local SQLite state.','最大8 MiB。アップロードはローカルSQLiteの状態を変更します。')}/></FormField>}
    {modal==='parameter'&&<><FormField label={t('Description','説明')} description={form.overwrite&&!Object.hasOwn(form,'description')?t('The current description was not loaded. Leave this field untouched to preserve it, or enter a replacement.','現在の説明は未取得です。変更しなければ保持されます。置き換える場合は入力してください。'):undefined}><Input value={form.description||''} onChange={({detail})=>change('description',detail.value)}/></FormField><FormField label={t('Parameter tier','パラメータ階層')}><RadioGroup value="Standard" items={[{value:'Standard',label:'Standard'},{value:'Advanced',label:'Advanced',disabled:true,description:t('Advanced tier is not implemented in this simulator','このシミュレーターはAdvanced階層に対応していません')}]}/></FormField><FormField label={t('Type','タイプ')}><Select disabled={!!form.overwrite} selectedOption={{label:form.type||'String',value:form.type||'String'}} options={[{label:'String',value:'String'},{label:'StringList',value:'StringList'},{label:'SecureString',value:'SecureString',disabled:true,description:t('SecureString encryption is not integrated with Parameter Store.','SecureStringの暗号化はParameter Storeと連携していません。')}]} onChange={({detail})=>change('type',detail.selectedOption.value)}/></FormField><FormField label={t('Data type','データ型')}><Select disabled selectedOption={{label:"text",value:"text"}} options={[{label:"text",value:"text"}]}/></FormField><FormField label={t('Value','値')}><Textarea rows={5} value={form.value||''} onChange={({detail})=>change('value',detail.value)}/></FormField></>}
    {(modal==='identity'||modal==='policy')&&<FormField label={t('Principal name','演習ID名')}><Input autoFocus value={form.principal||''} onChange={({detail})=>change('principal',detail.value)}/></FormField>}
    {modal==='policy'&&<FormField label={t('Identity policy JSON','IDポリシーJSON')}><Textarea rows={12} value={form.policy||''} onChange={({detail})=>change('policy',detail.value)}/></FormField>}
    {modal==='object'&&<KeyValuePairs columns={1} items={[{label:t('Key','キー'),value:identifier(form.name)},...(form.versionId?[{label:t('Version ID','バージョンID'),value:identifier(form.versionId)}]:[]),{label:t('Size','サイズ'),value:form.size+' B'},{label:'ETag',value:form.etag}]}/>}
    {modal==='delete'&&<><Alert type="warning">{t('Delete','削除')} “{identifier(form.name)}”? {bucket?(form.versionId?t("This permanently deletes the selected version and cannot be undone.","選択したバージョンを完全に削除します。この操作は取り消せません。"):t("A versioned bucket adds a delete marker; an unversioned object is permanently deleted.","バージョニング有効時は削除マーカーを追加します。無効時はオブジェクトを完全に削除します。")):t("Buckets must contain no objects, versions or delete markers.","バケット内のオブジェクト、バージョン、削除マーカーはすべて削除する必要があります。")}</Alert><FormField label={identifier(t('Type ','確認のため「')+form.name+t(' to confirm','」と入力'))}><Input autoFocus value={form.confirmation||''} onChange={({detail})=>change('confirmation',detail.value)}/></FormField></>}
  </SpaceBetween>;
  const provider=providerForService(service)||'aws';
  const sakuraScope=sakuraConfiguration(capabilities);
  const sakuraContext=JSON.stringify(['sakura',principal,navigationVersion,sakuraScope?.endpoint]);
  const controls=<GlobalNavigation service={service} capabilities={capabilities} saving={saving}
    onNavigate={navigate} principal={principal} authenticationRequired={authenticationRequired}
    identitySelection={identitySelection} onIdentity={()=>open('identity',{principal})} onSignOut={signOut}
    region={region} regions={capabilities?.consoleScope?[capabilities.consoleScope.region]:regions} onRegion={changeRegion} sessionError={feedback(accessError)}/>;
  const unavailable=<ContentLayout header={<Header variant="h1">{t('Console unavailable','コンソールは利用できません')}</Header>}><Alert type="info">{t('Choose an implemented service from Services. The requested console or provider scope is not available.','サービスから実装済みの機能を選択してください。指定されたコンソールまたはプロバイダー設定は利用できません。')}</Alert></ContentLayout>;
  const awsContent=service==='home'?<ConsoleHome capabilities={capabilities} onNavigate={navigate} region={region}/>:service==='wafv2'?<WAFv2View key={region+'|'+principal+'|'+navigationVersion} region={region} request={request} onSaving={setSaving}/>:service==='catalog'?<CatalogView key={region+'|'+principal+'|'+navigationVersion} region={region} request={request} catalog={capabilities?.services?.catalog} onSaving={setSaving}/>:service==='dynamodb'?<DynamoDBView key={region+'|'+principal+'|'+navigationVersion} region={region} request={request} onSaving={setSaving}/>:service==='sqs'?<SQSView key={region+'|'+principal+'|'+navigationVersion} region={region} request={request} onSaving={setSaving}/>:service==='logs'?<CloudWatchLogsView key={region+principal+navigationVersion} region={region} request={request} onSaving={setSaving}/>:service==='secretsmanager'?<SecretsManagerView key={region+principal+navigationVersion} region={region} request={request} onSaving={setSaving}/>:service==='kms'?<KMSView key={region+principal+navigationVersion} region={region} principal={principal} request={request} onSaving={setSaving}/>:service==='apigateway'?<APIGatewayView key={region+principal+navigationVersion} region={region} request={request} invokeRequest={invokeRequest} onSaving={setSaving}/>:service==='iam'?<IAMView key={principal+navigationVersion} request={request} onSaving={setSaving} onLambda={()=>navigate('lambda')}/>:service==='lambda'?<LambdaView key={region+principal+navigationVersion} region={region} request={request} onSaving={setSaving} onIAM={()=>navigate('iam')}/>:pageForm?<ContentLayout header={<Header variant="h1">{modalTitle}</Header>}><form onSubmit={e=>{e.preventDefault();submit()}}><Form errorText={modalError} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={close}>{t('Cancel','キャンセル')}</Button><Button variant="primary" formAction="submit" loading={saving}>{actionText}</Button></SpaceBetween>}><Container header={<Header variant="h2">{modal==='create-bucket'?t('General configuration','全般設定'):modal==='upload'?t('Files and folders','ファイルとフォルダー'):t('Parameter details','パラメータの詳細')}</Header>}><SpaceBetween size="l"><Alert type="info">{t("Only the documented simulator subset is available. Unsupported AWS settings cannot be enabled.","ドキュメントに記載された機能のみ利用できます。未対応のAWS設定は有効にできません。")}</Alert>{fields}</SpaceBetween></Container></Form></form></ContentLayout>:service==='ssm'&&detail?<ContentLayout header={<Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel={t('Refresh parameter','パラメータを更新')} onClick={load}/><Button onClick={()=>open('parameter',{name:detail.Name,type:detail.Type,value:detail.Value,...(detail.Description!==undefined?{description:detail.Description}:{}),overwrite:true})}>{t('Edit parameter','パラメータを編集')}</Button><Button onClick={()=>open('delete',{name:detail.Name})}>{t('Delete parameter','パラメータを削除')}</Button></SpaceBetween>}>{identifier(detail.Name)}</Header>}><SpaceBetween size="l">{error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success" dismissible onDismiss={()=>setMessage('')}>{notice(message)}</Alert>}<Alert type="info">{t("Independent local simulation. Standard text parameters only.","独立したローカルシミュレーションです。Standardのテキストパラメータのみ対応しています。")}</Alert><Tabs activeTabId="overview" tabs={[{id:'overview',label:t('Overview','概要')},{id:'history',label:t('History','履歴'),disabled:true},{id:'tags',label:t('Tags','タグ'),disabled:true}]}/><Container header={<Header variant="h2">{t('Parameter details','パラメータの詳細')}</Header>}><SpaceBetween size="l"><KeyValuePairs columns={2} minColumnWidth={280} items={[{label:t('Name','名前'),value:identifier(detail.Name)},{label:'ARN',value:identifier(detail.ARN)},{label:t('Type','タイプ'),value:detail.Type},{label:t('Version','バージョン'),value:detail.Version},{label:t('Tier','階層'),value:'Standard'},{label:t('Data type','データ型'),value:detail.DataType}]}/><FormField label={t('Value','値')}><Textarea readOnly rows={4} value={detail.Value}/></FormField></SpaceBetween></Container></SpaceBetween></ContentLayout>:<SpaceBetween size="l">
        <Alert type="info" header={t('Local simulation','ローカルシミュレーション')}>{persistent?t("Your resources survive restart.","リソースは再起動後も保持されます。"):t("Your resources reset when the process stops.","プロセスが停止するとリソースがリセットされます。")} {t('This is an independent simulator, not the AWS console.','AWSコンソールとは異なる独立したシミュレーターです。')}</Alert>
        {error&&items.length>0&&<Alert type="error">{error}</Alert>}
        {message&&<Alert type="success" dismissible onDismiss={()=>setMessage('')}>{notice(message)}</Alert>}
        {service==='ssm'&&<><Header variant="h1">Parameter Store</Header><Tabs activeTabId="mine" tabs={[{id:'mine',label:t('My parameters','マイパラメータ')},{id:'public',label:t('Public parameters (not implemented)','パブリックパラメータ（未対応）'),disabled:true},{id:'settings',label:t('Settings (not implemented)','設定（未対応）'),disabled:true}]}/>{capabilities?.consoleScope?<Alert type="info">{t('Only the parameter assigned to this session is listed. Open its name to read its value. Metadata is not fetched until you open it.','このセッションに割り当てられたパラメータだけを表示しています。名前を開くと値を読み取れます。詳細情報は開くまで取得しません。')}</Alert>:error&&<Container header={<Header variant="h2">{t('Open a parameter by name','名前を指定してパラメータを開く')}</Header>}><Form actions={<Button onClick={()=>inspect({name:form.lookup||''})}>{t('Open parameter','パラメータを開く')}</Button>}><FormField label={t('Parameter name','パラメータ名')} description={t('Use a direct name when your identity cannot list parameters.','演習IDに一覧表示権限がない場合は、名前を直接指定してください。')}><Input value={form.lookup||''} placeholder="/team/hello" onChange={({detail})=>change('lookup',detail.value)}/></FormField></Form></Container>}</>}
        {service==='cfn'&&<CloudFormationLookup onOpen={openStackHistory}/>}
        {bucket&&<><Header variant="h1">{identifier(bucket)}</Header><Tabs tabs={[{id:'objects',label:t('Objects','オブジェクト')},{id:'properties',label:t('Properties','プロパティ')},{id:'permissions',label:t('Permissions','アクセス許可')},{id:'metrics',label:t('Metrics','メトリクス'),disabled:true},{id:'management',label:t('Management','管理')}]} activeTabId={bucketTab} onChange={({detail})=>{if(saving)return;setBucketTab(detail.activeTabId);dismissResourceForm()}}/></>}
        {bucket&&bucketTab==='management'?<S3Multipart key={bucket+principal+region+navigationVersion} bucket={bucket} region={region} request={request} onSaving={setSaving} onComplete={load}/>:bucket&&bucketTab!=='objects'?<S3Settings key={bucket+principal+bucketTab} bucket={bucket} tab={bucketTab} request={request} onSaving={setSaving}/>:<>
        <Table variant="full-page" stickyHeader loading={busy} loadingText={t('Loading resources','リソースを読み込み中')} trackBy={showVersions&&bucket?"id":"name"} selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>setSelected(detail.selectedItems)} ariaLabels={{selectionGroupLabel:t("Resource selection","リソースの選択"),itemSelectionLabel:(_data,item)=>t('Select ','選択 ')+item.name,tableLabel:title}} columnDefinitions={columns} items={filtered.slice((page-1)*10,page*10)} sortingColumn={sort} sortingDescending={descending} onSortingChange={({detail})=>{setSort(detail.sortingColumn);setDescending(detail.isDescending);setSelected([])}}
          header={<Header variant="awsui-h1-sticky" counter={'('+items.length+')'} description={bucket?t("Objects are identified by their complete key. The console and SDK share the same state.","オブジェクトは完全なキーで識別されます。コンソールとSDKは同じ状態を使用します。"):service==='ssm'?t("Store and read text settings through the same SSM API as SDK clients.","SDKと同じSSM APIを使用してテキスト設定を保存・取得します。"):service==='cfn'?t("Only the documented CloudFormation resource types are supported. Stack updates and change sets are not implemented.","ドキュメントに記載されたCloudFormationリソースのみ対応しています。更新と変更セットは未対応です。"):t("Create buckets and organize your local objects.","バケットを作成し、ローカルのオブジェクトを整理します。")} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel={t('Refresh resources','リソースを更新')} onClick={load}/><Button disabled={!selected.length} onClick={()=>open('delete',{...selected[0]})}>{t('Delete','削除')}</Button>{service!=='cfn'&&<Button variant="primary" onClick={primary}>{primaryText}</Button>}</SpaceBetween>}>{title}</Header>}
          filter={<SpaceBetween direction="horizontal" size="s">{bucket&&<Toggle checked={showVersions} onChange={({detail})=>{generation.current++;setShowVersions(detail.checked);setSelected([]);setFilter('')}}>{t('Show versions','バージョンを表示')}</Toggle>}<TextFilter filteringText={filter} filteringPlaceholder={bucket?t("Find objects by key","キーでオブジェクトを検索"):t("Find resources by name","名前でリソースを検索")} filteringAriaLabel={t('Find resources','リソースを検索')} onChange={({detail})=>{setFilter(detail.filteringText);setPage(1);setSelected([])}} countText={filtered.length+t(' matches','件一致')}/></SpaceBetween>}
          pagination={<Pagination currentPageIndex={page} pagesCount={Math.max(1,Math.ceil(filtered.length/10))} onChange={({detail})=>{setPage(detail.currentPageIndex);setSelected([])}} ariaLabels={{nextPageLabel:t("Next page","次のページ"),previousPageLabel:t("Previous page","前のページ"),pageLabel:n=>t('Page ','ページ ')+n}}/>}
          empty={<Box textAlign="center" margin={{vertical:'l'}}><SpaceBetween size="m">{error?<Alert type="error" header={t('Could not load resources','リソースを読み込めませんでした')}>{error}</Alert>:<><Box variant="h2">{filter?t('No matching resources','一致するリソースはありません'):bucket?t('This bucket is empty','このバケットは空です'):service==='ssm'?t('No parameters yet','パラメータはありません'):service==='cfn'?t('No stacks yet','スタックはありません'):t('No buckets yet','バケットはありません')}</Box><Box>{filter?t("Clear your filter to see resources.","フィルターを解除するとリソースが表示されます。"):t("Create a resource or use an SDK client.","リソースを作成するか、SDKを使用してください。")}</Box>{filter?<Button onClick={()=>setFilter('')}>{t('Clear filter','フィルターをクリア')}</Button>:service!=='cfn'&&<Button onClick={primary}>{primaryText}</Button>}</>}</SpaceBetween></Box>}/>
        </>}
        {service==='cfn'&&detail&&<CloudFormationDetails key={JSON.stringify([detail.stackName,detail.revision,region,principal,navigationVersion])} stackName={detail.stackName} region={region} request={request} onDismiss={()=>setDetail(null)}/>}
        <Box color="text-body-secondary" fontSize="body-s">{principal} · {region} · {persistent?t('Persistent environment','永続環境'):t('Temporary environment','一時環境')} · {t('No external cloud connection','外部クラウド接続なし')}</Box>
      </SpaceBetween>;
  return <>
    {provider==='sakura'?<SakuraConsole controls={controls} configuration={sakuraScope}>{ready&&serviceAvailable(service,capabilities)?<SakuraDisks key={sakuraContext} request={request} configuration={sakuraScope} contextKey={sakuraContext}/>:unavailable}</SakuraConsole>:provider==='gcp'&&service==='gcp-catalog'?<GCPConsoleShell controls={controls} product={t('Service catalog','サービスカタログ')} resource={t('Google APIs','Google API')}>{ready&&serviceAvailable(service,capabilities)?<GCPCatalogView key={'gcp-catalog:'+principal} request={request} catalog={capabilities?.gcpCatalog}/>:unavailable}</GCPConsoleShell>:provider==='gcp'?<GCPConsoleShell controls={controls}>{ready&&serviceAvailable(service,capabilities)?<GCPStorageView key={'gcp:'+principal+':'+cloudStorage.gcp?.project} request={request} project={cloudStorage.gcp?.project}/>:unavailable}</GCPConsoleShell>:provider==='azure'?<AzureConsoleShell controls={controls}>{ready&&serviceAvailable(service,capabilities)?(service==='azure-arm'?<AzureARMView key={'azure-arm:'+principal} request={request} catalog={capabilities?.azureCatalog}/>:<AzureBlobView key={'azure:'+principal+':'+cloudStorage.azure?.account} request={request} account={cloudStorage.azure?.account}/>):unavailable}</AzureConsoleShell>:<>{controls}
    <AppLayout ariaLabels={{navigation:t("Service navigation","サービスナビゲーション"),navigationToggle:t("Toggle service navigation","サービスナビゲーションを切り替え"),navigationClose:t("Close service navigation","サービスナビゲーションを閉じる"),tools:t("Help panel","ヘルプパネル"),toolsToggle:t("Toggle help panel","ヘルプパネルを切り替え"),toolsClose:t("Close help panel","ヘルプパネルを閉じる")}} headerSelector="#navigation" contentType={pageForm?"form":service==='home'?"default":"table"} navigationHide={service==='home'} navigationOpen={navOpen} onNavigationChange={({detail})=>setNavOpen(detail.open)} toolsOpen={toolsOpen} onToolsChange={({detail})=>setToolsOpen(detail.open)} tools={<HelpPanel header={<Header variant="h2">{t('Simulator scope','シミュレーターの対応範囲')}</Header>}><SpaceBetween size="m"><p>{t("Independent local simulator. Not affiliated with AWS. No real cloud resources are created.","AWSとは無関係の独立したローカルシミュレーターです。実クラウドのリソースは作成されません。")}</p><p>{t("S3 basic operations, Standard SSM text parameters, Python 3.12 Lambda functions and only the documented CloudFormation resource types. Stack updates and change sets are not implemented.","S3の基本操作、StandardのSSMテキストパラメータ、Python 3.12のLambdaに対応しています。CloudFormationは記載されたリソースに限定され、更新と変更セットは未対応です。")}</p><p>{t("KMS supports bounded local symmetric keys, explicit IAM and simulator-only ciphertext; no real HSM or other-service encryption integration.","KMSは限定されたローカル対称キー、明示的IAM、専用の暗号文に対応します。実HSMや他サービスの暗号化連携はありません。")}</p><p>{t("Google Cloud Storage and Azure Blob Storage have bounded local owner-only scopes. Google IAM, Azure RBAC, federation and cloud account provisioning are not implemented.","Cloud StorageとBlob Storageはローカル所有者のみの限定スコープです。Google IAM、Azure RBAC、フェデレーション、クラウドアカウント作成は未対応です。")}</p><Button onClick={()=>open('policy',{principal:'reader',policy:defaultPolicy})}>{t('Manage identity policy','IDポリシーを管理')}</Button><Link href="/_simulator/licenses/CONSOLE-NOTICES.txt">{t('Console licenses','コンソールのライセンス')}</Link></SpaceBetween></HelpPanel>}
      navigation={<SideNavigation header={{text:label,href:'#'}} activeHref={'#/'+service} onFollow={e=>{e.preventDefault();const v=e.detail.href;if(v==='#/policy'){open('policy',{principal:'reader',policy:defaultPolicy})}else navigate(v==='#'?service:v.slice(2))}} items={[...(service==='s3'?[{type:'link',text:t('General purpose buckets','汎用バケット'),href:'#/s3'}]:service==='ssm'?[{type:'link',text:t('Parameter Store','Parameter Store'),href:'#/ssm'}]:service==='lambda'?[{type:'link',text:t('Functions','関数'),href:'#/lambda'}]:service==='iam'?[{type:'link',text:t('Roles','ロール'),href:'#/iam'}]:service==='kms'?[{type:'link',text:t('Customer managed keys','カスタマー管理キー'),href:'#/kms'}]:service==='apigateway'?[{type:'link',text:t('APIs','API'),href:'#/apigateway'}]:service==='wafv2'?[{type:'link',text:t('Web ACLs','ウェブACL'),href:'#/wafv2'}]:service==='dynamodb'?[{type:'link',text:t('Tables','テーブル'),href:'#/dynamodb'}]:service==='sqs'?[{type:'link',text:t('Queues','キュー'),href:'#/sqs'}]:service==='logs'?[{type:'link',text:t('Log groups','ロググループ'),href:'#/logs'}]:service==='secretsmanager'?[{type:'link',text:t('Secrets','シークレット'),href:'#/secretsmanager'}]:[{type:'link',text:t('Stacks','スタック'),href:'#/cfn'}]),...(identitySelection?[{type:'divider'},{type:'section',text:t('Simulator controls','シミュレーターの操作'),items:[{type:'link',text:t('Identity policies','IDポリシー'),href:'#/policy'}]}]:[])]}/>} breadcrumbs={<BreadcrumbGroup items={service==='home'?[]:[{text:t('Console Home','コンソールのホーム'),href:'#/home'},{text:service==='ssm'?'AWS Systems Manager':label,href:'#/'+service},...(bucket?[{text:bucket,href:'#/s3'}]:[]),...(service==='ssm'?[{text:t('Parameter Store','パラメータストア'),href:'#/ssm'},...(detail?[{text:detail.Name,href:location.hash}]:[])]:[])]} onFollow={e=>{e.preventDefault();navigate(e.detail.href.slice(2))}} ariaLabel={t('Breadcrumbs','パンくずリスト')}/>}
      content={!ready?<StatusIndicator type="loading">{t('Loading service capabilities','サービスの対応状況を読み込み中')}</StatusIndicator>:serviceAvailable(service,capabilities)?awsContent:unavailable}
    />
    </>}
    {!pageForm&&<Modal visible={!!modal} onDismiss={()=>!saving&&close()} header={modalTitle} closeAriaLabel={t('Close dialog','ダイアログを閉じる')} footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={close}>{t('Cancel','キャンセル')}</Button><Button variant="primary" loading={saving} onClick={submit}>{modal==='delete'?(bucket&&!form.versionId?t('Delete object','オブジェクトを削除'):t('Delete permanently','完全に削除')):modal==='object'?t('Download object','オブジェクトをダウンロード'):modal==='identity'?t('Choose identity','演習IDを選択'):modal==='policy'?t('Save policy','ポリシーを保存'):modal==='parameter'?(form.overwrite?t('Save changes','変更を保存'):t('Create parameter','パラメータを作成')):primaryText}</Button></SpaceBetween></Box>}><Form errorText={modalError}>{fields}</Form></Modal>}
  </>;
}
createRoot(document.getElementById('root')).render(<LocaleProvider>{location.pathname==='/_simulator/login'?<Login/>:location.hash==='#/terminal'?<SSMTerminal/>:<Console/>}</LocaleProvider>);
