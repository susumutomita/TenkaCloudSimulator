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
   const attrs={VisibilityTimeout:String(number(form.visibility,0,43200,'Visibility timeout')),DelaySeconds:String(number(form.delay,0,900,'Delivery delay')),MessageRetentionPeriod:String(number(form.retention,60,1209600,'Message retention period'))};
   mutate('CreateQueue',{QueueName:form.name,Attributes:attrs},result=>{setNotice('Queue created.');inspect({name:form.name,url:result.QueueUrl},true)});
  }catch(e){setError(e.message)}
 }
 function send(event){
  event.preventDefault();
  try{
   const bytes=new TextEncoder().encode(body).length;if(bytes<1||bytes>262144)throw Error('Message body must contain 1–262144 UTF-8 bytes (local simulator limit).');
   const delaySeconds=number(delay,0,900,'Delivery delay');
   mutate('SendMessage',{QueueUrl:queue.url,MessageBody:body,DelaySeconds:delaySeconds},result=>{setSent({id:result.MessageId,md5:result.MD5OfMessageBody});setBody('');setNotice('Message sent.')});
  }catch(e){setError(e.message)}
 }
 async function poll(event){
  event.preventDefault();if(mutating.current||pollActive.current)return;
  let input;
  try{input={QueueUrl:queue.url,MaxNumberOfMessages:number(maxMessages,1,10,'Maximum messages'),VisibilityTimeout:number(visibility,0,43200,'Visibility timeout'),WaitTimeSeconds:number(wait,0,20,'Polling duration'),MessageSystemAttributeNames:['All'],MessageAttributeNames:['All']}}
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
  try{if(modal==='visibility')timeout=number(newVisibility,0,43200,'Visibility timeout')}
  catch(e){setError(e.message);return}
  const operation=modal==='visibility'?'ChangeMessageVisibility':'DeleteMessage';
  mutate(operation,{QueueUrl:queue.url,ReceiptHandle:handle,...(operation==='ChangeMessageVisibility'?{VisibilityTimeout:timeout}:{})},()=>{
   setModal('');setMessageSelection([]);
   if(operation==='DeleteMessage'||timeout===0){receiptHandles.current.delete(selectedMessage.id);setMessages(old=>old.filter(row=>row.id!==selectedMessage.id))}
   setNotice(operation==='DeleteMessage'?'Message deleted.':timeout===0?'Message released. Poll again for a new receipt.':'Message visibility changed.');
  });
 }
 const attrs=queue?.attributes||{};
 const alerts=<>{error&&<Alert type="error" header="Request could not be completed">{error}</Alert>}{notice&&<Alert type="success">{notice}</Alert>}</>;
 const back=<Button formAction="none" disabled={busy} onClick={()=>loadQueues()}>Queues</Button>;
 const messagesAction=()=>{const selectedQueue=queue||selected[0];setQueue(selectedQueue);changeView('messages');setVisibility(selectedQueue.attributes?.VisibilityTimeout??'30');setDelay(selectedQueue.attributes?.DelaySeconds??'0')};
 const tableEmpty=<Box textAlign="center" padding="l"><SpaceBetween size="m"><Box variant="h2">No queues</Box><Box>Create a Standard queue or change the name prefix.</Box></SpaceBetween></Box>;
 let content;
 if(view==='create')content=<ContentLayout header={<Header variant="h1">Create queue</Header>}><form onSubmit={create}><Form errorText={error} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>loadQueues()}>Cancel</Button><Button variant="primary" formAction="submit" loading={busy}>Create queue</Button></SpaceBetween>}><SpaceBetween size="l"><Container header={<Header variant="h2">Details</Header>}><SpaceBetween size="l"><FormField label="Type"><RadioGroup value="standard" items={[{value:'standard',label:'Standard',description:'Supported local queue type.'},{value:'fifo',label:'FIFO',disabled:true,description:'Not implemented by this simulator.'}]}/></FormField><FormField label="Name"><Input value={form.name} onChange={({detail})=>setForm({...form,name:detail.value})} autoFocus/></FormField></SpaceBetween></Container><Container header={<Header variant="h2">Configuration</Header>}><SpaceBetween size="l">{[['visibility','Visibility timeout (seconds)','0–43200 seconds'],['retention','Message retention period (seconds)','60–1209600 seconds'],['delay','Delivery delay (seconds)','0–900 seconds']].map(([key,label,description])=><FormField key={key} label={label} description={description}><Input value={form[key]} inputMode="numeric" onChange={({detail})=>setForm({...form,[key]:detail.value})}/></FormField>)}</SpaceBetween></Container><Alert type="info">Local simulator subset: encryption/KMS, access policies, redrive, tags, FIFO and editing queue settings are unavailable. Messages are stored locally; encryption is not implemented. Maximum message size is fixed at 256 KiB.</Alert></SpaceBetween></Form></form></ContentLayout>;
 else if(view==='details')content=<ContentLayout header={<Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs">{back}<Button disabled={busy||loading||!queue?.url} onClick={messagesAction}>Send and receive messages</Button><Button disabled={busy||loading||!queue?.url} onClick={()=>openModal('queue')}>Delete</Button></SpaceBetween>}>{identifier(queue?.name)}</Header>}><SpaceBetween size="l">{alerts}<Container header={<Header variant="h2" actions={<Button formAction="none" loading={loading} disabled={busy} iconName="refresh" onClick={()=>inspect(queue,true)}>Refresh queue details</Button>}>Details</Header>}><KeyValuePairs columns={2} items={[{label:'Type',value:'Standard'},{label:'Queue URL',value:identifier(queue?.url)},{label:'ARN',value:identifier(attrs.QueueArn)},{label:'Created',value:attrs.CreatedTimestamp?new Date(Number(attrs.CreatedTimestamp)*1000).toLocaleString():'Unavailable'},{label:'Messages available',value:attrs.ApproximateNumberOfMessages??'Unavailable'},{label:'Messages in flight',value:attrs.ApproximateNumberOfMessagesNotVisible??'Unavailable'},{label:'Visibility timeout',value:attrs.VisibilityTimeout!==undefined?attrs.VisibilityTimeout+' seconds':'Unavailable'},{label:'Message retention period',value:attrs.MessageRetentionPeriod!==undefined?attrs.MessageRetentionPeriod+' seconds':'Unavailable'},{label:'Delivery delay',value:attrs.DelaySeconds!==undefined?attrs.DelaySeconds+' seconds':'Unavailable'}]}/></Container><Alert type="info">Values reflect the last native API read. Queue edits, encryption, policies, DLQs, triggers and purge are not implemented.</Alert></SpaceBetween></ContentLayout>;
 else if(view==='messages')content=<ContentLayout header={<Header variant="h1" description={queue?.name} actions={<SpaceBetween direction="horizontal" size="xs">{back}<Button formAction="none" disabled={busy} onClick={()=>inspect(queue)}>Queue details</Button></SpaceBetween>}>Send and receive messages</Header>}><SpaceBetween size="l">{alerts}<Container header={<Header variant="h2">Send message</Header>}><form onSubmit={send}><Form actions={<Button variant="primary" formAction="submit" loading={busy} disabled={polling}>Send message</Button>}><SpaceBetween size="l"><FormField label="Message body" description="1–262144 UTF-8 bytes (local simulator limit)."><Textarea value={body} rows={5} onChange={({detail})=>setBody(detail.value)}/></FormField><FormField label="Delivery delay (seconds)"><Input value={delay} inputMode="numeric" onChange={({detail})=>setDelay(detail.value)}/></FormField>{sent&&<KeyValuePairs columns={1} items={[{label:'Message ID',value:identifier(sent.id)},{label:'MD5 of message body',value:identifier(sent.md5)}]}/>}</SpaceBetween></Form></form></Container><Container header={<Header variant="h2">Receive messages</Header>}><form onSubmit={poll}><Form actions={<SpaceBetween direction="horizontal" size="xs">{polling&&<Button formAction="none" onClick={stopPolling}>Stop polling</Button>}<Button variant="primary" formAction="submit" loading={polling} disabled={busy}>Poll for messages</Button></SpaceBetween>}><SpaceBetween size="l">{[[maxMessages,setMaxMessages,'Maximum messages','1–10'],[visibility,setVisibility,'Visibility timeout (seconds)','0–43200'],[wait,setWait,'Polling duration (seconds)','0–20']].map(([value,change,label,description])=><FormField key={label} label={label} description={description}><Input value={value} inputMode="numeric" onChange={({detail})=>change(detail.value)}/></FormField>)}<Alert type="info">Polling uses ReceiveMessage and changes message visibility. It never deletes messages automatically. Leaving this page clears local receipts; a server claim may still finish. Choose visibility 0 to release immediately.</Alert></SpaceBetween></Form></form></Container><Table variant="container" trackBy="id" items={messages} selectedItems={messageSelection} selectionType="single" onSelectionChange={({detail})=>setMessageSelection(detail.selectedItems)} ariaLabels={{selectionGroupLabel:'Received messages',itemSelectionLabel:(_,item)=>'Select message '+item.id}} header={<Header variant="h2" counter={'('+messages.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={busy||polling||!messageSelection.length} onClick={()=>openModal('visibility')}>Change visibility</Button><Button disabled={busy||polling||!messageSelection.length} onClick={()=>openModal('message')}>Delete message</Button></SpaceBetween>}>Messages</Header>} columnDefinitions={[{id:'id',header:'Message ID',cell:item=>identifier(item.id)},{id:'sent',header:'Sent date',cell:item=>item.attributes.SentTimestamp?new Date(Number(item.attributes.SentTimestamp)).toLocaleString():'Unavailable'},{id:'size',header:'Size',cell:item=>item.size+' B'},{id:'received',header:'Receive count',cell:item=>item.attributes.ApproximateReceiveCount??'Unavailable'}]} empty={<Box textAlign="center" padding="l">No messages from the current poll.</Box>}/>{messageSelection[0]&&<Container header={<Header variant="h2">Message details</Header>}><FormField label="Received message body"><Textarea readOnly rows={6} value={messageSelection[0].body}/></FormField></Container>}</SpaceBetween></ContentLayout>;
 else content=<ContentLayout header={<Header variant="h1" counter={'('+queues.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel="Refresh queues" loading={loading} onClick={()=>loadQueues(history.at(-1),history)}> </Button><Button disabled={!selected.length||loading} onClick={messagesAction}>Send and receive messages</Button><Button disabled={!selected.length||loading} onClick={()=>openModal('queue')}>Delete</Button><Button variant="primary" onClick={()=>{changeView('create');setForm(defaults)}}>Create queue</Button></SpaceBetween>}>Queues</Header>}><SpaceBetween size="l">{alerts}<Alert type="info">Independent local SQS simulator. Standard queues only; no actual AWS access. Supported controls call the same nine native APIs as SDK clients.</Alert><form onSubmit={event=>{event.preventDefault();setNotice('');loadQueues('',[''],prefix)}}><Form actions={<Button formAction="submit">Apply prefix</Button>}><FormField label="Queue name prefix"><Input value={prefix} onChange={({detail})=>setPrefix(detail.value)}/></FormField></Form></form><Table variant="container" loading={loading} trackBy="url" items={queues} selectedItems={selected} selectionType="single" onSelectionChange={({detail})=>setSelected(detail.selectedItems)} ariaLabels={{selectionGroupLabel:'Queues',itemSelectionLabel:(_,item)=>'Select '+item.name}} columnDefinitions={[{id:'name',header:'Name',cell:item=><Link href="#/sqs" onFollow={event=>{event.preventDefault();inspect(item)}}>{identifier(item.name)}</Link>},{id:'type',header:'Type',cell:()=> 'Standard'},{id:'available',header:'Messages available',cell:item=>item.attributes?.ApproximateNumberOfMessages??'Unavailable'},{id:'inflight',header:'Messages in flight',cell:item=>item.attributes?.ApproximateNumberOfMessagesNotVisible??'Unavailable'},{id:'created',header:'Created',cell:item=>item.attributes?.CreatedTimestamp?new Date(Number(item.attributes.CreatedTimestamp)*1000).toLocaleString():'Unavailable'}]} empty={tableEmpty}/><SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={loading||history.length<2} onClick={()=>loadQueues(history.at(-2),history.slice(0,-1))}>Previous page</Button><Button formAction="none" disabled={loading||!next} onClick={()=>loadQueues(next,[...history,next])}>Next page</Button></SpaceBetween><Container header={<Header variant="h2">Open a queue by name</Header>}><form onSubmit={event=>{event.preventDefault();if(lookup)inspect({name:lookup})}}><Form actions={<Button formAction="submit">Open queue</Button>}><FormField label="Queue name" description="Use an exact name when your IAM identity cannot list queues."><Input value={lookup} onChange={({detail})=>setLookup(detail.value)}/></FormField></Form></form></Container></SpaceBetween></ContentLayout>;
 return <>{content}<Modal visible={!!modal} onDismiss={()=>{if(!busy)setModal('')}} header={modal==='queue'?'Delete queue':modal==='visibility'?'Change message visibility':'Delete message'} closeAriaLabel="Close dialog"><form onSubmit={modal==='queue'?deleteQueue:messageAction}><Form errorText={error} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>setModal('')}>Cancel</Button><Button variant="primary" formAction="submit" loading={busy}>{modal==='visibility'?'Change visibility':'Delete'}</Button></SpaceBetween>}><SpaceBetween size="l">{modal==='queue'?<><Alert type="warning">This deletes the queue and all messages, including messages in flight.</Alert><Box>{identifier(queue?.name||selected[0]?.name)}</Box><FormField label="Type delete to confirm"><Input value={confirmation} onChange={({detail})=>setConfirmation(detail.value)}/></FormField></>:modal==='visibility'?<FormField label="New visibility timeout (seconds)" description="0 releases the message. Poll again to obtain a new receipt after release."><Input value={newVisibility} inputMode="numeric" onChange={({detail})=>setNewVisibility(detail.value)}/></FormField>:<Box>Delete the selected message using its current native receipt?</Box>}</SpaceBetween></Form></form></Modal></>;
}
