import {useLocale} from './i18n.jsx';
import React,{useEffect,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import RadioGroup from '@cloudscape-design/components/radio-group';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';

const identifier=value=><span className="resource-identifier">{value??'Unavailable'}</span>;
const number=(value,min,max,label)=>{
 if(!/^\d+$/.test(value)||Number(value)<min||Number(value)>max)throw Error(`${label} must be an integer from ${min} to ${max}.`);
 return Number(value);
};
const queueName=url=>decodeURIComponent(url.split('/').filter(Boolean).at(-1)||'');
const defaults={name:'',visibility:'30',delay:'0',retention:'345600'};

// All authoritative queue/message state and IAM checks live in the native API.
// Receipt handles never enter React state, the DOM, URLs, logs or local storage.
export default function SQSView({region,request,onSaving}){
 const {t}=useLocale();
 const [view,setView]=useState('list'),[queues,setQueues]=useState([]),[queue,setQueue]=useState(null);
 const [selected,setSelected]=useState([]),[prefix,setPrefix]=useState(''),[activePrefix,setActivePrefix]=useState('');
 const [history,setHistory]=useState(['']),[next,setNext]=useState(''),[lookup,setLookup]=useState('');
 const [form,setForm]=useState(defaults),[body,setBody]=useState(''),[delay,setDelay]=useState('0');
 const [maxMessages,setMaxMessages]=useState('10'),[visibility,setVisibility]=useState('30'),[wait,setWait]=useState('0');
 const [messages,setMessages]=useState([]),[messageSelection,setMessageSelection]=useState([]),[sent,setSent]=useState(null);
 const [modal,setModal]=useState(''),[confirmation,setConfirmation]=useState(''),[newVisibility,setNewVisibility]=useState('0');
 const [loading,setLoading]=useState(false),[busy,setBusy]=useState(false),[polling,setPolling]=useState(false);
 const [error,setError]=useState(''),[notice,setNotice]=useState('');
 const alive=useRef(true),revision=useRef(0),abort=useRef(new AbortController());
 const receiptHandles=useRef(new Map()),pollAbort=useRef(null),mutating=useRef(false),pollActive=useRef(false);
 const valid=epoch=>alive.current&&epoch===revision.current;
 const clearMessages=()=>{receiptHandles.current.clear();setMessages([]);setMessageSelection([])};
 function invalidate(){
  revision.current++;pollAbort.current?.abort();pollAbort.current=null;pollActive.current=false;setPolling(false);
  return revision.current;
 }
 async function call(operation,input,signal=abort.current.signal){
  const response=await request('/',{method:'POST',signal,headers:{'Content-Type':'application/x-amz-json-1.0','X-Amz-Target':'AmazonSQS.'+operation,'X-Simulator-Service':'sqs','X-Simulator-Region':region},body:JSON.stringify(input)});
  const text=await response.text();return text?JSON.parse(text):{};
 }
 async function loadQueues(token='',tokens=[''],filter=activePrefix){
  if(mutating.current)return;
  const epoch=invalidate();clearMessages();setLoading(true);setError('');setSelected([]);setView('list');setQueue(null);setSent(null);setBody('');setDelay('0');
  try{
   const page=await call('ListQueues',{MaxResults:25,...(filter?{QueueNamePrefix:filter}:{}),...(token?{NextToken:token}:{})});
   if(!valid(epoch))return;
   // Listing permission does not imply GetQueueAttributes permission. Preserve
   // actual listed names while showing unavailable metadata rather than zeroes.
   const rows=await Promise.all((page.QueueUrls||[]).map(async url=>{
    try{return {name:queueName(url),url,attributes:(await call('GetQueueAttributes',{QueueUrl:url,AttributeNames:['All']})).Attributes}}
    catch(e){return {name:queueName(url),url,metadataError:e.message}}
   }));
   if(valid(epoch)){setQueues(rows);setNext(page.NextToken||'');setHistory(tokens);setActivePrefix(filter)}
  }catch(e){if(valid(epoch)){setQueues([]);setNext('');setError(e.message)}}
  finally{if(valid(epoch))setLoading(false)}
 }
 async function inspect(item,keepNotice=false){
  if(mutating.current)return;
  const epoch=invalidate();clearMessages();setSent(null);setBody('');setDelay('0');setError('');if(!keepNotice)setNotice('');setLoading(true);setView('details');setQueue({name:item.name,url:item.url,attributes:null});
  try{
   const resolved=await call('GetQueueUrl',{QueueName:item.name});
   if(!valid(epoch))return;
   const response=await call('GetQueueAttributes',{QueueUrl:resolved.QueueUrl,AttributeNames:['All']});
   if(valid(epoch))setQueue({name:item.name,url:resolved.QueueUrl,attributes:response.Attributes});
  }catch(e){if(valid(epoch))setError(e.message)}finally{if(valid(epoch))setLoading(false)}
 }
 useEffect(()=>{
  loadQueues();
  return()=>{alive.current=false;revision.current++;abort.current.abort();pollAbort.current?.abort();receiptHandles.current.clear();if(mutating.current)onSaving(false)};
 },[]);
 function changeView(nextView){
  if(mutating.current)return;invalidate();clearMessages();setLoading(false);setError('');setNotice('');setSent(null);setBody('');setDelay('0');setModal('');setView(nextView);
 }
 async function mutate(operation,input,apply){
  if(mutating.current)return;
  const epoch=invalidate();mutating.current=true;setBusy(true);onSaving(true);setError('');setNotice('');setSent(null);
  let result,acknowledged=false;
  try{result=await call(operation,input);acknowledged=true}
  catch(e){if(valid(epoch))setError(e.message+' Refresh state before retrying; an interrupted request may have completed.')}
  finally{if(valid(epoch)){mutating.current=false;setBusy(false);onSaving(false)}}
  if(acknowledged&&valid(epoch))apply(result);
 }
 function create(event){
  event.preventDefault();
  try{
   if(!/^[A-Za-z0-9_-]{1,80}$/.test(form.name))throw Error('Name must contain 1–80 letters, digits, hyphens or underscores. FIFO queues are unsupported.');
   // Translate only canonical notices created by this view; API errors and data remain untouched.
 const noticeLabels=new Map([
  ["Queue created.",t("Queue created.","キューを作成しました。")],
  ["Message sent.",t("Message sent.","メッセージを送信しました。")],
  ["No messages returned by this poll.",t("No messages returned by this poll.","今回のポーリングではメッセージを受信しませんでした。")],
  ["Polling stopped locally. A server claim may already have completed; refresh queue details before retrying.",t("Polling stopped locally. A server claim may already have completed; refresh queue details before retrying.","ローカルでポーリングを停止しました。サーバーでの受信処理が完了している場合があります。再試行前にキューの詳細を更新してください。")],
  ["Queue deleted.",t("Queue deleted.","キューを削除しました。")],
  ["Message deleted.",t("Message deleted.","メッセージを削除しました。")],
  ["Message released. Poll again for a new receipt.",t("Message released. Poll again for a new receipt.","メッセージを解放しました。再度ポーリングして新しい受信ハンドルを取得してください。")],
  ["Message visibility changed.",t("Message visibility changed.","メッセージの可視性を変更しました。")]
 ]);
 const receivedNotice=/^(\d+) message\(s\) received\.$/.exec(notice);
 const renderedNotice=receivedNotice?receivedNotice[1]+t(' message(s) received.',' 件のメッセージを受信しました。'):(noticeLabels.get(notice)??notice);
 const attrs={VisibilityTimeout:String(number(form.visibility,0,43200,t("Visibility timeout","可視性タイムアウト"))),DelaySeconds:String(number(form.delay,0,900,t("Delivery delay","配信遅延"))),MessageRetentionPeriod:String(number(form.retention,60,1209600,t("Message retention period","メッセージ保持期間")))};
   mutate('CreateQueue',{QueueName:form.name,Attributes:attrs},result=>{setNotice('Queue created.');inspect({name:form.name,url:result.QueueUrl},true)});
  }catch(e){setError(e.message)}
 }
 function send(event){
  event.preventDefault();
  try{
   const bytes=new TextEncoder().encode(body).length;if(bytes<1||bytes>262144)throw Error('Message body must contain 1–262144 UTF-8 bytes (local simulator limit).');
   const delaySeconds=number(delay,0,900,t("Delivery delay","配信遅延"));
   mutate('SendMessage',{QueueUrl:queue.url,MessageBody:body,DelaySeconds:delaySeconds},result=>{setSent({id:result.MessageId,md5:result.MD5OfMessageBody});setBody('');setNotice('Message sent.')});
  }catch(e){setError(e.message)}
 }
 async function poll(event){
  event.preventDefault();if(mutating.current||pollActive.current)return;
  let input;
  try{input={QueueUrl:queue.url,MaxNumberOfMessages:number(maxMessages,1,10,t("Maximum messages","最大メッセージ数")),VisibilityTimeout:number(visibility,0,43200,t("Visibility timeout","可視性タイムアウト")),WaitTimeSeconds:number(wait,0,20,'Polling duration'),MessageSystemAttributeNames:['All'],MessageAttributeNames:['All']}}
  catch(e){setError(e.message);return}
  const epoch=invalidate();clearMessages();setNotice('');setError('');setSent(null);setLoading(false);setPolling(true);pollActive.current=true;
  const controller=new AbortController();pollAbort.current=controller;
  try{
   const response=await call('ReceiveMessage',input,controller.signal);
   if(!valid(epoch))return;
   const received=response.Messages||[];const handles=new Map();
   const rows=received.map(message=>{
    if(typeof message.MessageId!=='string'||!message.MessageId||typeof message.Body!=='string'||typeof message.ReceiptHandle!=='string'||!message.ReceiptHandle)throw Error('The server did not return a usable message and receipt.');
    handles.set(message.MessageId,message.ReceiptHandle);
    return {id:message.MessageId,body:message.Body,attributes:message.Attributes||{},size:new TextEncoder().encode(message.Body).length,md5:message.MD5OfBody};
   });
   receiptHandles.current=handles;setMessages(rows);setNotice(rows.length?`${rows.length} message(s) received.`:'No messages returned by this poll.');
  }catch(e){if(valid(epoch))setError(e.message)}
  finally{if(valid(epoch)){pollActive.current=false;pollAbort.current=null;setPolling(false)}}
 }
 function stopPolling(){invalidate();clearMessages();setNotice('Polling stopped locally. A server claim may already have completed; refresh queue details before retrying.')}
 function openModal(kind){if(busy||polling)return;setConfirmation('');setNewVisibility('0');setError('');setNotice('');setModal(kind)}
 function deleteQueue(event){
  event.preventDefault();if(confirmation!=='delete'){setError('Enter delete to confirm.');return}
  const target=queue||selected[0];
  mutate('DeleteQueue',{QueueUrl:target.url},()=>{setModal('');setNotice('Queue deleted.');loadQueues()});
 }
 function messageAction(event){
  event.preventDefault();const selectedMessage=messageSelection[0];const handle=receiptHandles.current.get(selectedMessage?.id);
  if(!handle){setError('No current message receipt is available. Poll again.');return}
  let timeout;
  try{if(modal==='visibility')timeout=number(newVisibility,0,43200,t("Visibility timeout","可視性タイムアウト"))}
  catch(e){setError(e.message);return}
  const operation=modal==='visibility'?'ChangeMessageVisibility':'DeleteMessage';
  mutate(operation,{QueueUrl:queue.url,ReceiptHandle:handle,...(operation==='ChangeMessageVisibility'?{VisibilityTimeout:timeout}:{})},()=>{
   setModal('');setMessageSelection([]);
   if(operation==='DeleteMessage'||timeout===0){receiptHandles.current.delete(selectedMessage.id);setMessages(old=>old.filter(row=>row.id!==selectedMessage.id))}
   setNotice(operation==='DeleteMessage'?'Message deleted.':timeout===0?'Message released. Poll again for a new receipt.':'Message visibility changed.');
  });
 }
 const attrs=queue?.attributes||{};
 const alerts=<>{error&&<Alert type="error" header={t("Request could not be completed","リクエストを完了できませんでした")}>{error}</Alert>}{notice&&<Alert type="success">{renderedNotice}</Alert>}</>;
 const back=<Button formAction="none" disabled={busy} onClick={()=>loadQueues()}>{t("Queues","キュー")}</Button>;
 const messagesAction=()=>{const selectedQueue=queue||selected[0];setQueue(selectedQueue);changeView('messages');setVisibility(selectedQueue.attributes?.VisibilityTimeout??'30');setDelay(selectedQueue.attributes?.DelaySeconds??'0')};
 const tableEmpty=<Box textAlign="center" padding="l"><SpaceBetween size="m"><Box variant="h2">{t("No queues","キューがありません")}</Box><Box>{t("Create a Standard queue or change the name prefix.","スタンダードキューを作成するか、名前のプレフィックスを変更してください。")}</Box></SpaceBetween></Box>;
 let content;
 if(view==='create')content=<ContentLayout header={<Header variant="h1">{t("Create queue","キューを作成")}</Header>}><form onSubmit={create}><Form errorText={error} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>loadQueues()}>{t("Cancel","キャンセル")}</Button><Button variant="primary" formAction="submit" loading={busy}>{t("Create queue","キューを作成")}</Button></SpaceBetween>}><SpaceBetween size="l"><Container header={<Header variant="h2">{t("Details","詳細")}</Header>}><SpaceBetween size="l"><FormField label={t("Type","種類")}><RadioGroup value="standard" items={[{value:'standard',label:t("Standard","スタンダード"),description:t("Supported local queue type.","対応しているローカルキューの種類です。")},{value:'fifo',label:'FIFO',disabled:true,description:t("Not implemented by this simulator.","このシミュレーターでは未対応です。")}]}/></FormField><FormField label={t("Name","名前")}><Input value={form.name} onChange={({detail})=>setForm({...form,name:detail.value})} autoFocus/></FormField></SpaceBetween></Container><Container header={<Header variant="h2">{t("Configuration","設定")}</Header>}><SpaceBetween size="l">{[['visibility',t("Visibility timeout (seconds)","可視性タイムアウト（秒）"),t("0–43200 seconds","0～43200 秒")],['retention',t("Message retention period (seconds)","メッセージ保持期間（秒）"),t("60–1209600 seconds","60～1209600 秒")],['delay',t("Delivery delay (seconds)","配信遅延（秒）"),t("0–900 seconds","0～900 秒")]].map(([key,label,description])=><FormField key={key} label={label} description={description}><Input value={form[key]} inputMode="numeric" onChange={({detail})=>setForm({...form,[key]:detail.value})}/></FormField>)}</SpaceBetween></Container><Alert type="info">{t("Local simulator subset: encryption/KMS, access policies, redrive, tags, FIFO and editing queue settings are unavailable. Messages are stored locally; encryption is not implemented. Maximum message size is fixed at 256 KiB.","このローカルシミュレーターでは、暗号化／KMS、アクセスポリシー、再処理、タグ、FIFO、キュー設定の編集は未対応です。メッセージは暗号化せずローカルに保存します。メッセージサイズの上限は 256 KiB 固定です。")}</Alert></SpaceBetween></Form></form></ContentLayout>;
 else if(view==='details')content=<ContentLayout header={<Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs">{back}<Button disabled={busy||loading||!queue?.url} onClick={messagesAction}>{t("Send and receive messages","メッセージの送受信")}</Button><Button disabled={busy||loading||!queue?.url} onClick={()=>openModal('queue')}>{t("Delete","削除")}</Button></SpaceBetween>}>{identifier(queue?.name)}</Header>}><SpaceBetween size="l">{alerts}<Container header={<Header variant="h2" actions={<Button formAction="none" loading={loading} disabled={busy} iconName="refresh" onClick={()=>inspect(queue,true)}>{t("Refresh queue details","キューの詳細を更新")}</Button>}>{t("Details","詳細")}</Header>}><KeyValuePairs columns={2} items={[{label:t("Type","種類"),value:t("Standard","スタンダード")},{label:t("Queue URL","キュー URL"),value:identifier(queue?.url)},{label:'ARN',value:identifier(attrs.QueueArn??t("Unavailable","取得不可"))},{label:t("Created","作成日時"),value:attrs.CreatedTimestamp?new Date(Number(attrs.CreatedTimestamp)*1000).toLocaleString():t("Unavailable","取得不可")},{label:t("Messages available","利用可能なメッセージ"),value:attrs.ApproximateNumberOfMessages??t("Unavailable","取得不可")},{label:t("Messages in flight","処理中のメッセージ"),value:attrs.ApproximateNumberOfMessagesNotVisible??t("Unavailable","取得不可")},{label:t("Visibility timeout","可視性タイムアウト"),value:attrs.VisibilityTimeout!==undefined?attrs.VisibilityTimeout+t(" seconds"," 秒"):t("Unavailable","取得不可")},{label:t("Message retention period","メッセージ保持期間"),value:attrs.MessageRetentionPeriod!==undefined?attrs.MessageRetentionPeriod+t(" seconds"," 秒"):t("Unavailable","取得不可")},{label:t("Delivery delay","配信遅延"),value:attrs.DelaySeconds!==undefined?attrs.DelaySeconds+t(" seconds"," 秒"):t("Unavailable","取得不可")}]}/></Container><Alert type="info">{t("Values reflect the last native API read. Queue edits, encryption, policies, DLQs, triggers and purge are not implemented.","値は最後の API 読み取り結果です。キューの編集、暗号化、ポリシー、DLQ、トリガー、一括消去は未対応です。")}</Alert></SpaceBetween></ContentLayout>;
 else if(view==='messages')content=<ContentLayout header={<Header variant="h1" description={queue?.name} actions={<SpaceBetween direction="horizontal" size="xs">{back}<Button formAction="none" disabled={busy} onClick={()=>inspect(queue)}>{t("Queue details","キューの詳細")}</Button></SpaceBetween>}>{t("Send and receive messages","メッセージの送受信")}</Header>}><SpaceBetween size="l">{alerts}<Container header={<Header variant="h2">{t("Send message","メッセージを送信")}</Header>}><form onSubmit={send}><Form actions={<Button variant="primary" formAction="submit" loading={busy} disabled={polling}>{t("Send message","メッセージを送信")}</Button>}><SpaceBetween size="l"><FormField label={t("Message body","メッセージ本文")} description={t("1–262144 UTF-8 bytes (local simulator limit).","UTF-8 で 1～262144 バイト（ローカルシミュレーターの上限）。")}><Textarea value={body} rows={5} onChange={({detail})=>setBody(detail.value)}/></FormField><FormField label={t("Delivery delay (seconds)","配信遅延（秒）")}><Input value={delay} inputMode="numeric" onChange={({detail})=>setDelay(detail.value)}/></FormField>{sent&&<KeyValuePairs columns={1} items={[{label:t("Message ID","メッセージ ID"),value:identifier(sent.id)},{label:t("MD5 of message body","メッセージ本文の MD5"),value:identifier(sent.md5)}]}/>}</SpaceBetween></Form></form></Container><Container header={<Header variant="h2">{t("Receive messages","メッセージを受信")}</Header>}><form onSubmit={poll}><Form actions={<SpaceBetween direction="horizontal" size="xs">{polling&&<Button formAction="none" onClick={stopPolling}>{t("Stop polling","ポーリングを停止")}</Button>}<Button variant="primary" formAction="submit" loading={polling} disabled={busy}>{t("Poll for messages","メッセージをポーリング")}</Button></SpaceBetween>}><SpaceBetween size="l">{[[maxMessages,setMaxMessages,t("Maximum messages","最大メッセージ数"),'1–10'],[visibility,setVisibility,t("Visibility timeout (seconds)","可視性タイムアウト（秒）"),'0–43200'],[wait,setWait,t("Polling duration (seconds)","ポーリング時間（秒）"),'0–20']].map(([value,change,label,description])=><FormField key={label} label={label} description={description}><Input value={value} inputMode="numeric" onChange={({detail})=>change(detail.value)}/></FormField>)}<Alert type="info">{t("Polling uses ReceiveMessage and changes message visibility. It never deletes messages automatically. Leaving this page clears local receipts; a server claim may still finish. Choose visibility 0 to release immediately.","ポーリングは ReceiveMessage を使い、メッセージの可視性を変更します。メッセージは自動削除しません。この画面を離れるとローカルの受信ハンドルは消去されますが、サーバーでの受信処理が完了する場合があります。すぐに解放するには可視性を 0 にしてください。")}</Alert></SpaceBetween></Form></form></Container><Table variant="container" trackBy="id" items={messages} selectedItems={messageSelection} selectionType="single" onSelectionChange={({detail})=>setMessageSelection(detail.selectedItems)} ariaLabels={{selectionGroupLabel:t("Received messages","受信したメッセージ"),itemSelectionLabel:(_,item)=>t("Select message ","メッセージを選択 ")+item.id}} header={<Header variant="h2" counter={'('+messages.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={busy||polling||!messageSelection.length} onClick={()=>openModal('visibility')}>{t("Change visibility","可視性を変更")}</Button><Button disabled={busy||polling||!messageSelection.length} onClick={()=>openModal('message')}>{t("Delete message","メッセージを削除")}</Button></SpaceBetween>}>{t("Messages","メッセージ")}</Header>} columnDefinitions={[{id:'id',header:t("Message ID","メッセージ ID"),cell:item=>identifier(item.id)},{id:'sent',header:t("Sent date","送信日時"),cell:item=>item.attributes.SentTimestamp?new Date(Number(item.attributes.SentTimestamp)).toLocaleString():t("Unavailable","取得不可")},{id:'size',header:t("Size","サイズ"),cell:item=>item.size+' B'},{id:'received',header:t("Receive count","受信回数"),cell:item=>item.attributes.ApproximateReceiveCount??t("Unavailable","取得不可")}]} empty={<Box textAlign="center" padding="l">{t("No messages from the current poll.","今回のポーリングで受信したメッセージはありません。")}</Box>}/>{messageSelection[0]&&<Container header={<Header variant="h2">{t("Message details","メッセージの詳細")}</Header>}><FormField label={t("Received message body","受信したメッセージ本文")}><Textarea readOnly rows={6} value={messageSelection[0].body}/></FormField></Container>}</SpaceBetween></ContentLayout>;
 else content=<ContentLayout header={<Header variant="h1" counter={'('+queues.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel={t("Refresh queues","キュー一覧を更新")} loading={loading} onClick={()=>loadQueues(history.at(-1),history)}> </Button><Button disabled={!selected.length||loading} onClick={messagesAction}>{t("Send and receive messages","メッセージの送受信")}</Button><Button disabled={!selected.length||loading} onClick={()=>openModal('queue')}>{t("Delete","削除")}</Button><Button variant="primary" onClick={()=>{changeView('create');setForm(defaults)}}>{t("Create queue","キューを作成")}</Button></SpaceBetween>}>{t("Queues","キュー")}</Header>}><SpaceBetween size="l">{alerts}<Alert type="info">{t("Independent local SQS simulator. Standard queues only; no actual AWS access. Supported controls call the same nine native APIs as SDK clients.","独立したローカル SQS シミュレーターです。スタンダードキューのみ対応し、実際の AWS には接続しません。画面の操作は SDK クライアントと同じ 9 つの API を呼び出します。")}</Alert><form onSubmit={event=>{event.preventDefault();setNotice('');loadQueues('',[''],prefix)}}><Form actions={<Button formAction="submit">{t("Apply prefix","プレフィックスを適用")}</Button>}><FormField label={t("Queue name prefix","キュー名のプレフィックス")}><Input value={prefix} onChange={({detail})=>setPrefix(detail.value)}/></FormField></Form></form><Table variant="container" loading={loading} trackBy="url" items={queues} selectedItems={selected} selectionType="single" onSelectionChange={({detail})=>setSelected(detail.selectedItems)} ariaLabels={{selectionGroupLabel:t("Queues","キュー"),itemSelectionLabel:(_,item)=>t("Select ","選択 ")+item.name}} columnDefinitions={[{id:'name',header:t("Name","名前"),cell:item=><Link href="#/sqs" onFollow={event=>{event.preventDefault();inspect(item)}}>{identifier(item.name)}</Link>},{id:'type',header:t("Type","種類"),cell:()=> t("Standard","スタンダード")},{id:'available',header:t("Messages available","利用可能なメッセージ"),cell:item=>item.attributes?.ApproximateNumberOfMessages??t("Unavailable","取得不可")},{id:'inflight',header:t("Messages in flight","処理中のメッセージ"),cell:item=>item.attributes?.ApproximateNumberOfMessagesNotVisible??t("Unavailable","取得不可")},{id:'created',header:t("Created","作成日時"),cell:item=>item.attributes?.CreatedTimestamp?new Date(Number(item.attributes.CreatedTimestamp)*1000).toLocaleString():t("Unavailable","取得不可")}]} empty={tableEmpty}/><SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={loading||history.length<2} onClick={()=>loadQueues(history.at(-2),history.slice(0,-1))}>{t("Previous page","前のページ")}</Button><Button formAction="none" disabled={loading||!next} onClick={()=>loadQueues(next,[...history,next])}>{t("Next page","次のページ")}</Button></SpaceBetween><Container header={<Header variant="h2">{t("Open a queue by name","名前でキューを開く")}</Header>}><form onSubmit={event=>{event.preventDefault();if(lookup)inspect({name:lookup})}}><Form actions={<Button formAction="submit">{t("Open queue","キューを開く")}</Button>}><FormField label={t("Queue name","キュー名")} description={t("Use an exact name when your IAM identity cannot list queues.","IAM ID にキュー一覧の権限がない場合は、正確な名前を指定してください。")}><Input value={lookup} onChange={({detail})=>setLookup(detail.value)}/></FormField></Form></form></Container></SpaceBetween></ContentLayout>;
 return <>{content}<Modal visible={!!modal} onDismiss={()=>{if(!busy)setModal('')}} header={modal==='queue'?t("Delete queue","キューを削除"):modal==='visibility'?t("Change message visibility","メッセージの可視性を変更"):t("Delete message","メッセージを削除")} closeAriaLabel={t("Close dialog","ダイアログを閉じる")}><form onSubmit={modal==='queue'?deleteQueue:messageAction}><Form errorText={error} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>setModal('')}>{t("Cancel","キャンセル")}</Button><Button variant="primary" formAction="submit" loading={busy}>{modal==='visibility'?t("Change visibility","可視性を変更"):t("Delete","削除")}</Button></SpaceBetween>}><SpaceBetween size="l">{modal==='queue'?<><Alert type="warning">{t("This deletes the queue and all messages, including messages in flight.","キューと、処理中を含むすべてのメッセージを削除します。")}</Alert><Box>{identifier(queue?.name||selected[0]?.name)}</Box><FormField label={t("Type delete to confirm","確認のため delete と入力")}><Input value={confirmation} onChange={({detail})=>setConfirmation(detail.value)}/></FormField></>:modal==='visibility'?<FormField label={t("New visibility timeout (seconds)","新しい可視性タイムアウト（秒）")} description={t("0 releases the message. Poll again to obtain a new receipt after release.","0 を指定するとメッセージを解放します。解放後は再度ポーリングして新しい受信ハンドルを取得してください。")}><Input value={newVisibility} inputMode="numeric" onChange={({detail})=>setNewVisibility(detail.value)}/></FormField>:<Box>{t("Delete the selected message using its current native receipt?","現在の受信ハンドルを使って、選択したメッセージを削除しますか？")}</Box>}</SpaceBetween></Form></form></Modal></>;
}
