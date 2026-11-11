import React,{useLayoutEffect,useMemo,useRef,useState} from 'react';
import {useLocale} from './i18n.jsx';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Textarea from '@cloudscape-design/components/textarea';

export function descriptionName(region,arn){
 const prefix=`arn:aws:secretsmanager:${region}:000000000000:secret:`;
 if(!arn.startsWith(prefix))return null;
 const tail=arn.slice(prefix.length);
 return /^[A-Za-z0-9/_+=.@-]{1,512}-[A-Za-z0-9]{6}$/.test(tail)?tail.slice(0,-7):null;
}
export function validDescription(value){
 return Array.from(value).length<=2048&&!/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value);
}
export function descriptionAck(data,arn,name){
 return !!data&&typeof data==='object'&&!Array.isArray(data)&&data.ARN===arn&&data.Name===name&&Object.keys(data).every(key=>key==='ARN'||key==='Name');
}

// No value API or token authority is exposed here. Each request captures the
// host's authenticated callback, region and physical ARN. Abort is not rollback.
export default function SecretDescription({region,request,secretARN='',active=true}){
 const {t}=useLocale();
 const scope=useMemo(()=>({region,request,secretARN,active}),[region,request,secretARN,active]);
 const now=useRef(scope),epoch=useRef(0),alive=useRef(false),pending=useRef(null),locked=useRef(false);
 const [arn,setARN]=useState(secretARN),[description,setDescription]=useState(''),[confirmed,setConfirmed]=useState(false),[busy,setBusy]=useState(false),[attempted,setAttempted]=useState(false),[status,setStatus]=useState(null),[metadata,setMetadata]=useState(null);
 useLayoutEffect(()=>{
  now.current=scope;alive.current=true;epoch.current++;locked.current=false;setARN(secretARN);setDescription('');setConfirmed(false);setBusy(false);setAttempted(false);setStatus(null);setMetadata(null);
  return()=>{alive.current=false;epoch.current++;pending.current?.abort();pending.current=null;locked.current=false};
 },[scope]);
 const name=descriptionName(region,arn),valid=validDescription(description);
 const visible=status?.scope===scope?status:null;
 function edit(setter,value){epoch.current++;pending.current?.abort();pending.current=null;setBusy(false);locked.current=false;setConfirmed(false);setStatus(null);setMetadata(null);setter(value)}
 async function execute(write){
  if(locked.current||!active||(write&&visible?.kind==='uncertain'))return;
  setAttempted(true);if(name===null||(write&&(!valid||!confirmed)))return;
  locked.current=true;setBusy(true);setMetadata(null);
  const selected=scope,id=arn,selectedName=name,ticket=++epoch.current,controller=new AbortController();pending.current=controller;
  const current=()=>alive.current&&now.current===selected&&epoch.current===ticket&&!controller.signal.aborted;
  const call=async(operation,input)=>{
   const response=await request('/',{method:'POST',signal:controller.signal,headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'secretsmanager.'+operation,'X-Simulator-Service':'secretsmanager','X-Simulator-Region':region},body:JSON.stringify(input)});
   return response.json();
  };
  let acknowledged=false;
  try{
   if(write){
    setStatus(null);
    const data=await call('UpdateSecret',{SecretId:id,Description:description});
    if(!current())return;
    if(!descriptionAck(data,id,selectedName))throw Error('Unexpected acknowledgment');
    acknowledged=true;setAttempted(false);setConfirmed(false);setStatus({scope:selected,kind:'saved'});
   }
   const data=await call('DescribeSecret',{SecretId:id});if(!current())return;
   if(!data||data.ARN!==id||data.Name!==selectedName||(data.Description!==undefined&&(typeof data.Description!=='string'||!validDescription(data.Description))))throw Error('Unexpected metadata');
   setMetadata({scope:selected,arn:id,description:data.Description||''});
   if(!write)setStatus(old=>old?.scope===selected&&['saved','refresh'].includes(old.kind)?{scope:selected,kind:'saved'}:{scope:selected,kind:'loaded'});
  }catch(error){
   if(current())setStatus(old=>({scope:selected,kind:acknowledged||(!write&&['saved','refresh'].includes(old?.kind))?'refresh':write||old?.kind==='uncertain'?'uncertain':'read'}));
  }finally{if(current()){locked.current=false;pending.current=null;setBusy(false)}}
 }
 const messages={
  saved:t('Description saved. No secret version was created.','説明を保存しました。シークレットのバージョンは作成されていません。'),
  refresh:t('Description saved, but metadata refresh failed. Retrying metadata refresh does not repeat the write. DescribeSecret permission is separate from UpdateSecret.','説明を保存しましたが、メタデータの再取得に失敗しました。再取得では書き込みを繰り返しません。DescribeSecret 権限は UpdateSecret とは別です。'),
  uncertain:t('The description update was not confirmed. It may have failed or may have been saved. Check metadata before submitting another update; no automatic retry was made.','説明の更新を確認できませんでした。失敗したか、保存済みの可能性があります。再送信する前にメタデータを確認してください。自動再試行は行っていません。'),
  read:t('Metadata could not be read. Check the ARN, region and DescribeSecret permission.','メタデータを取得できませんでした。ARN、リージョン、DescribeSecret 権限を確認してください。'),
  loaded:t('Current description loaded.','現在の説明を取得しました。')
 };
 return <div data-secret-description><Container header={<Header variant="h2">{t('Edit description','説明を編集')}</Header>}><SpaceBetween size="m">
  <Alert type="info">{t('Replace description metadata only. Empty text clears it. Values, KMS settings and rotation cannot be changed here. A full saved ARN and UpdateSecret permission are sufficient to save; listing and reading metadata require separate permissions.','説明メタデータのみを置き換えます。空欄にすると消去します。値、KMS 設定、ローテーションは変更できません。保存には保存済みの完全な ARN と UpdateSecret 権限が必要です。一覧表示とメタデータ取得には別の権限が必要です。')}</Alert>
  <form onSubmit={event=>{event.preventDefault();execute(true)}}><SpaceBetween size="m">
   <FormField label={t('Secret ARN for description','説明を更新するシークレット ARN')} errorText={attempted&&name===null?t('Enter the full local secret ARN for the selected region.','選択中のリージョンの完全なローカルシークレット ARN を入力してください。'):undefined}><Input value={arn} disabled={busy||!active} autoComplete="off" onChange={({detail})=>edit(setARN,detail.value)}/></FormField>
   <FormField label={t('New description','新しい説明')} constraintText={t('Up to 2048 Unicode characters. Empty text clears the description.','2048 文字以内。空欄にすると説明を消去します。')} errorText={attempted&&!valid?t('Enter valid Unicode text of at most 2048 characters.','有効な Unicode テキストを 2048 文字以内で入力してください。'):undefined}><Textarea value={description} disabled={busy||!active} onChange={({detail})=>edit(setDescription,detail.value)}/></FormField>
   <FormField errorText={attempted&&!confirmed?t('Confirm replacement before saving.','保存前に置き換えを確認してください。'):undefined}><Checkbox checked={confirmed} disabled={busy||!active} onChange={({detail})=>setConfirmed(detail.checked)}>{t('Replace the description of this exact ARN (empty text clears it).','この ARN の説明を置き換えます（空欄は消去）。')}</Checkbox></FormField>
   <SpaceBetween direction="horizontal" size="xs"><Button formAction="submit" variant="primary" disabled={busy||!active||visible?.kind==='uncertain'}>{t('Save description','説明を保存')}</Button><Button formAction="none" disabled={busy||!active} onClick={()=>execute(false)}>{t('Refresh description metadata','説明メタデータを再取得')}</Button></SpaceBetween>
  </SpaceBetween></form>
  {busy&&<div role="status">{t('Request in progress. Leaving this view does not undo an update.','リクエスト処理中です。この画面を離れても更新は取り消されません。')}</div>}
  {visible&&<Alert type={['saved','loaded'].includes(visible.kind)?'success':visible.kind==='refresh'?'warning':'error'}>{messages[visible.kind]}</Alert>}
  {metadata?.scope===scope&&metadata.arn===arn&&<FormField label={t('Current saved description','現在保存されている説明')}><Textarea readOnly value={metadata.description}/></FormField>}
 </SpaceBetween></Container></div>
}
