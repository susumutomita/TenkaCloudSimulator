import React,{useEffect,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';

const xml=text=>{const d=new DOMParser().parseFromString(text,'application/xml');if(d.querySelector('parsererror'))throw Error('Invalid XML response');return d;};
const value=(node,name)=>node.querySelector(name)?.textContent||'';
const escape=text=>String(text).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[c]));
export default function S3Multipart({bucket,region,request,onSaving,onComplete}){
 const {t}=useLocale();
 const [uploads,setUploads]=useState([]),[selected,setSelected]=useState([]),[parts,setParts]=useState([]);
 const [key,setKey]=useState(''),[number,setNumber]=useState('1'),[confirmation,setConfirmation]=useState('');
 const [busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState(''),[receipt,setReceipt]=useState(null);
 const file=useRef(null),fileInput=useRef(null),alive=useRef(true),revision=useRef(0),mutating=useRef(false),attempted=useRef(false),controller=useRef(new AbortController());
 const path=key=>{if(key==='.'||key==='..')throw Error(t('Use an SDK for standalone dot keys.','単独のドットキーにはSDKを使用してください。'));return '/'+bucket+'/'+encodeURIComponent(key)};
 const current=selected[0];
 const call=async(url,options={})=>{if(options.method&&options.method!=='GET')attempted.current=true;return request(url,{...options,signal:controller.current.signal,headers:{'X-Simulator-Region':region,...options.headers}})};
 function clearPartDraft(){file.current=null;if(fileInput.current)fileInput.current.value='';setNumber('1');setConfirmation('')}
 async function action(work,apply){
  if(mutating.current||!alive.current)return;mutating.current=true;attempted.current=false;const epoch=++revision.current;setBusy(true);onSaving(true);setError('');setNotice('');setReceipt(null);
  try{const result=await work();if(alive.current&&epoch===revision.current)apply(result)}
  catch(e){if(alive.current&&epoch===revision.current)setError(e.message+(attempted.current?t(' Refresh state before retrying; an interrupted request may have completed.',' 中断したリクエストが完了している可能性があるため、再試行前に状態を更新してください。'):''))}
  finally{mutating.current=false;if(alive.current){setBusy(false);onSaving(false)}}
 }
 const readUploads=async()=>{const d=xml(await(await call('/'+bucket+'?uploads')).text());if(value(d,'IsTruncated')==='true')throw Error(t('Upload list exceeded its local limit.','アップロード一覧がローカル上限を超えました。'));return [...d.querySelectorAll('Upload')].map(n=>({key:value(n,'Key'),id:value(n,'UploadId'),created:value(n,'Initiated')}))};
 async function readParts(upload){
  const result=[];let marker=0;
  do{const d=xml(await(await call(path(upload.key)+'?uploadId='+encodeURIComponent(upload.id)+'&part-number-marker='+marker)).text());
   result.push(...[...d.querySelectorAll('Part')].map(n=>({number:Number(value(n,'PartNumber')),etag:value(n,'ETag'),size:Number(value(n,'Size'))})));
   if(value(d,'IsTruncated')!=='true')return result;
   const next=Number(value(d,'NextPartNumberMarker'));if(next<=marker||result.length>=10000)throw Error('Invalid part cursor');marker=next;
  }while(alive.current);return [];
 }
 function refresh(){action(readUploads,rows=>{setUploads(rows);setSelected([]);setParts([]);clearPartDraft()})}
 useEffect(()=>{refresh();return()=>{alive.current=false;revision.current++;controller.current.abort();if(mutating.current)onSaving(false)}},[]);
 function inspect(item){action(()=>readParts(item),rows=>{setSelected([item]);setParts(rows);clearPartDraft()})}
 function start(){action(async()=>{
  if(!key||new TextEncoder().encode(key).length>1024)throw Error(t('Enter an object key up to 1024 UTF-8 bytes.','UTF-8で1024バイト以内のオブジェクトキーを入力してください。'));
  // Explicitly omit object cache metadata. Chromium may otherwise synthesize
  // Cache-Control: no-cache when its transport cache is disabled. This API
  // deliberately rejects nonempty, unsupported persisted metadata.
  const d=xml(await(await call(path(key)+'?uploads',{method:'POST',headers:{'Content-Type':'application/octet-stream','Cache-Control':''}})).text());return {key,id:value(d,'UploadId'),created:''};
 },item=>{setUploads(old=>[...old,item]);setSelected([item]);setParts([]);clearPartDraft();setNotice('started');setReceipt({key:item.key,id:item.id})})}
 function upload(){action(async()=>{
  const chosen=file.current;if(!chosen)throw Error(t('Choose a part file.','partファイルを選択してください。'));
  if(chosen.size>8388608)throw Error(t('Each part is limited to 8 MiB.','各partは8 MiB以内です。'));
  if(!/^[0-9]+$/.test(number)||Number(number)<1||Number(number)>10000)throw Error(t('Part number must be 1–10000.','part番号は1～10000です。'));
  const response=await call(path(current.key)+'?uploadId='+encodeURIComponent(current.id)+'&partNumber='+Number(number),{method:'PUT',body:chosen});return {rows:await readParts(current),etag:response.headers.get('ETag')};
 },result=>{setParts(result.rows);clearPartDraft();setNotice('part');setReceipt({key:current.key,id:current.id,etag:result.etag})})}
 function complete(){action(async()=>{
  if(confirmation!=='complete')throw Error(t('Type complete to confirm replacement.','置換を確認するため complete と入力してください。'));
  const actual=await readParts(current);if(!actual.length)throw Error(t('Upload at least one part.','少なくとも1つのpartを保存してください。'));
  if(JSON.stringify(actual)!==JSON.stringify(parts))return {changed:true,parts:actual};
  const body='<CompleteMultipartUpload>'+actual.map(p=>'<Part><PartNumber>'+p.number+'</PartNumber><ETag>'+escape(p.etag)+'</ETag></Part>').join('')+'</CompleteMultipartUpload>';
  const response=await call(path(current.key)+'?uploadId='+encodeURIComponent(current.id),{method:'POST',headers:{'Content-Type':'application/xml'},body});const result=xml(await response.text());return {rows:await readUploads(),etag:value(result,'ETag'),version:response.headers.get('x-amz-version-id')};
 },result=>{if(result.changed){setParts(result.parts);setConfirmation('');setError(t('Parts changed. Review the refreshed list and type complete again.','partが変更されました。更新された一覧を確認し、complete を再入力してください。'));return}setUploads(result.rows);setSelected([]);setParts([]);clearPartDraft();setNotice('complete');setReceipt({key:current.key,id:current.id,etag:result.etag,version:result.version});onComplete()})}
 function abort(){action(async()=>{
  if(confirmation!=='abort')throw Error(t('Type abort to confirm.','確認のため abort と入力してください。'));
  await call(path(current.key)+'?uploadId='+encodeURIComponent(current.id),{method:'DELETE'});return readUploads();
 },rows=>{setUploads(rows);setSelected([]);setParts([]);clearPartDraft();setNotice('abort');setReceipt({key:current.key,id:current.id})})}
 const messages={started:t('Multipart upload started.','multipart uploadを開始しました。'),part:t('Part saved.','partを保存しました。'),complete:t('Object completed.','オブジェクトを結合しました。'),abort:t('Upload aborted and parts removed.','アップロードを中止し、partを削除しました。')};
 return <SpaceBetween size="l"><Alert type="info">{t('Multipart data is stored locally until complete or abort. Completed objects are limited to 8 MiB; all but the final part must be at least 5 MiB. Completion replaces the key or creates a version. Active uploads block bucket deletion.','partは結合または中止までローカルに保存されます。結合後は8 MiB以内、最後以外のpartは5 MiB以上です。結合はキーの置換またはversion作成を行います。アップロード中のbucketは削除できません。')}</Alert>{error&&<Alert type="error">{error}</Alert>}{notice&&<Alert type="success"><SpaceBetween size="xs"><div>{messages[notice]}</div>{receipt&&<div className="resource-identifier">{receipt.key}<br/>Upload ID: {receipt.id}{receipt.etag&&<><br/>ETag: {receipt.etag}</>}{receipt.version&&<><br/>{t('Version ID','version ID')}: {receipt.version}</>}</div>}</SpaceBetween></Alert>}
 <Container header={<Header variant="h2">{t('Start multipart upload','multipart uploadを開始')}</Header>}><SpaceBetween size="m"><FormField label={t('Object key','オブジェクトキー')}><Input value={key} disabled={busy} onChange={({detail})=>setKey(detail.value)}/></FormField><Button disabled={busy} onClick={start}>{t('Start upload','アップロードを開始')}</Button></SpaceBetween></Container>
 <Table items={uploads} loading={busy} trackBy="id" selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>{if(!busy&&detail.selectedItems[0])inspect(detail.selectedItems[0])}} ariaLabels={{selectionGroupLabel:t('Multipart uploads','multipart uploads'),itemSelectionLabel:(_,item)=>t('Select upload ','アップロードを選択: ')+item.key}} header={<Header variant="h2" actions={<Button disabled={busy} onClick={refresh}>{t('Refresh uploads','アップロード一覧を更新')}</Button>}>{t('Multipart uploads','multipart uploads')}</Header>} columnDefinitions={[{id:'key',header:t('Object key','オブジェクトキー'),cell:i=><span className="resource-identifier">{i.key}</span>},{id:'id',header:'Upload ID',cell:i=><span className="resource-identifier">{i.id}</span>}]} empty={<Box>{t('No active uploads','進行中のアップロードはありません')}</Box>}/>
 {current&&<Container header={<Header variant="h2">{t('Upload parts','partを保存')}</Header>}><SpaceBetween size="m"><FormField label={t('Part number','part番号')}><Input value={number} inputMode="numeric" disabled={busy} onChange={({detail})=>setNumber(detail.value)}/></FormField><FormField label={t('Part file','partファイル')}><input key={current.id} ref={fileInput} aria-label={t('Part file','partファイル')} type="file" disabled={busy} onChange={event=>{file.current=event.target.files?.[0]||null}}/></FormField><Button disabled={busy} onClick={upload}>{t('Save part','partを保存')}</Button>
 <Table items={parts} trackBy="number" columnDefinitions={[{id:'number',header:t('Part number','part番号'),cell:i=>i.number},{id:'size',header:t('Size','サイズ'),cell:i=>i.size+' B'},{id:'etag',header:'ETag',cell:i=><span className="resource-identifier">{i.etag}</span>}]} empty={<Box>{t('No saved parts','保存されたpartはありません')}</Box>}/>
 <FormField label={t('Confirmation word','確認の文字列')} description={t('Type complete to combine every listed part in order, or abort to discard all parts. Refresh before retrying an interrupted mutation.','一覧の全partを順番に結合するには complete、中止して全partを破棄するには abort と入力してください。中断した操作を再試行する前に更新してください。')}><Input value={confirmation} disabled={busy} onChange={({detail})=>setConfirmation(detail.value)}/></FormField><SpaceBetween direction="horizontal" size="xs"><Button disabled={busy} onClick={complete}>{t('Complete upload','アップロードを結合')}</Button><Button disabled={busy} onClick={abort}>{t('Abort upload','アップロードを中止')}</Button></SpaceBetween></SpaceBetween></Container>}
 </SpaceBetween>;
}
