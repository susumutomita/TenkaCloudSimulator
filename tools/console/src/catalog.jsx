import React,{useEffect,useMemo,useRef,useState} from 'react';
import {useLocale} from './i18n.jsx';
import AppStreamAppBlockForm from './appstream-app-block.jsx';
import {appStreamAppBlockDetailInput,isAppStreamAppBlock} from './appstream-app-block.mjs';
import {decodeQuery,queryBody} from './catalog-query.mjs';
import {example,fromWire,parseComplex,parseTagLines,tagPairs,tagShape,tagValue,toWire} from './catalog-input.mjs';
import {humanize,matches,phrase,pickColumns,plural,tableShape} from './catalog-names.mjs';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
import Pagination from '@cloudscape-design/components/pagination';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import TextFilter from '@cloudscape-design/components/text-filter';
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

// A read can return editable members beside its resource wrapper, including
// the body of a Network Firewall policy. Keep its token separately so it is
// never an editable field. Kinesis Video returns the version inside its wrapper.
export function catalogReadSnapshot(read,resource,out){
 const value=read.wrapper?out[read.wrapper]:out;
 if(!value||!resource.tokenWire)return {value};
 const token=value[resource.tokenWire]??out[resource.tokenWire];
 if(!read.wrapper)return {value,token};
 const editable=resource.operations.update?.editable||[];
 const siblings=Object.fromEntries(editable.filter(key=>key!==resource.tokenWire&&!(key in value)&&Object.hasOwn(out,key)).map(key=>[key,out[key]]));
 return {value:{...value,...siblings},token};
}

// Use exactly the version read when this editor opened. Never fetch a new
// version on Save or retry with it: that would overwrite a newer change.
export function catalogUpdateInput(op,identity,changes,token){
 if(!op.token)return {...identity,...changes};
 const member=op.members?.find(m=>m.name===op.token);
 const valid=member?.type==='string'?typeof token==='string'&&token.trim()!==''
  :['integer','long'].includes(member?.type)&&Number.isSafeInteger(token);
 return valid?{...identity,...changes,[op.token]:token}:null;
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
const display=v=>typeof v==='number'&&v>1e9&&v<1e11?new Date(v*1000).toISOString():Array.isArray(v)?v.join(', '):String(v);

// The official name is shown under a derived label only when they differ.
const official=name=>humanize(name)===name?undefined:<span className="catalog-key">{name}</span>;

// Form fields put the resource's name first, then required members, then
// the optional ones, each group in official-name order.
export function fieldOrder(members,resource){
 const rank=m=>m.name===resource?.nameMember?0:m.required?1:2;
 return [...members].sort((a,b)=>rank(a)-rank(b)||(a.name<b.name?-1:a.name>b.name?1:0));
}

// Each resource kind and member keeps its official name beside the derived
// label wherever a user maps it to the API or an SDK.
function Official({name}){return <Box variant="small" color="text-body-secondary"><span className="catalog-key">{name}</span></Box>}

export default function CatalogView({region,request,catalog,onSaving}){
 const {t}=useLocale();
 // Capabilities list compact summaries; the full native descriptors are
 // served separately so the capability document stays bounded.
 const [services,setServices]=useState(null),[descriptorError,setDescriptorError]=useState('');
 const [model,setModel]=useState(''),[kind,setKind]=useState('');
 const [parents,setParents]=useState([]),[parent,setParent]=useState(''),[items,setItems]=useState([]),[next,setNext]=useState('');
 // page is the view: list, create, detail or edit (as an AWS console's
 // resource list, create page and resource page).
 const [page,setPage]=useState('list'),[detail,setDetail]=useState(null),[tags,setTags]=useState(null),[form,setForm]=useState({});
 const [loading,setLoading]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[loadError,setLoadError]=useState(''),[notice,setNotice]=useState(''),[confirm,setConfirm]=useState(null);
 // The listed row a detail page shows, the open edit form (current values
 // and the values it started from), per-field problems and new-tag fields.
 const [selected,setSelected]=useState(null),[editing,setEditing]=useState(null),[fieldErrors,setFieldErrors]=useState({}),[tagForm,setTagForm]=useState({key:'',value:''});
 const [chosen,setChosen]=useState([]),[filter,setFilter]=useState(''),[pager,setPager]=useState({tokens:[''],index:0}),[scope,setScope]=useState({});
 const revision=useRef(0),alive=useRef(true);
 const service=(services||[]).find(s=>s.model===model);
 const resource=service?.resources.find(r=>r.kind===(kind||(service?.model==='appstream'?'appBlock':'')))||service?.resources[0];
 const appBlock=isAppStreamAppBlock(service,resource);
 const parentResource=resource?.parent?service.resources.find(r=>r.kind===resource.parent):null;
 // Required list members other than the parent and page token select what
 // the official list returns (ListControls controlType, GetResourceShares
 // resourceOwner): enums become a selector, page sizes use their maximum
 // and other strings are asked for before listing.
 const listOp=resource?.operations.list;
 const scopeMembers=(listOp?.members||[]).filter(m=>m.required&&m.name!==listOp.parent&&m.name!==listOp.inputToken&&!(m.name in (listOp.fixed||{}))&&!(service?.accountMembers||[]).includes(m.name));
 const scopeDefaults=()=>Object.fromEntries(scopeMembers.map(m=>[m.name,m.enum?m.enum[0]:['integer','long'].includes(m.type)?String(Math.min(m.max??100,100)):'']));
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
 // visible to this render's closure. The first page resets the token trail.
 async function load(token='',preferred=parent,values=scope){
  if(!service||!resource)return;const epoch=fresh();setLoading(true);setLoadError('');setPage('list');setDetail(null);setTags(null);setEditing(null);setSelected(null);setChosen([]);setFieldErrors({});
  if(!token)setPager({tokens:[''],index:0});
  try{
   let owner=preferred;if(parentResource){owner=await loadParents(epoch,preferred);if(!valid(epoch))return;if(!owner){setItems([]);setNext('');return}}
   const list=resource.operations.list;if(!list){setItems([]);return}
   const selection={};
   for(const member of scopeMembers){const value=values[member.name];if(value===undefined||value===''){setItems([]);setNext('');return}selection[member.name]=['integer','long'].includes(member.type)?Number(value):value}
   const page=await call(list,{...(list.parent&&owner?{[list.parent]:owner}:{}),...selection,...(token&&list.inputToken?{[list.inputToken]:token}:{})});
   if(valid(epoch)){setItems(listRows(list,page));setNext(list.outputToken?page[list.outputToken]||'':'')}
  }catch(e){if(valid(epoch)){setItems([]);setNext('');setLoadError(e.message)}}finally{if(valid(epoch))setLoading(false)}
 }
 useEffect(()=>{const values=scopeDefaults();setScope(values);setFilter('');setError('');setNotice('');load('',parent,values)},[model,kind,region]);
 // Service tokens page forward; earlier pages are reached by their tokens.
 function turnPage(index){
  if(index===pager.index+1&&next){setPager(p=>({tokens:[...p.tokens.slice(0,p.index+1),next],index:p.index+1}));load(next);return}
  if(index>=0&&index<pager.tokens.length){const token=pager.tokens[index];if(token){setPager(p=>({...p,index}));load(token)}else load('')}
 }
 async function inspect(item){
  const read=resource.operations.read;const epoch=fresh();setError('');setPage('detail');setTags(null);setEditing(null);setSelected(item);setFieldErrors({});
  // Without a read operation, a list that describes named resources gives
  // the details; otherwise the listed projection is the detail view.
  try{
   setLoading(true);setDetail(null);
   let value=item,token;
   if(read){const out=await call(read,{...parentInput(read),[read.lookup]:lookupValue(item,read)});if(!valid(epoch))return null;({value,token}=catalogReadSnapshot(read,resource,out))}
   else{
    const describe=resource.operations.describe,list=resource.operations.list;
    // A describe operation whose names member is one string (RDS
    // DescribeDBSubnetGroups DBSubnetGroupName) reads one resource by name.
    if(appBlock){
     const input=appStreamAppBlockDetailInput(resource,item);
     if(!input)throw Error(t('App block ARN is unavailable. Reload the list.','App Block ARN がありません。一覧を更新してください。'));
     const out=await call(list,input);if(!valid(epoch))return null;
     value=(out[list.items]||[]).find(row=>row.Arn===item.Arn);
     if(!value)throw Error(t('App block is no longer listed.','App Block が一覧にありません。'));
    }
    else if(describe&&(describe.members||[]).some(m=>m.name===describe.names&&m.type==='string')){const page=await call(describe,{...parentInput(describe),[describe.names]:identity(item)});if(!valid(epoch))return null;value=(page[describe.items]||[])[0]||item}
    else if(describe&&list?.valueItems){const page=await call(describe,{...parentInput(describe),[describe.names]:[item[list.valueMember]]});if(!valid(epoch))return null;value=(page[describe.items]||[])[0]||item}
   }
   setDetail(value);await loadTags(epoch,value);return valid(epoch)?{value,token}:null;
  }catch(e){if(valid(epoch))setError(e.message);return null}finally{if(valid(epoch))setLoading(false)}
 }
 async function loadTags(epoch,value){
  const arn=resourceARN(service,resource,value,region);const listTags=service.tagging?.['list-tags'];
  // Without a list-tags operation the description itself carries the tags.
  if(!listTags&&resource.tagsWire){if(valid(epoch))setTags(value?.[resource.tagsWire]||[]);return}
  if(arn&&listTags){try{const tagged=await call(listTags,{[listTags.lookup]:arn});if(valid(epoch))setTags((listTags.tagsKey&&tagged[listTags.tagsKey])||tagged.Tags||tagged.tags||null)}catch{/* Tag permission is independent of read permission. */}}
 }
 // Fixed and account members are filled by call(), so the form omits them.
 const skipped=op=>new Set([op?.parent,'ClientToken','clientToken','ClientRequestToken','clientRequestToken',...Object.keys(op?.fixed||{}),...(service?.accountMembers||[])].filter(Boolean));
 // Field values are converted and validated before any request; problems
 // are reported at their fields and nothing is sent.
 function readFields(members,values,initial){
  const input={},problems={};let changed=false;
  for(const member of members){
   const raw=values[member.name],before=initial?.[member.name];
   if(initial&&raw===before&&!member.required)continue;
   if(raw===undefined||raw===''){
    if(member.required)problems[member.name]=t('Required.','必須です。');
    else if(initial)problems[member.name]=t('Clearing a value is not supported here; use an SDK.','ここでは値を空にできません。SDK を使ってください。');
    continue;
   }
   try{input[member.name]=convertField(member,raw,service);changed=changed||raw!==before}catch(e){problems[member.name]=e.message}
  }
  return {input,problems,changed};
 }
 const createOp=resource?.operations.create;
 const fields=useMemo(()=>fieldOrder(formMembers(createOp,skipped(createOp)),resource),[createOp]);
 function openCreate(){setForm({});setFieldErrors({});setError('');setNotice('');setPage('create')}
 async function create(nativeInput){
  const op=createOp;const {input,problems}=nativeInput?{input:nativeInput,problems:{}}:readFields(fields,form);
  setFieldErrors(problems);if(Object.keys(problems).length){setError('');return}
  fresh();setBusy(true);onSaving(true);setError('');setNotice('');
  let created=false;
  try{await call(op,{...parentInput(op),...input});created=true}
  catch(e){if(alive.current)setError(e.message)}finally{if(alive.current){setBusy(false);onSaving(false)}}
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
 const updateFields=updateOp?fieldOrder(formMembers(updateOp,new Set([...skipped(updateOp),updateOp.lookup,updateOp.token,...(updateOp.lookupAlt||[])])).filter(m=>!updateOp.editable||updateOp.editable.includes(m.name)),resource):[];
 const missingEditToken=()=>t('The service did not return the current version. Reopen Edit to read the resource again.','現在のバージョンを取得できませんでした。編集を開き直してください。');
 function startEdit(value=detail,token){
  if(catalogUpdateInput(updateOp,{},{},token)===null){setError(missingEditToken());return}
  const values=Object.fromEntries(updateFields.map(m=>[m.name,fieldText(service,m,value?.[m.locationName||m.name])]));
  setNotice('');setError('');setFieldErrors({});setEditing({values,initial:values,token});setPage('edit');
 }
 // Editing from the list reads the resource first, so the form starts from
 // its current description.
 async function editFromList(item){const snapshot=await inspect(item);if(snapshot?.value&&alive.current)startEdit(snapshot.value,snapshot.token)}
 async function saveUpdate(){
  const op=updateOp;const {input,problems,changed}=readFields(updateFields,editing.values,editing.initial);
  setFieldErrors(problems);
  if(Object.keys(problems).length){setError('');return}
  if(!changed){setError(t('Change at least one field.','少なくとも 1 つの項目を変更してください。'));return}
  const update=catalogUpdateInput(op,{...parentInput(op),[op.lookup]:lookupValue(selected,op)},input,editing.token);
  if(update===null){setError(missingEditToken());return}
  const ok=await writeDetail(op,update,t('Resource updated.','リソースを更新しました。'));
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
  if(!key){setFieldErrors({tagKey:t('Enter a tag key.','タグキーを入力してください。')});return}
  setFieldErrors({});
  const ok=await writeDetail(tagOp,{[tagOp.lookup]:tagTarget(tagOp),[tagsMember.name]:tagValue(tagShape(service.inputShapes,tagsMember.shape),[[key,tagForm.value]])},t('Tag added.','タグを追加しました。'));
  if(ok&&alive.current)setTagForm({key:'',value:''});
 }
 async function removeTag(key){
  await writeDetail(untagOp,{[untagOp.lookup]:tagTarget(untagOp),[keysMember.name]:[key]},t('Tag removed.','タグを削除しました。'));
 }
 // One form control per member: a derived label, the official member name,
 // a required or optional mark, and the problem found at that field. Values
 // stay the text the control edits until the form is submitted.
 function field(member,values,update){
  // Editing a field clears the problem reported at it.
  const set=value=>{update(current=>({...current,[member.name]:value}));setFieldErrors(errors=>{if(!(member.name in errors))return errors;const rest={...errors};delete rest[member.name];return rest})};
  const kind=control(service,member);
  const hint=kind==='tags'?t('One key=value per line.','1 行に 1 つの key=value を入力します。'):kind==='json'?t('JSON value with official member names, for example ','公式メンバー名による JSON 値。例: ')+JSON.stringify(example(service.inputShapes,member.shape)):member.type==='list'?t('Comma-separated values.','カンマ区切りの値。'):'';
  const props={key:member.name,controlId:'catalog-field-'+member.name,label:<>{humanize(member.name)}{member.required?'':<i>{' - '+t('optional','任意')}</i>}</>,
   description:<><span className="catalog-key">{member.name}</span>{hint?' · '+hint:''}</>,errorText:fieldErrors[member.name]};
  if(kind==='toggle')return <FormField {...props}><Toggle checked={values[member.name]===true} onChange={e=>set(e.detail.checked)}>{humanize(member.name)}</Toggle></FormField>;
  if(kind==='enum')return <FormField {...props}><Select selectedOption={values[member.name]?{value:values[member.name],label:values[member.name]}:null} placeholder={t('Choose','選択')} onChange={e=>set(e.detail.selectedOption.value)} options={member.enum.map(v=>({value:v,label:v}))}/></FormField>;
  if(kind==='tags')return <FormField {...props}><Textarea rows={3} value={values[member.name]??''} onChange={e=>set(e.detail.value)} placeholder="team=web"/></FormField>;
  if(kind==='json')return <FormField {...props} stretch><Textarea rows={4} value={values[member.name]??''} onChange={e=>set(e.detail.value)} spellcheck={false}/></FormField>;
  return <FormField {...props}><Input value={values[member.name]??''} onChange={e=>set(e.detail.value)} inputMode={['integer','long','float','double'].includes(member.type)?'numeric':undefined}/></FormField>;
 }
 const kindName=res=>humanize(res?.kind||'');
 const columnKeys=useMemo(()=>pickColumns(items,[keys.name,keys.id,keys.arn].filter(Boolean)),[items,keys.name,keys.id,keys.arn]);
 const shown=useMemo(()=>items.filter(row=>matches(row,filter)),[items,filter]);
 if(descriptorError)return <Alert type="error">{descriptorError}</Alert>;
 if(services===null)return <Box>{t('Loading','読み込み中')}</Box>;
 if(!services.length)return <Alert type="info">{t('No catalog services are enabled.','有効なカタログサービスはありません。')}</Alert>;
 const singular=kindName(resource),many=appBlock?t('App blocks','App Block'):plural(singular);
 const createLabel=appBlock?t('Create app block','App Block を作成'):t('Create ','作成: ')+phrase(singular);
 const pick=chosen[0];
 const toList=e=>{e?.preventDefault?.();if(!busy)load()};
 const crumbs=last=><BreadcrumbGroup ariaLabel={t('Catalog location','カタログ内の位置')} onFollow={e=>{e.preventDefault();if(e.detail.href==='#list')toList()}}
   items={[{text:service.title,href:'#list'},...(appBlock?[{text:t('Applications Manager','Applications Manager'),href:'#list'}]:[]),{text:many,href:'#list'},...(last?[{text:last,href:'#here'}]:[])]}/>;
 const alerts=<>{error&&<Alert type="error" header={t('Request failed','リクエストに失敗しました')}>{error}</Alert>}
  {notice&&<Alert type="success" dismissible onDismiss={()=>setNotice('')}>{notice}</Alert>}</>;
 const confirmModal=<Modal visible={!!confirm} onDismiss={()=>setConfirm(null)} header={t('Delete ','削除: ')+phrase(singular)}
   footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button onClick={()=>setConfirm(null)}>{t('Cancel','キャンセル')}</Button><Button variant="primary" onClick={()=>remove(confirm)}>{t('Delete','削除')}</Button></SpaceBetween></Box>}>
   {t('Delete','削除')} <strong>{identity(confirm||{})}</strong>? {t('This sends ','次の操作を送信します: ')}<code>{resource.operations.delete?.name}</code>.
  </Modal>;
 if(page==='create'&&createOp&&appBlock){
  return <ContentLayout header={<SpaceBetween size="s">{crumbs(createLabel)}<Header variant="h1">{createLabel}</Header></SpaceBetween>}>
    <AppStreamAppBlockForm busy={busy} error={error} onCancel={()=>{setError('');setPage('list')}} onCreate={create}/>
   </ContentLayout>;
 }
 if(page==='create'&&createOp){
  return <ContentLayout header={<SpaceBetween size="s">{crumbs(t('Create','作成'))}<Header variant="h1" description={<>{t('Sends ','送信する操作: ')}<code>{createOp.name}</code>{t(' to the simulator. Unlisted official members are available through SDKs.','。一覧にない公式メンバーは SDK から利用できます。')}</>}>{createLabel}</Header></SpaceBetween>}>
   <form onSubmit={e=>{e.preventDefault();create()}}><Form errorText={error||undefined} errorIconAriaLabel={t('Error','エラー')}
     actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" variant="link" disabled={busy} onClick={()=>{setError('');setPage('list')}}>{t('Cancel','キャンセル')}</Button><Button variant="primary" formAction="submit" loading={busy}>{t('Create','作成')}</Button></SpaceBetween>}>
    <SpaceBetween size="l">
     {parentResource&&<Container header={<Header variant="h2">{humanize(parentResource.kind)}</Header>}><Box>{parent}</Box></Container>}
     <Container header={<Header variant="h2">{t('Settings','設定')}</Header>}>
      <SpaceBetween size="l">{fields.length?fields.map(member=>field(member,form,values=>setForm(values))):<Box>{t('This operation takes no editable members.','この操作には編集できるメンバーがありません。')}</Box>}</SpaceBetween>
     </Container>
    </SpaceBetween></Form></form>
  </ContentLayout>;
 }
 const name=identity(selected)||identity(detail);
 if(page==='edit'&&editing){
  return <ContentLayout header={<SpaceBetween size="s">{crumbs(name)}<Header variant="h1" description={<>{t('Changes are sent with ','変更は ')}<code>{updateOp.name}</code>{t('. Only changed and required fields are sent.',' で送信されます。変更した項目と必須項目だけを送信します。')}</>}>{t('Edit','編集')+' '+name}</Header></SpaceBetween>}>
   <form onSubmit={e=>{e.preventDefault();saveUpdate()}}><Form errorText={error||undefined} errorIconAriaLabel={t('Error','エラー')}
     actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" variant="link" disabled={busy} onClick={()=>{setError('');setFieldErrors({});setEditing(null);setPage('detail')}}>{t('Cancel','キャンセル')}</Button><Button variant="primary" formAction="submit" loading={busy}>{t('Save changes','変更を保存')}</Button></SpaceBetween>}>
    <Container header={<Header variant="h2">{t('Settings','設定')}</Header>}>
     <SpaceBetween size="l">{updateFields.map(member=>field(member,editing.values,values=>setEditing(current=>current&&{...current,values:values(current.values)})))}</SpaceBetween>
    </Container></Form></form>
  </ContentLayout>;
 }
 if(page==='detail'||page==='edit'){
  const entries=Object.entries(detail||{});
  const scalars=entries.filter(([,v])=>isScalar(v)||Array.isArray(v)&&v.every(x=>isScalar(x)));
  const complex=entries.filter(([k,v])=>!scalars.some(([s])=>s===k)&&k!==resource.tagsWire);
  const section=(k,v)=>{
   const columns=tableShape(v);
   if(columns)return <Table key={k} variant="container" items={v} header={<Header variant="h2" counter={'('+v.length+')'} description={official(k)}>{humanize(k)}</Header>}
     columnDefinitions={columns.map(c=>({id:c,header:humanize(c),cell:row=>row[c]===undefined||row[c]===null?'-':display(row[c])}))}/>;
   if(v&&typeof v==='object'&&!Array.isArray(v)&&Object.values(v).every(x=>isScalar(x)||Array.isArray(x)&&x.every(isScalar))&&Object.keys(v).length)
    return <Container key={k} header={<Header variant="h2" description={official(k)}>{humanize(k)}</Header>}>
     <KeyValuePairs columns={2} items={Object.entries(v).map(([n,x])=>({label:<span className="catalog-key">{humanize(n)}</span>,value:<span className="catalog-key">{display(x)}</span>}))}/></Container>;
   return <Container key={k} header={<Header variant="h2" description={official(k)}>{humanize(k)}</Header>}><pre className="catalog-json">{JSON.stringify(v,null,2)}</pre></Container>;
  };
  const pairs=tagPairs(tags);
  return <ContentLayout header={<SpaceBetween size="s">{crumbs(name)}<Header variant="h1" description={singular}
    actions={<SpaceBetween direction="horizontal" size="xs">
     {updateOp&&updateFields.length>0&&<Button disabled={busy||loading||!detail} onClick={()=>updateOp.token?editFromList(selected):startEdit()}>{t('Edit','編集')}</Button>}
     {resource.operations.delete&&<Button disabled={busy||loading} onClick={()=>setConfirm(selected)}>{t('Delete','削除')}</Button>}</SpaceBetween>}>{name}</Header></SpaceBetween>}>
   <SpaceBetween size="l">
    {alerts}
    <Container header={<Header variant="h2">{t('Details','詳細')}</Header>}>
     {loading?<StatusIndicator type="loading">{t('Loading','読み込み中')}</StatusIndicator>:detail?<KeyValuePairs columns={3} items={scalars.map(([k,v])=>({label:<span className="catalog-key">{humanize(k)}</span>,value:<SpaceBetween size="xxxs"><span className="catalog-key">{display(v)===''?'-':display(v)}</span><Official name={k}/></SpaceBetween>}))}/>:<Box>{t('No details','詳細はありません')}</Box>}
    </Container>
    {!loading&&complex.map(([k,v])=>section(k,v))}
    {!loading&&tags!==null&&<Table variant="container" items={pairs.map(([key,value])=>({key,value}))} trackBy="key" empty={<Box textAlign="center" color="inherit">{t('No tags','タグはありません')}</Box>}
      header={<Header variant="h2" counter={'('+pairs.length+')'} description={t('Tags are written with ','タグは次の操作で書き込みます: ')+[tagOp?.name,untagOp?.name].filter(Boolean).join(t(' and ','、'))+'.'}>{t('Tags','タグ')}</Header>}
      columnDefinitions={[{id:'key',header:t('Key','キー'),cell:tag=>tag.key},{id:'value',header:t('Value','値'),cell:tag=>tag.value||'-'},
       ...(untagOp&&keysMember&&tagTarget(untagOp)?[{id:'remove',header:t('Actions','アクション'),cell:tag=><Button disabled={busy} ariaLabel={t('Remove tag','タグを削除')+' '+tag.key} onClick={()=>removeTag(tag.key)}>{t('Remove','削除')}</Button>}]:[])]}
      footer={tagOp&&tagsMember&&tagTarget(tagOp)?<form onSubmit={e=>{e.preventDefault();addTag()}}><SpaceBetween size="s"><ColumnLayout columns={2}>
       <FormField label={t('Tag key','タグキー')} controlId="catalog-tag-key" errorText={fieldErrors.tagKey}><Input value={tagForm.key} onChange={e=>{setTagForm(f=>({...f,key:e.detail.value}));setFieldErrors({})}}/></FormField>
       <FormField label={<>{t('Tag value','タグ値')}<i>{' - '+t('optional','任意')}</i></>} controlId="catalog-tag-value"><Input value={tagForm.value} onChange={e=>setTagForm(f=>({...f,value:e.detail.value}))}/></FormField>
      </ColumnLayout><Button formAction="submit" disabled={busy} loading={busy}>{t('Add tag','タグを追加')}</Button></SpaceBetween></form>:undefined}/>}
    {confirmModal}
   </SpaceBetween>
  </ContentLayout>;
 }
 const columns=[{id:'name',header:t('Name','名前'),cell:item=><Link onFollow={e=>{e.preventDefault();inspect(item)}} href="#">{identity(item)||'-'}</Link>,isRowHeader:true},
  ...columnKeys.map(k=>({id:'c-'+k,header:humanize(k),cell:item=>item[k]===undefined||item[k]===null||item[k]===''?'-':display(item[k])})),
  ...(keys.arn&&keys.arn!==keys.name?[{id:'arn',header:'ARN',cell:item=>item[keys.arn]||'-'}]:[])];
 const createButton=createOp&&<Button variant="primary" disabled={busy||!!parentResource&&!parent} onClick={openCreate}>{createLabel}</Button>;
 return <ContentLayout header={<Header variant="h1" description={t('Bounded configuration metadata through each service\'s native API. Unlisted official operations return HTTP 501.','各サービスのネイティブ API で扱う、範囲を限定した設定メタデータです。一覧にない公式操作は HTTP 501 を返します。')}
   actions={<Link external href="/_simulator/docs/catalog.html">{t('API reference','API リファレンス')}</Link>}>{t('AWS service catalog','AWS サービスカタログ')}</Header>}>
  <SpaceBetween size="l">
   <FormField label={t('Catalog service','カタログサービス')}><Select selectedOption={{value:model,label:service?.title}} filteringType="auto" disabled={busy} onChange={e=>{setModel(e.detail.selectedOption.value);setKind('');setParent('');setParents([])}} options={services.map(s=>({value:s.model,label:s.title,description:s.operations.length+' / '+s.officialOperations+' '+t('official operations','公式操作')}))}/></FormField>
   {/* Bounds stay complete but collapsed, so they do not push the list down. */}
   <ExpandableSection variant="container" headerText={t('Simulator scope','シミュレーターの範囲')} headerDescription={service.bounds.length+' '+t('notes on what this service implements and refuses','件の実装範囲と拒否する操作についての注記')}>
    <ul className="catalog-bounds">{service.bounds.map(b=><li key={b}>{b}</li>)}</ul>
   </ExpandableSection>
   {alerts}
   {appBlock&&crumbs()}
   <Tabs activeTabId={resource.kind} onChange={e=>{if(!busy){setKind(e.detail.activeTabId);setParent('')}}} tabs={service.resources.map(r=>({id:r.kind,label:plural(kindName(r))}))}/>
   {parentResource&&<FormField label={humanize(parentResource.kind)}><Select selectedOption={parent?{value:parent,label:parent}:null} placeholder={t('Create a parent resource first','先に親リソースを作成してください')} disabled={busy||!parents.length}
     onChange={e=>{const value=e.detail.selectedOption.value;setParent(value);load('',value)}} options={parents.map(p=>{const pk=rowKeys(parentResource);const v=p[pk.id]||p[pk.name]||p[pk.arn];return {value:v,label:p[pk.name]||v}})}/></FormField>}
   {scopeMembers.some(m=>m.enum||!['integer','long'].includes(m.type))&&<ColumnLayout columns={3}>{scopeMembers.filter(m=>m.enum||!['integer','long'].includes(m.type)).map(m=>m.enum
     ?<FormField key={m.name} controlId={'catalog-scope-'+m.name} label={humanize(m.name)} description={official(m.name)}><Select selectedOption={{value:scope[m.name],label:scope[m.name]}} disabled={busy||loading} onChange={e=>{const values={...scope,[m.name]:e.detail.selectedOption.value};setScope(values);load('',parent,values)}} options={m.enum.map(v=>({value:v,label:v}))}/></FormField>
     :<FormField key={m.name} controlId={'catalog-scope-'+m.name} label={humanize(m.name)} description={official(m.name)} secondaryControl={<Button disabled={busy||loading||!scope[m.name]} onClick={()=>load('',parent,scope)}>{t('List','一覧表示')}</Button>}><Input value={scope[m.name]??''} onChange={e=>setScope(v=>({...v,[m.name]:e.detail.value}))}/></FormField>)}</ColumnLayout>}
   <Table loading={loading} loadingText={t('Loading ','読み込み中: ')+phrase(many)} items={shown} columnDefinitions={columns} trackBy={item=>identity(item)}
    selectionType={resource.operations.delete||updateOp?'single':undefined} selectedItems={chosen} onSelectionChange={e=>setChosen(e.detail.selectedItems)}
    ariaLabels={{selectionGroupLabel:t('Select','選択'),itemSelectionLabel:(_,item)=>identity(item),allItemsSelectionLabel:()=>t('Select all','すべて選択')}}
    header={<Header counter={'('+items.length+(next?'+':'')+')'} description={<span className="catalog-key">{resource.operations.list?.name}</span>}
     actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" ariaLabel={t('Refresh','更新')} disabled={busy||loading} onClick={()=>load(pager.tokens[pager.index])}/>
      <Button disabled={!pick||busy} onClick={()=>inspect(pick)}>{t('View details','詳細を表示')}</Button>
      {updateOp&&updateFields.length>0&&<Button disabled={!pick||busy} onClick={()=>editFromList(pick)}>{t('Edit','編集')}</Button>}
      {resource.operations.delete&&<Button disabled={!pick||busy} onClick={()=>setConfirm(pick)}>{t('Delete','削除')}</Button>}
      {createButton}</SpaceBetween>}>{many}</Header>}
    filter={<TextFilter filteringText={filter} filteringPlaceholder={t('Find ','検索: ')+phrase(many)} filteringAriaLabel={t('Filter loaded ','読み込んだ項目を絞り込む: ')+phrase(many)}
     countText={filter?shown.length+' '+t('matches','件一致'):''} onChange={e=>setFilter(e.detail.filteringText)}/>}
    pagination={resource.operations.list?.outputToken?<Pagination currentPageIndex={pager.index+1} pagesCount={pager.tokens.length} openEnd={!!next} disabled={loading||busy}
     ariaLabels={{nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>t('Page ','ページ ')+n}}
     onChange={e=>turnPage(e.detail.currentPageIndex-1)} onNextPageClick={e=>{if(!e.detail.requestedPageAvailable)turnPage(pager.index+1)}}/>:undefined}
    empty={scopeMembers.some(m=>!scope[m.name])?<Box textAlign="center" color="inherit">{(names=>t('Enter '+names+' to list '+phrase(many)+'.','一覧表示するには次を入力してください: '+names))(scopeMembers.filter(m=>!scope[m.name]).map(m=>humanize(m.name)).join(', '))}</Box>:loadError?<Box textAlign="center" color="inherit"><SpaceBetween size="xs"><StatusIndicator type="error">{loadError}</StatusIndicator><Button onClick={()=>load(pager.tokens[pager.index])}>{t('Retry','再試行')}</Button></SpaceBetween></Box>
     :<Box textAlign="center" color="inherit"><SpaceBetween size="xs"><b>{t('No ','')+phrase(many)+t('','はありません')}</b>{createOp&&<Button disabled={busy||!!parentResource&&!parent} onClick={openCreate}>{createLabel}</Button>}</SpaceBetween></Box>}
    noMatch={<Box textAlign="center" color="inherit"><SpaceBetween size="xs"><b>{t('No matches','一致する項目はありません')}</b><Button onClick={()=>setFilter('')}>{t('Clear filter','絞り込みを解除')}</Button></SpaceBetween></Box>}/>
   {confirmModal}
  </SpaceBetween>
 </ContentLayout>;
}
