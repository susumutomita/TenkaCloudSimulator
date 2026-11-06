import React,{useEffect,useMemo,useRef,useState} from 'react';
import {useLocale} from './i18n.jsx';
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
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import Toggle from '@cloudscape-design/components/toggle';

// Generic console for the model-driven catalog. Every request uses the
// service's actual native wire protocol; the descriptor only says how.
export function buildCatalogRequest(service,op,input,region){
 if(service.protocol==='json')return {path:'/',options:{method:'POST',headers:{'Content-Type':'application/x-amz-json-'+service.jsonVersion,'X-Amz-Target':service.targetPrefix+'.'+op.name,'X-Simulator-Region':region},body:JSON.stringify(input)}};
 let [path,queryText]=op.uri.split('?');const query=new URLSearchParams(queryText||'');const body={};
 const headers={'X-Simulator-Service':service.signingName,'X-Simulator-Region':region};
 for(const member of op.members){
  if(!(member.name in input))continue;const value=input[member.name];const wire=member.locationName||member.name;
  if(member.location==='uri')path=path.replace('{'+wire+'+}',String(value).split('/').map(encodeURIComponent).join('/')).replace('{'+wire+'}',encodeURIComponent(String(value)));
  else if(member.location==='querystring')(Array.isArray(value)?value:[value]).forEach(item=>query.append(wire,String(item)));
  else if(member.location==='header')headers[wire]=String(value);
  else body[member.name]=value;
 }
 const search=query.toString();const hasBody=Object.keys(body).length>0;
 if(hasBody)headers['Content-Type']='application/json';
 return {path:path+(search?'?'+search:''),options:{method:op.method,headers,...(hasBody?{body:JSON.stringify(body)}:{})}};
}

// Only simple official members are editable in the form; complex members
// remain available to SDK clients and the API reference.
export function formMembers(op,skip){
 return (op?.members||[]).filter(m=>!skip.has(m.name)&&(['string','integer','long','boolean'].includes(m.type)||m.type==='list'&&m.element==='string'));
}

export function convertField(member,text){
 if(member.type==='boolean')return text===true;
 if(member.type==='integer'||member.type==='long'){const n=Number(text);if(!Number.isInteger(n))throw Error(member.name+' must be an integer.');return n}
 if(member.type==='list')return String(text).split(',').map(s=>s.trim()).filter(Boolean);
 return String(text);
}

const isScalar=v=>v===null||['string','number','boolean'].includes(typeof v);
const display=v=>typeof v==='number'&&v>1e9&&v<1e11?new Date(v*1000).toISOString():String(v);

export default function CatalogView({region,request,catalog,onSaving}){
 const {t}=useLocale();
 const services=catalog?.services||[];
 const [model,setModel]=useState(services[0]?.model||''),[kind,setKind]=useState('');
 const [parents,setParents]=useState([]),[parent,setParent]=useState(''),[items,setItems]=useState([]),[next,setNext]=useState('');
 const [detail,setDetail]=useState(null),[tags,setTags]=useState(null),[mode,setMode]=useState('list'),[form,setForm]=useState({});
 const [loading,setLoading]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState(''),[confirm,setConfirm]=useState(null);
 const revision=useRef(0),alive=useRef(true);
 const service=services.find(s=>s.model===model);
 const resource=service?.resources.find(r=>r.kind===kind)||service?.resources[0];
 const parentResource=resource?.parent?service.resources.find(r=>r.kind===resource.parent):null;
 const fresh=()=>++revision.current;const valid=epoch=>alive.current&&epoch===revision.current;
 useEffect(()=>()=>{alive.current=false;revision.current++},[]);
 async function call(op,input){
  const {path,options}=buildCatalogRequest(service,op,input,region);
  const response=await request(path,options);const text=await response.text();return text?JSON.parse(text):{};
 }
 const identity=item=>item?.[resource?.nameMember]||item?.[resource?.idMember]||item?.[resource?.arnMember]||'';
 const lookupValue=(item,op)=>{
  const member=op.lookup;if(member===resource.arnMember||/arn$/i.test(member))return item[resource.arnMember]||item[member];
  if(member===resource.idMember)return item[resource.idMember];return item[resource.nameMember]||item[member]||item[resource.idMember];
 };
 const parentInput=op=>op?.parent&&parent?{[op.parent]:parent}:{};
 async function loadParents(epoch){
  const list=parentResource?.operations.list;if(!list)return '';
  const page=await call(list,{});if(!valid(epoch))return '';
  const rows=page[list.items]||[];setParents(rows);
  const keep=rows.some(r=>(r[parentResource.idMember]||r[parentResource.nameMember])===parent)?parent:(rows[0]?.[parentResource.idMember]||rows[0]?.[parentResource.nameMember]||'');
  setParent(keep);return keep;
 }
 async function load(token=''){
  if(!service||!resource)return;const epoch=fresh();setLoading(true);setError('');setMode('list');setDetail(null);setTags(null);
  try{
   let scope=parent;if(parentResource){scope=await loadParents(epoch);if(!valid(epoch))return;if(!scope){setItems([]);setNext('');return}}
   const list=resource.operations.list;if(!list){setItems([]);return}
   const page=await call(list,{...(list.parent&&scope?{[list.parent]:scope}:{}),...(token&&list.inputToken?{[list.inputToken]:token}:{})});
   if(valid(epoch)){setItems(page[list.items]||[]);setNext(list.outputToken?page[list.outputToken]||'':'')}
  }catch(e){if(valid(epoch)){setItems([]);setError(e.message)}}finally{if(valid(epoch))setLoading(false)}
 }
 useEffect(()=>{load()},[model,kind,region]);
 async function inspect(item){
  const read=resource.operations.read;const epoch=fresh();setError('');setMode('detail');setTags(null);
  // Services without a read operation describe resources only through their
  // official list output, so the listed projection is the detail view.
  if(!read){setDetail(item);setLoading(false);return}
  setLoading(true);setDetail(null);
  try{
   const out=await call(read,{...parentInput(read),[read.lookup]:lookupValue(item,read)});if(!valid(epoch))return;
   const value=read.wrapper?out[read.wrapper]:out;setDetail(value);
   const arn=value?.[resource.arnMember];const listTags=service.tagging?.['list-tags'];
   if(arn&&listTags){try{const tagged=await call(listTags,{[listTags.lookup]:arn});if(valid(epoch))setTags(tagged.Tags||tagged.tags||null)}catch{/* Tag permission is independent of read permission. */}}
  }catch(e){if(valid(epoch))setError(e.message)}finally{if(valid(epoch))setLoading(false)}
 }
 async function create(){
  const op=resource.operations.create;fresh();setBusy(true);onSaving(true);setError('');setNotice('');
  let created=false;
  try{
   const input={...parentInput(op)};
   for(const member of formMembers(op,skipped(op))){const raw=form[member.name];if(raw===undefined||raw==='')continue;input[member.name]=convertField(member,raw)}
   await call(op,input);created=true;
  }catch(e){if(alive.current)setError(e.message)}finally{if(alive.current){setBusy(false);onSaving(false)}}
  // Reload only after an acknowledged write so a failure stays visible.
  if(created&&alive.current){await load();if(alive.current){setNotice(t('Resource created.','リソースを作成しました。'));setForm({})}}
 }
 async function remove(item){
  const op=resource.operations.delete;setConfirm(null);fresh();setBusy(true);onSaving(true);setError('');setNotice('');
  let removed=false;
  try{await call(op,{...parentInput(op),[op.lookup]:lookupValue(item,op)});removed=true}
  catch(e){if(alive.current)setError(e.message)}finally{if(alive.current){setBusy(false);onSaving(false)}}
  if(removed&&alive.current){await load();if(alive.current)setNotice(t('Resource deleted.','リソースを削除しました。'))}
 }
 const skipped=op=>new Set([op?.parent,'tags','Tags','ClientToken','clientToken','ClientRequestToken','clientRequestToken'].filter(Boolean));
 const createOp=resource?.operations.create;
 const fields=useMemo(()=>formMembers(createOp,skipped(createOp)),[createOp]);
 if(!services.length)return <Alert type="info">{t('No catalog services are enabled.','有効なカタログサービスはありません。')}</Alert>;
 const columns=[{id:'name',header:t('Name or identifier','名前または識別子'),cell:item=><Link onFollow={e=>{e.preventDefault();inspect(item)}} href="#">{item[resource.nameMember]||item[resource.idMember]||identity(item)}</Link>},
  {id:'arn',header:'ARN',cell:item=>item[resource.arnMember]||'-'}];
 return <ContentLayout header={<Header variant="h1" description={t('Bounded configuration metadata through each service\'s native API. Unlisted official operations return HTTP 501.','各サービスのネイティブ API で扱う、範囲を限定した設定メタデータです。一覧にない公式操作は HTTP 501 を返します。')}
   actions={<Link external href="/_simulator/docs/catalog.html">{t('API reference','API リファレンス')}</Link>}>{t('AWS service catalog','AWS サービスカタログ')}</Header>}>
  <SpaceBetween size="l">
   <FormField label={t('Catalog service','カタログサービス')}><Select selectedOption={{value:model,label:service?.title}} disabled={busy} onChange={e=>{setModel(e.detail.selectedOption.value);setKind('');setParent('');setParents([])}} options={services.map(s=>({value:s.model,label:s.title,description:s.operations.length+' / '+s.officialOperations+' '+t('official operations','公式操作')}))}/></FormField>
   {service.bounds.map(b=><Alert key={b} type="info">{b}</Alert>)}
   {error&&<Alert type="error" header={t('Request failed','リクエストに失敗しました')}>{error}</Alert>}
   {notice&&<Alert type="success" dismissible onDismiss={()=>setNotice('')}>{notice}</Alert>}
   <Tabs activeTabId={resource.kind} onChange={e=>{if(!busy){setKind(e.detail.activeTabId);setParent('')}}} tabs={service.resources.map(r=>({id:r.kind,label:r.kind}))}/>
   {parentResource&&<FormField label={parentResource.kind}><Select selectedOption={parent?{value:parent,label:parent}:null} placeholder={t('Create a parent resource first','先に親リソースを作成してください')} disabled={busy||!parents.length}
     onChange={e=>{setParent(e.detail.selectedOption.value);setTimeout(()=>load(),0)}} options={parents.map(p=>{const v=p[parentResource.idMember]||p[parentResource.nameMember];return {value:v,label:p[parentResource.nameMember]||v}})}/></FormField>}
   {mode==='detail'?<Container header={<Header variant="h2" actions={<Button onClick={()=>load()}>{t('Back to list','一覧に戻る')}</Button>}>{t('Details','詳細')}</Header>}>
     {loading?<Box>{t('Loading','読み込み中')}</Box>:detail?<SpaceBetween size="m"><KeyValuePairs columns={2} items={Object.entries(detail).filter(([,v])=>isScalar(v)).map(([k,v])=>({label:k,value:display(v)}))}/>
      {Object.entries(detail).filter(([,v])=>!isScalar(v)).map(([k,v])=><FormField key={k} label={k}><pre className="catalog-json">{JSON.stringify(v,null,2)}</pre></FormField>)}
      {tags&&<FormField label={t('Tags','タグ')}><pre className="catalog-json">{JSON.stringify(tags,null,2)}</pre></FormField>}</SpaceBetween>:null}
    </Container>:
    <Table loading={loading} loadingText={t('Loading resources','リソースを読み込み中')} items={items} columnDefinitions={[...columns,{id:'actions',header:t('Actions','アクション'),cell:item=>resource.operations.delete?<Button disabled={busy} ariaLabel={t('Delete','削除')+' '+identity(item)} onClick={()=>setConfirm(item)}>{t('Delete','削除')}</Button>:null}]}
     header={<Header counter={'('+items.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel={t('Refresh','更新')} disabled={busy} onClick={()=>load()}/>{next&&<Button disabled={busy} onClick={()=>load(next)}>{t('Next page','次のページ')}</Button>}</SpaceBetween>}>{resource.kind}</Header>}
     empty={<Box textAlign="center">{t('No resources','リソースはありません')}</Box>}/>}
   {createOp&&(!parentResource||parent)&&<Container header={<Header variant="h2">{t('Create','作成')} {resource.kind}</Header>}>
    <form onSubmit={e=>{e.preventDefault();create()}}><Form actions={<Button variant="primary" formAction="submit" loading={busy}>{t('Create','作成')}</Button>}><SpaceBetween size="m">
     {fields.map(member=>member.type==='boolean'?<Toggle key={member.name} checked={form[member.name]===true} onChange={e=>setForm(f=>({...f,[member.name]:e.detail.checked}))}>{member.name}</Toggle>
      :member.enum?<FormField key={member.name} label={member.name+(member.required?'':' ('+t('optional','任意')+')')}><Select selectedOption={form[member.name]?{value:form[member.name],label:form[member.name]}:null} placeholder={t('Choose','選択')} onChange={e=>setForm(f=>({...f,[member.name]:e.detail.selectedOption.value}))} options={member.enum.map(v=>({value:v,label:v}))}/></FormField>
      :<FormField key={member.name} label={member.name+(member.required?'':' ('+t('optional','任意')+')')} description={member.type==='list'?t('Comma-separated values','カンマ区切りの値'):undefined}><Input value={form[member.name]??''} onChange={e=>setForm(f=>({...f,[member.name]:e.detail.value}))} inputMode={member.type==='integer'||member.type==='long'?'numeric':undefined}/></FormField>)}
    </SpaceBetween></Form></form></Container>}
   <Modal visible={!!confirm} onDismiss={()=>setConfirm(null)} header={t('Delete resource','リソースを削除')} footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button onClick={()=>setConfirm(null)}>{t('Cancel','キャンセル')}</Button><Button variant="primary" onClick={()=>remove(confirm)}>{t('Delete','削除')}</Button></SpaceBetween></Box>}>
    {t('Delete','削除')} <strong>{identity(confirm||{})}</strong>?
   </Modal>
  </SpaceBetween>
 </ContentLayout>;
}
