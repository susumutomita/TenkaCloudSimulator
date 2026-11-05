import React,{useLayoutEffect,useMemo,useRef,useState} from 'react';
import {useLocale} from './i18n.jsx';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';

const pageSize=20;
const identifier=text=><span className="resource-identifier">{text}</span>;
function physicalName(region,arn){
 const prefix=`arn:aws:secretsmanager:${region}:000000000000:secret:`;
 if(!arn.startsWith(prefix))return null;
 const tail=arn.slice(prefix.length);
 return /^[A-Za-z0-9/_+=.@-]{1,512}-[A-Za-z0-9]{6}$/.test(tail)?tail.slice(0,-7):null;
}
function metadataPage(data,arn,name){
 if(!data||typeof data!=='object'||Array.isArray(data)||data.ARN!==arn||data.Name!==name||!Array.isArray(data.Versions)||data.Versions.length>pageSize||Object.keys(data).some(key=>!['ARN','Name','Versions','NextToken'].includes(key)))return null;
 if(Object.hasOwn(data,'NextToken')&&(typeof data.NextToken!=='string'||!data.NextToken||data.NextToken.length>4096))return null;
 const ids=new Set(),items=[];
 for(const item of data.Versions){
  if(!item||typeof item!=='object'||Array.isArray(item)||Object.keys(item).some(key=>!['VersionId','VersionStages','CreatedDate'].includes(key))||typeof item.VersionId!=='string'||Array.from(item.VersionId).length<32||Array.from(item.VersionId).length>64||ids.has(item.VersionId)||typeof item.CreatedDate!=='number'||!Number.isFinite(item.CreatedDate)||item.CreatedDate<0)return null;
  const date=new Date(item.CreatedDate*1000);
  if(!Number.isFinite(date.getTime()))return null;
  let stages=[];
  if(Object.hasOwn(item,'VersionStages')){
   if(!Array.isArray(item.VersionStages)||!item.VersionStages.length||item.VersionStages.length>2||new Set(item.VersionStages).size!==item.VersionStages.length||item.VersionStages.some(stage=>stage!=='AWSCURRENT'&&stage!=='AWSPREVIOUS'))return null;
   stages=[...item.VersionStages];
  }
  ids.add(item.VersionId);items.push({id:item.VersionId,stages,created:date.toISOString()});
 }
 return {items,next:data.NextToken||''};
}

// request is the host's principal-bound callback, never selected by this child.
// Parent retains its region/principal/navigation remount contract. Request
// identity also participates in this child's guard when a host reuses it.
export default function SecretVersions({region,request,secretARN='',active=true}){
 const {t}=useLocale();
 const scope=useMemo(()=>({region,request,secretARN,active}),[region,request,secretARN,active]);
 const scopeNow=useRef(scope);
 const epoch=useRef(0),alive=useRef(false),pending=useRef(null);
 const [arn,setARN]=useState(secretARN),[include,setInclude]=useState(false),[result,setResult]=useState(null),[loading,setLoading]=useState(false),[problem,setError]=useState(null);
 const clear=()=>{epoch.current++;pending.current?.abort();pending.current=null;setLoading(false);setResult(null);setError(null)};
 useLayoutEffect(()=>{
  scopeNow.current=scope;alive.current=true;clear();setARN(secretARN);setInclude(false);
  return()=>{alive.current=false;epoch.current++;pending.current?.abort();pending.current=null};
 },[scope]);
 const error=problem?.scope===scope?problem:null;
 const visible=result?.scope===scope&&result.arn===arn&&result.include===include?result:null;
 async function load(next='',page=1){
  clear();
  if(!active)return;
  const name=physicalName(region,arn);
  if(name===null){setError({kind:'arn',scope});return}
  const ticket=epoch.current,selectedScope=scope,selectedARN=arn,selectedInclude=include;
  const controller=new AbortController();pending.current=controller;setLoading(true);
  const current=()=>alive.current&&ticket===epoch.current&&scopeNow.current===selectedScope&&!controller.signal.aborted;
  try{
   const response=await request('/',{method:'POST',signal:controller.signal,headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'secretsmanager.ListSecretVersionIds','X-Simulator-Service':'secretsmanager','X-Simulator-Region':region},body:JSON.stringify({SecretId:selectedARN,IncludeDeprecated:selectedInclude,MaxResults:pageSize,...(next?{NextToken:next}:{})})});
   const data=await response.json();if(!current())return;
   const parsed=metadataPage(data,selectedARN,name);
   if(!parsed){setError({kind:'response',scope:selectedScope});return}
   setResult({scope:selectedScope,arn:selectedARN,include:selectedInclude,items:parsed.items,next:parsed.next,page});
  }catch(e){if(current())setError({kind:'native',scope:selectedScope,message:typeof e?.message==='string'?e.message:''})}
  finally{if(current()){pending.current=null;setLoading(false)}}
 }
 const errorText=error?.kind==='arn'?t('Enter the full local secret ARN for the selected region.','選択中のリージョンの完全なローカルシークレット ARN を入力してください。'):error?.kind==='response'?t('Version metadata did not match the selected secret.','バージョンメタデータが選択したシークレットと一致しません。'):error?.message||t('Version metadata could not be loaded.','バージョンメタデータを読み込めませんでした。');
 return <div data-secret-versions><Container header={<Header variant="h2">{t('Version metadata','バージョンメタデータ')}</Header>}><SpaceBetween size="m">
  <Alert type="info">{t('Metadata only: no secret values are requested. This is a live page, not a complete history snapshot. Label changes or concurrent insertions can change later pages.','メタデータのみを表示し、シークレット値は要求しません。これは現在のページであり、履歴全体のスナップショットではありません。ラベル変更や同時追加により後続ページが変わる場合があります。')}</Alert>
  <form onSubmit={event=>{event.preventDefault();load()}}><SpaceBetween size="m">
   <FormField label={t('Secret ARN for version metadata','バージョンメタデータのシークレット ARN')} description={t('Enter a saved physical ARN. ListSecretVersionIds permission is sufficient; GetSecretValue is not needed.','保存された物理 ARN を入力します。ListSecretVersionIds 権限のみで利用でき、GetSecretValue 権限は不要です。')}><Input value={arn} disabled={!active} autoComplete="off" onChange={({detail})=>{clear();setARN(detail.value)}}/></FormField>
   <Checkbox checked={include} disabled={!active} onChange={({detail})=>{clear();setInclude(detail.checked)}}>{t('Include deprecated versions','非推奨バージョンを含める')}</Checkbox>
   <SpaceBetween direction="horizontal" size="xs"><Button formAction="submit" variant="primary" disabled={!active||loading}>{t('List versions','バージョンを一覧表示')}</Button><Button formAction="none" disabled={!active||loading} onClick={()=>load()}>{t('Refresh versions','バージョンを更新')}</Button>{loading&&<Button formAction="none" onClick={clear}>{t('Cancel version request','バージョンのリクエストをキャンセル')}</Button>}</SpaceBetween>
  </SpaceBetween></form>
  {error&&<Alert type="error" header={t('Version metadata unavailable','バージョンメタデータを取得できません')} action={<Button formAction="none" disabled={!active||loading} onClick={()=>load()}>{t('Retry first page','最初のページを再試行')}</Button>}>{errorText}</Alert>}
  <Table items={visible?.items||[]} trackBy="id" loading={loading} loadingText={t('Loading version metadata','バージョンメタデータを読み込み中')} wrapLines ariaLabels={{tableLabel:t('Secret version metadata','シークレットのバージョンメタデータ')}} columnDefinitions={[{id:'id',header:t('Version ID','バージョン ID'),cell:item=>identifier(item.id)},{id:'stages',header:t('Staging labels','ステージングラベル'),cell:item=>item.stages.length?item.stages.join(', '):t('Deprecated (no staging labels)','非推奨（ステージングラベルなし）')},{id:'created',header:t('Created date','作成日時'),cell:item=>identifier(item.created)}]} header={<Header variant="h3">{t('Versions','バージョン')}{visible?' — '+t('Page ','ページ ')+visible.page:''}</Header>} empty={<Box>{visible?t('No versions on this live page.','この現在のページにバージョンはありません。'):t('Choose List versions to request metadata.','「バージョンを一覧表示」を選択してメタデータを要求してください。')}</Box>}/>
  <SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={!visible?.next||loading||!active} onClick={()=>load(visible.next,visible.page+1)}>{t('Next page','次のページ')}</Button><Box>{t('Up to 20 versions per page. Refresh starts a new live list.','1 ページあたり最大 20 バージョンです。更新すると、新しい現在の一覧を開始します。')}</Box></SpaceBetween>
 </SpaceBetween></Container></div>
}
