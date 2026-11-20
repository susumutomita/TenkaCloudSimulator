import React,{useEffect,useMemo,useRef,useState} from 'react';
import {useLocale} from './i18n.jsx';
import {decodeQuery,queryBody} from './catalog-query.mjs';
import {example,fromWire,parseComplex,parseTagLines,tagPairs,tagShape,tagValue,toWire} from './catalog-input.mjs';
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
import Textarea from '@cloudscape-design/components/textarea';
import Toggle from '@cloudscape-design/components/toggle';

// Generic console for the model-driven catalog. Every request uses the
// service's actual native wire protocol; the descriptor only says how.
// Inputs are keyed by official member names; the wire uses each member's
// serialized locationName, exactly as SDKs send it.
export function buildCatalogRequest(service,op,input,region){
 // Query: a form POST to / named by the signing scope, as botocore sends it.
 if(service.protocol==='query')return {path:'/',options:{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded; charset=utf-8','X-Simulator-Service':service.signingName,'X-Simulator-Region':region},body:queryBody(service,op,input)}};
 if(service.protocol==='json'){const body={};for(const member of op.members)if(member.name in input)body[member.locationName||member.name]=member.shape?toWire(service.inputShapes,member.shape,input[member.name]):input[member.name];
  return {path:'/',options:{method:'POST',headers:{'Content-Type':'application/x-amz-json-'+service.jsonVersion,'X-Amz-Target':service.targetPrefix+'.'+op.name,'X-Simulator-Region':region},body:JSON.stringify(body)}}}
 let [path,queryText]=op.uri.split('?');const query=new URLSearchParams(queryText||'');const body={};
 const headers={'X-Simulator-Service':service.signingName,'X-Simulator-Region':region};
 for(const member of op.members){
  if(!(member.name in input))continue;const value=input[member.name];const wire=member.locationName||member.name;
  if(member.location==='uri')path=path.replace('{'+wire+'+}',String(value).split('/').map(encodeURIComponent).join('/')).replace('{'+wire+'}',encodeURIComponent(String(value)));
  else if(member.location==='querystring')(Array.isArray(value)?value:[value]).forEach(item=>query.append(wire,String(item)));
  else if(member.location==='header')headers[wire]=String(value);
  else body[wire]=member.shape?toWire(service.inputShapes,member.shape,value):value;
 }
 const search=query.toString();const hasBody=Object.keys(body).length>0;
 if(hasBody)headers['Content-Type']='application/json';
 return {path:path+(search?'?'+search:''),options:{method:op.method,headers,...(hasBody?{body:JSON.stringify(body)}:{})}};
}

// Form members: scalars, string lists, and complex members whose official
// input shape the descriptor publishes (tags as key=value lines, other
// structures, maps and lists of structures as validated JSON values).
const SCALARS=['string','integer','long','float','double','boolean','timestamp','blob'];
export function formMembers(op,skip){
 return (op?.members||[]).filter(m=>!skip.has(m.name)&&(SCALARS.includes(m.type)||m.type==='list'&&m.element==='string'||!!m.shape));
}

// control names how a member is edited.
export function control(service,member){
 if(member.shape)return tagShape(service?.inputShapes,member.shape)?'tags':'json';
 if(member.type==='boolean')return 'toggle';
 return member.enum?'enum':'text';
}

export function convertField(member,text,service){
 if(member.shape){const form=tagShape(service?.inputShapes,member.shape);return form?tagValue(form,parseTagLines(text)):parseComplex(service?.inputShapes,member,text)}
 if(member.type==='boolean')return text===true;
 if(member.type==='integer'||member.type==='long'){const n=Number(text);if(String(text).trim()===''||!Number.isInteger(n))throw Error(member.name+' must be an integer.');return n}
 if(member.type==='float'||member.type==='double'){const n=Number(text);if(String(text).trim()===''||!Number.isFinite(n))throw Error(member.name+' must be a number.');return n}
 if(member.type==='list')return String(text).split(',').map(s=>s.trim()).filter(Boolean);
 return String(text);
}

// fieldText renders a current value as the text its control edits, so an
// update form starts from the resource as the service last described it.
export function fieldText(service,member,value){
 if(value===undefined||value===null)return member.type==='boolean'?false:'';
 const kind=control(service,member);
 if(kind==='tags')return tagPairs(value).map(([k,v])=>k+'='+v).join('\n');
 if(kind==='json')return JSON.stringify(fromWire(service.inputShapes,member.shape,value),null,2);
 if(kind==='toggle')return value===true;
 if(Array.isArray(value))return value.join(', ');
 return typeof value==='object'?'':String(value);
}

// resourceARN is the described ARN, or the simulator's ARN template filled
// for a resource whose shape carries none (Redshift parameter groups).
export function resourceARN(service,resource,value,region){
 if(!value)return '';
 if(value[resource.arnWire])return value[resource.arnWire];
 if(!resource.arnTemplate)return '';
 const name=value[resource.nameWire],id=value[resource.idWire]??name;
 if(!name&&!id)return '';
 return resource.arnTemplate.replaceAll('{partition}','aws').replaceAll('{region}',service.global?'':region).replaceAll('{account}',service.account).replaceAll('{name}',name??id).replaceAll('{id}',id);
}

const isScalar=v=>v===null||['string','number','boolean'].includes(typeof v);
const display=v=>typeof v==='number'&&v>1e9&&v<1e11?new Date(v*1000).toISOString():String(v);

export default function CatalogView({region,request,catalog,onSaving}){
 const {t}=useLocale();
 // Capabilities list compact summaries; the full native descriptors are
 // served separately so the capability document stays bounded.
 const [services,setServices]=useState(null),[descriptorError,setDescriptorError]=useState('');
 const [model,setModel]=useState(''),[kind,setKind]=useState('');
 const [parents,setParents]=useState([]),[parent,setParent]=useState(''),[items,setItems]=useState([]),[next,setNext]=useState('');
 const [detail,setDetail]=useState(null),[tags,setTags]=useState(null),[mode,setMode]=useState('list'),[form,setForm]=useState({});
 const [loading,setLoading]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState(''),[confirm,setConfirm]=useState(null);
 // The listed row a detail view shows, the open edit form (current values
 // and the values it started from) and the new-tag fields.
 const [selected,setSelected]=useState(null),[editing,setEditing]=useState(null),[tagForm,setTagForm]=useState({key:'',value:''});
 const revision=useRef(0),alive=useRef(true);
 const service=(services||[]).find(s=>s.model===model);
 const resource=service?.resources.find(r=>r.kind===kind)||service?.resources[0];
 const parentResource=resource?.parent?service.resources.find(r=>r.kind===resource.parent):null;
 const fresh=()=>++revision.current;const valid=epoch=>alive.current&&epoch===revision.current;
 useEffect(()=>()=>{alive.current=false;revision.current++},[]);
 useEffect(()=>{
  let live=true;const path=catalog?.descriptors;if(!path){setServices([]);return}
  request(path).then(async r=>{if(!r.ok)throw Error(t('Catalog descriptors are unavailable','カタログ記述子を取得できません')+' ('+r.status+').');return r.json()})
   .then(d=>{if(live){const list=d.services||[];setServices(list);setModel(m=>m||list[0]?.model||'')}})
   .catch(e=>{if(live){setServices([]);setDescriptorError(e.message)}});
  return ()=>{live=false};
 },[catalog?.descriptors]);
 // Fixed members are the selection the bounded model serves, and required
 // account members name the simulator account; every request of the
 // operation sends them, as SDK clients must.
 async function call(op,input){
  const account=Object.fromEntries((op.members||[]).filter(m=>m.required&&(service.accountMembers||[]).includes(m.name)).map(m=>[m.name,service.account]));
  const {path,options}=buildCatalogRequest(service,op,{...account,...(op.fixed||{}),...input},region);
  const response=await request(path,options);const text=await response.text();
  if(service.protocol==='query')return decodeQuery(service,op,text);
  return text?JSON.parse(text):{};
 }
 // List rows carry the list's own serialized identity keys, which may differ
 // from the read shape's (nameWire/idWire/arnWire).
 const rowKeys=(res)=>{const l=res?.operations.list;
  if(l?.valueItems){const arn=l.valueMember===res.arnWire;return {name:arn?'':l.valueMember,id:'',arn:arn?l.valueMember:''}}
  return {name:l?.itemName||res?.nameWire,id:l?.itemId||res?.idWire,arn:l?.itemArn||res?.arnWire}};
 const keys=rowKeys(resource);
 const identity=item=>item?.[keys.name]||item?.[keys.id]||item?.[keys.arn]||'';
 const lookupValue=(item,op)=>{
  // Prefer the identity the lookup member names, then any candidate that
  // matches the member's official pattern (an ID, not a display name).
  const order=op.lookupKey==='arn'?['arn','id','name']:op.lookupKey==='id'?['id','name','arn']:['name','id','arn'];
  const values=[...order.map(k=>item[keys[k]]),item[op.lookup]].filter(v=>v!==undefined&&v!==null&&v!=='');
  const member=(op.members||[]).find(m=>m.name===op.lookup);
  if(member?.pattern){try{const re=new RegExp('^(?:'+member.pattern+')$');const hit=values.find(v=>re.test(String(v)));if(hit!==undefined)return hit}catch{/* An RE2-only pattern falls back to the identity order. */}}
  return values[0]??identity(item);
 };
 const parentInput=op=>op?.parent&&parent?{[op.parent]:parent}:{};
 // Some official lists return plain ARNs or names; each becomes an item
 // carrying that one identifying key.
 const listRows=(list,page)=>list.items?page[list.items]||[]:list.valueItems?(page[list.valueItems]||[]).map(value=>({[list.valueMember]:value})):[];
 async function loadParents(epoch,preferred){
  const list=parentResource?.operations.list;if(!list)return '';
  const page=await call(list,{});if(!valid(epoch))return '';
  const rows=listRows(list,page);setParents(rows);
  // A parent row may carry only an ARN (Cloud Directory schema ARNs).
  const pk=rowKeys(parentResource),pid=r=>r?.[pk.id]||r?.[pk.name]||r?.[pk.arn]||'';const keep=rows.some(r=>pid(r)===preferred)?preferred:pid(rows[0]);
  setParent(keep);return keep;
 }
 // A selection passes its parent explicitly: the state update is not yet
 // visible to this render's closure.
 async function load(token='',preferred=parent){
  if(!service||!resource)return;const epoch=fresh();setLoading(true);setError('');setMode('list');setDetail(null);setTags(null);setEditing(null);setSelected(null);
  try{
   let scope=preferred;if(parentResource){scope=await loadParents(epoch,preferred);if(!valid(epoch))return;if(!scope){setItems([]);setNext('');return}}
   const list=resource.operations.list;if(!list){setItems([]);return}
   const page=await call(list,{...(list.parent&&scope?{[list.parent]:scope}:{}),...(token&&list.inputToken?{[list.inputToken]:token}:{})});
   if(valid(epoch)){setItems(listRows(list,page));setNext(list.outputToken?page[list.outputToken]||'':'')}
  }catch(e){if(valid(epoch)){setItems([]);setError(e.message)}}finally{if(valid(epoch))setLoading(false)}
 }
 useEffect(()=>{load()},[model,kind,region]);
 async function inspect(item){
  const read=resource.operations.read;const epoch=fresh();setError('');setMode('detail');setTags(null);setEditing(null);setSelected(item);
  // Without a read operation, a list that describes named resources gives
  // the details; otherwise the listed projection is the detail view.
  if(!read){
   const describe=resource.operations.describe,list=resource.operations.list;
   // A describe operation whose names member is one string (RDS
   // DescribeDBSubnetGroups DBSubnetGroupName) reads one resource by name.
   if(describe&&(describe.members||[]).some(m=>m.name===describe.names&&m.type==='string')){
    setLoading(true);setDetail(null);
    try{const page=await call(describe,{...parentInput(describe),[describe.names]:identity(item)});if(!valid(epoch))return;
     const value=(page[describe.items]||[])[0]||item;setDetail(value);await loadTags(epoch,value)}
    catch(e){if(valid(epoch))setError(e.message)}finally{if(valid(epoch))setLoading(false)}
    return;
   }
   if(!describe||!list?.valueItems){setDetail(item);setLoading(false);await loadTags(epoch,item);return}
   setLoading(true);setDetail(null);
   try{const page=await call(describe,{...parentInput(describe),[describe.names]:[item[list.valueMember]]});if(!valid(epoch))return;const value=(page[describe.items]||[])[0]||item;setDetail(value);await loadTags(epoch,value)}
   catch(e){if(valid(epoch))setError(e.message)}finally{if(valid(epoch))setLoading(false)}
   return;
  }
  setLoading(true);setDetail(null);
  try{
   const out=await call(read,{...parentInput(read),[read.lookup]:lookupValue(item,read)});if(!valid(epoch))return;
   const value=read.wrapper?out[read.wrapper]:out;setDetail(value);
   await loadTags(epoch,value);
  }catch(e){if(valid(epoch))setError(e.message)}finally{if(valid(epoch))setLoading(false)}
 }
 async function loadTags(epoch,value){
  const arn=resourceARN(service,resource,value,region);const listTags=service.tagging?.['list-tags'];
  // Without a list-tags operation the description itself carries the tags.
  if(!listTags&&resource.tagsWire){if(valid(epoch))setTags(value?.[resource.tagsWire]||[]);return}
  if(arn&&listTags){try{const tagged=await call(listTags,{[listTags.lookup]:arn});if(valid(epoch))setTags((listTags.tagsKey&&tagged[listTags.tagsKey])||tagged.Tags||tagged.tags||null)}catch{/* Tag permission is independent of read permission. */}}
 }
 async function create(){
  const op=resource.operations.create;fresh();setBusy(true);onSaving(true);setError('');setNotice('');
  let created=false;
  try{
   const input={...parentInput(op)};
   for(const member of formMembers(op,skipped(op))){const raw=form[member.name];if(raw===undefined||raw==='')continue;input[member.name]=convertField(member,raw,service)}
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
 // Fixed and account members are filled by call(), so the form omits them.
 const skipped=op=>new Set([op?.parent,'ClientToken','clientToken','ClientRequestToken','clientRequestToken',...Object.keys(op?.fixed||{}),...(service?.accountMembers||[])].filter(Boolean));
 // One native write per action; the detail is read again from the service
 // only after the write is acknowledged, and a failure stays visible.
 async function writeDetail(op,input,done){
  fresh();setBusy(true);onSaving(true);setError('');setNotice('');
  let written=false;
  try{await call(op,input);written=true}
  catch(e){if(alive.current)setError(e.message)}finally{if(alive.current){setBusy(false);onSaving(false)}}
  if(written&&alive.current){const item=selected;await inspect(item);if(alive.current)setNotice(done)}
  return written;
 }
 const updateOp=resource?.operations.update;
 const updateFields=updateOp?formMembers(updateOp,new Set([...skipped(updateOp),updateOp.lookup,...(updateOp.lookupAlt||[])])).filter(m=>!updateOp.editable||updateOp.editable.includes(m.name)):[];
 function startEdit(){
  const values=Object.fromEntries(updateFields.map(m=>[m.name,fieldText(service,m,detail?.[m.locationName||m.name])]));
  setNotice('');setEditing({values,initial:values});
 }
 async function saveUpdate(){
  const op=updateOp,input={...parentInput(op),[op.lookup]:lookupValue(selected,op)};let changed=false;
  try{
   for(const member of updateFields){
    const raw=editing.values[member.name],before=editing.initial[member.name];
    if(raw===before&&!member.required)continue;
    if(raw===''||raw===undefined){if(member.required)throw Error(member.name+' '+t('is required.','は必須です。'));throw Error(member.name+': '+t('clearing a value is not supported here; use an SDK.','ここでは値を空にできません。SDK を使ってください。'))}
    input[member.name]=convertField(member,raw,service);changed=changed||raw!==before;
   }
   if(!changed)throw Error(t('Change at least one field.','少なくとも 1 つの項目を変更してください。'));
  }catch(e){setError(e.message);return}
  const ok=await writeDetail(op,input,t('Resource updated.','リソースを更新しました。'));
  if(ok&&alive.current)setEditing(null);
 }
 // Tag operations take the resource's ARN, or the identity their lookup
 // member's official pattern accepts.
 const tagTarget=op=>{
  const arn=resourceARN(service,resource,detail,region);const values=[arn,detail?.[resource.idWire],detail?.[resource.nameWire]].filter(Boolean);
  const member=(op.members||[]).find(m=>m.name===op.lookup);
  if(member?.pattern){try{const re=new RegExp('^(?:'+member.pattern+')$');const hit=values.find(v=>re.test(String(v)));if(hit!==undefined)return hit}catch{/* An RE2-only pattern keeps the ARN. */}}
  return values[0]||'';
 };
 const tagOp=service?.tagging?.tag,untagOp=service?.tagging?.untag;
 const tagsMember=tagOp?.members?.find(m=>m.shape&&tagShape(service.inputShapes,m.shape));
 const keysMember=untagOp?.members?.find(m=>m.name!==untagOp.lookup&&m.type==='list'&&m.element==='string');
 async function addTag(){
  const key=tagForm.key.trim();
  if(!key){setError(t('Enter a tag key.','タグキーを入力してください。'));return}
  const ok=await writeDetail(tagOp,{[tagOp.lookup]:tagTarget(tagOp),[tagsMember.name]:tagValue(tagShape(service.inputShapes,tagsMember.shape),[[key,tagForm.value]])},t('Tag added.','タグを追加しました。'));
  if(ok&&alive.current)setTagForm({key:'',value:''});
 }
 async function removeTag(key){
  await writeDetail(untagOp,{[untagOp.lookup]:tagTarget(untagOp),[keysMember.name]:[key]},t('Tag removed.','タグを削除しました。'));
 }
 // One form control per member; values are kept as the text the control
 // edits and converted (and validated) only when the form is submitted.
 function field(member,values,update){
  const set=value=>update(current=>({...current,[member.name]:value}));
  const label=member.name+(member.required?'':' ('+t('optional','任意')+')');
  const kind=control(service,member);
  if(kind==='toggle')return <Toggle key={member.name} checked={values[member.name]===true} onChange={e=>set(e.detail.checked)}>{member.name}</Toggle>;
  if(kind==='enum')return <FormField key={member.name} label={label}><Select selectedOption={values[member.name]?{value:values[member.name],label:values[member.name]}:null} placeholder={t('Choose','選択')} onChange={e=>set(e.detail.selectedOption.value)} options={member.enum.map(v=>({value:v,label:v}))}/></FormField>;
  if(kind==='tags')return <FormField key={member.name} label={label} description={t('One key=value per line.','1 行に 1 つの key=value を入力します。')}><Textarea rows={3} value={values[member.name]??''} onChange={e=>set(e.detail.value)} placeholder="team=web"/></FormField>;
  if(kind==='json')return <FormField key={member.name} label={label} description={t('JSON value with official member names, for example ','公式メンバー名による JSON 値。例: ')+JSON.stringify(example(service.inputShapes,member.shape))}><Textarea rows={4} value={values[member.name]??''} onChange={e=>set(e.detail.value)} spellcheck={false}/></FormField>;
  return <FormField key={member.name} label={label} description={member.type==='list'?t('Comma-separated values','カンマ区切りの値'):undefined}><Input value={values[member.name]??''} onChange={e=>set(e.detail.value)} inputMode={['integer','long','float','double'].includes(member.type)?'numeric':undefined}/></FormField>;
 }
 const createOp=resource?.operations.create;
 const fields=useMemo(()=>formMembers(createOp,skipped(createOp)),[createOp]);
 if(descriptorError)return <Alert type="error">{descriptorError}</Alert>;
 if(services===null)return <Box>{t('Loading','読み込み中')}</Box>;
 if(!services.length)return <Alert type="info">{t('No catalog services are enabled.','有効なカタログサービスはありません。')}</Alert>;
 const columns=[{id:'name',header:t('Name or identifier','名前または識別子'),cell:item=><Link onFollow={e=>{e.preventDefault();inspect(item)}} href="#">{identity(item)}</Link>},
  {id:'arn',header:'ARN',cell:item=>item[keys.arn]||'-'}];
 return <ContentLayout header={<Header variant="h1" description={t('Bounded configuration metadata through each service\'s native API. Unlisted official operations return HTTP 501.','各サービスのネイティブ API で扱う、範囲を限定した設定メタデータです。一覧にない公式操作は HTTP 501 を返します。')}
   actions={<Link external href="/_simulator/docs/catalog.html">{t('API reference','API リファレンス')}</Link>}>{t('AWS service catalog','AWS サービスカタログ')}</Header>}>
  <SpaceBetween size="l">
   <FormField label={t('Catalog service','カタログサービス')}><Select selectedOption={{value:model,label:service?.title}} disabled={busy} onChange={e=>{setModel(e.detail.selectedOption.value);setKind('');setParent('');setParents([])}} options={services.map(s=>({value:s.model,label:s.title,description:s.operations.length+' / '+s.officialOperations+' '+t('official operations','公式操作')}))}/></FormField>
   {service.bounds.map(b=><Alert key={b} type="info">{b}</Alert>)}
   {error&&<Alert type="error" header={t('Request failed','リクエストに失敗しました')}>{error}</Alert>}
   {notice&&<Alert type="success" dismissible onDismiss={()=>setNotice('')}>{notice}</Alert>}
   <Tabs activeTabId={resource.kind} onChange={e=>{if(!busy){setKind(e.detail.activeTabId);setParent('')}}} tabs={service.resources.map(r=>({id:r.kind,label:r.kind}))}/>
   {parentResource&&<FormField label={parentResource.kind}><Select selectedOption={parent?{value:parent,label:parent}:null} placeholder={t('Create a parent resource first','先に親リソースを作成してください')} disabled={busy||!parents.length}
     onChange={e=>{const chosen=e.detail.selectedOption.value;setParent(chosen);load('',chosen)}} options={parents.map(p=>{const pk=rowKeys(parentResource);const v=p[pk.id]||p[pk.name]||p[pk.arn];return {value:v,label:p[pk.name]||v}})}/></FormField>}
   {mode==='detail'?<Container header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">{updateOp&&updateFields.length>0&&detail&&!editing&&<Button disabled={busy||loading} onClick={startEdit}>{t('Edit','編集')}</Button>}<Button disabled={busy} onClick={()=>load()}>{t('Back to list','一覧に戻る')}</Button></SpaceBetween>}>{t('Details','詳細')}</Header>}>
     {loading?<Box>{t('Loading','読み込み中')}</Box>:detail?<SpaceBetween size="m">
      {editing&&<form onSubmit={e=>{e.preventDefault();saveUpdate()}}><Form header={<Header variant="h3" description={t('Changes are sent with ','変更は ')+updateOp.name+t('. Only changed fields and required fields are sent.',' で送信されます。変更した項目と必須項目だけを送信します。')}>{t('Edit','編集')} {identity(selected)}</Header>}
        actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>setEditing(null)}>{t('Cancel','キャンセル')}</Button><Button variant="primary" formAction="submit" loading={busy}>{t('Save changes','変更を保存')}</Button></SpaceBetween>}>
       <SpaceBetween size="m">{updateFields.map(member=>field(member,editing.values,values=>setEditing(current=>current&&{...current,values:values(current.values)})))}</SpaceBetween></Form></form>}
      <KeyValuePairs columns={2} items={Object.entries(detail).filter(([,v])=>isScalar(v)).map(([k,v])=>({label:<span className="catalog-key">{k}</span>,value:<span className="catalog-key">{display(v)}</span>}))}/>
      {Object.entries(detail).filter(([k,v])=>!isScalar(v)&&k!==resource.tagsWire).map(([k,v])=><FormField key={k} label={k}><pre className="catalog-json">{JSON.stringify(v,null,2)}</pre></FormField>)}
      {tags!==null&&<Table variant="embedded" items={tagPairs(tags).map(([key,value])=>({key,value}))} trackBy="key" empty={<Box textAlign="center">{t('No tags','タグはありません')}</Box>}
        header={<Header variant="h3" counter={'('+tagPairs(tags).length+')'}>{t('Tags','タグ')}</Header>}
        columnDefinitions={[{id:'key',header:t('Key','キー'),cell:tag=>tag.key},{id:'value',header:t('Value','値'),cell:tag=>tag.value||'-'},
         ...(untagOp&&keysMember&&tagTarget(untagOp)?[{id:'remove',header:t('Actions','アクション'),cell:tag=><Button disabled={busy} ariaLabel={t('Remove tag','タグを削除')+' '+tag.key} onClick={()=>removeTag(tag.key)}>{t('Remove','削除')}</Button>}]:[])]}/>}
      {tagOp&&tagsMember&&tagTarget(tagOp)&&<form onSubmit={e=>{e.preventDefault();addTag()}}><Form actions={<Button formAction="submit" disabled={busy} loading={busy}>{t('Add tag','タグを追加')}</Button>}><SpaceBetween direction="horizontal" size="s">
       <FormField label={t('Tag key','タグキー')}><Input value={tagForm.key} onChange={e=>setTagForm(f=>({...f,key:e.detail.value}))}/></FormField>
       <FormField label={t('Tag value','タグ値')+' ('+t('optional','任意')+')'}><Input value={tagForm.value} onChange={e=>setTagForm(f=>({...f,value:e.detail.value}))}/></FormField>
      </SpaceBetween></Form></form>}</SpaceBetween>:null}
    </Container>:
    <Table loading={loading} loadingText={t('Loading resources','リソースを読み込み中')} items={items} columnDefinitions={[...columns,{id:'actions',header:t('Actions','アクション'),cell:item=>resource.operations.delete?<Button disabled={busy} ariaLabel={t('Delete','削除')+' '+identity(item)} onClick={()=>setConfirm(item)}>{t('Delete','削除')}</Button>:null}]}
     header={<Header counter={'('+items.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel={t('Refresh','更新')} disabled={busy} onClick={()=>load()}/>{next&&<Button disabled={busy} onClick={()=>load(next)}>{t('Next page','次のページ')}</Button>}</SpaceBetween>}>{resource.kind}</Header>}
     empty={<Box textAlign="center">{t('No resources','リソースはありません')}</Box>}/>}
   {createOp&&(!parentResource||parent)&&<Container header={<Header variant="h2">{t('Create','作成')} {resource.kind}</Header>}>
    <form onSubmit={e=>{e.preventDefault();create()}}><Form actions={<Button variant="primary" formAction="submit" loading={busy}>{t('Create','作成')}</Button>}><SpaceBetween size="m">
     {fields.map(member=>field(member,form,values=>setForm(values)))}
    </SpaceBetween></Form></form></Container>}
   <Modal visible={!!confirm} onDismiss={()=>setConfirm(null)} header={t('Delete resource','リソースを削除')} footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button onClick={()=>setConfirm(null)}>{t('Cancel','キャンセル')}</Button><Button variant="primary" onClick={()=>remove(confirm)}>{t('Delete','削除')}</Button></SpaceBetween></Box>}>
    {t('Delete','削除')} <strong>{identity(confirm||{})}</strong>?
   </Modal>
  </SpaceBetween>
 </ContentLayout>;
}
