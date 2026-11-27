const namespace='http://ec2.amazonaws.com/doc/2016-11-15/';
const description='DescribeVpcs', upsert='CreateTags', removal='DeleteTags';
const maxTags=50, maxReply=16*1024*1024;
const selectedScope=new WeakMap();
const bytes=new TextEncoder();
const same=(value,pattern)=>typeof value==='string'&&pattern.exec(value)?.[0]===value;
const validId=id=>same(id,/^vpc-(?:[0-9a-f]{8}|[0-9a-f]{17})$/);
const invalid=()=>{throw Error('Could not verify the saved tags for this VPC.');};
const parseXML=text=>new DOMParser().parseFromString(text,'application/xml');

function xmlTag(value,max,min) {
  if(typeof value!=='string') return false;
  const n=bytes.encode(value).length;
  if(n<min||n>max) return false;
  for(const x of value) {
    const r=x.codePointAt(0);
    if ((r<32 && r!==9 && r!==10 && r!==13)||(r>=0xd800 && r<=0xdfff)||r===0xfffe||r===0xffff) return false;
  }
  return true;
}
export const validVpcTagKey=key=>xmlTag(key,128,1)&&!/^aws:/i.test(key);
export const validVpcTagValue=value=>xmlTag(value,256,0);

export function vpcTagEditingAvailable(capabilities,authenticationRequired) {
  const ec2=capabilities?.services?.ec2, ops=ec2?.operations;
  return authenticationRequired===true&&!capabilities?.consoleScope&&ec2?.console===true&&ec2.vpcTagMutationProfile==='single-native-vpc'&&Array.isArray(ops)&&
    [description,upsert,removal].every(name=>ops.includes(name));
}

function fields(node,names) {
  if(!node||[...(node.childNodes??[])].some(n=>n.nodeType!==1&&(n.nodeType!==3||n.textContent.trim()))) invalid();
  if([...node.children].some(n=>n.namespaceURI!==namespace||!names.includes(n.localName))) invalid();
}
function one(node,name,required=true) {
  const matches=[...(node?.children??[])].filter(n=>n.localName===name);
  if(matches.length>1||required&&matches.length!==1||matches.some(n=>n.namespaceURI!==namespace)) invalid();
  return matches[0];
}
function value(node,name) {
  const field=one(node,name);
  if(field.children.length||[...(field.childNodes??[])].some(n=>n.nodeType!==3&&n.nodeType!==4)) invalid();
  return field.textContent;
}
function envelope(document,action,names) {
  const root=document?.documentElement;
  if(document?.doctype||root?.namespaceURI!==namespace||root.localName!==action+'Response') invalid();
  fields(root,['requestId',...names]);
  if(!value(root,'requestId').trim()) invalid();
  return root;
}

export function parseVpcTagSnapshot(document,id) {
  if(!validId(id)) invalid();
  const root=envelope(document,description,['vpcSet','reservationSet']);
  const reservations=one(root,'reservationSet',false);
  if(reservations) fields(reservations,[]);
  const set=one(root,'vpcSet',false);
  if(!set) invalid();
  fields(set,['item']);
  const rows=[...set.children];
  if(rows.length>1000) invalid();
  const known=new Set();
  let selected;
  for(const row of rows) {
    const found=value(row,'vpcId');
    if(!validId(found)||known.has(found)) invalid();
    known.add(found);
    if(found===id) selected=row;
  }
  if(!selected) invalid();
  fields(selected,['vpcId','cidrBlock','tagSet','simulatorManagedBy']);
  if(value(selected,'simulatorManagedBy')!=='ec2') invalid();
  const tagSet=one(selected,'tagSet',false), tags=[];
  const keys=new Set();
  if(tagSet) {
    fields(tagSet,['item']);
    if(tagSet.children.length>maxTags) invalid();
    for(const tag of tagSet.children) {
      fields(tag,['key','value']);
      const key=value(tag,'key'), data=value(tag,'value');
      if(!validVpcTagKey(key)||!validVpcTagValue(data)||keys.has(key)) invalid();
      keys.add(key);tags.push(Object.freeze({key,value:data}));
    }
  }
  return Object.freeze({id,managedBy:'ec2',tags:Object.freeze(tags)});
}

function select(request,region,vpc) {
  if(typeof request!=='function'||!same(region,/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/)||region.length>32||
      !validId(vpc?.id)||vpc.managedBy!=='ec2') invalid();
  return {request,region,id:vpc.id};
}
async function query(scope,action,params,{signal,parse=parseXML}={}) {
  signal?.throwIfAborted();
  const response=await scope.request('/',{method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':scope.region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params})});
  signal?.throwIfAborted();
  if(response?.status!==200) throw Error((response?.status??0)+': VPC tag query failed.');
  const kind=response.headers?.get('content-type')?.split(';')[0].trim().toLowerCase();
  const reader=response.body?.getReader();
  if(!reader) invalid();
  const decoder=new TextDecoder('utf-8',{fatal:true});
  let raw='',size=0,done=false;
  try {
    if(kind!=='text/xml'&&kind!=='application/xml') invalid();
    for(;;) {
      signal?.throwIfAborted();
      const part=await reader.read();
      if(part.done){done=true;break;}
      size+=part.value.byteLength;
      if(size>maxReply)throw Error('VPC tags inventory exceeds the Console response limit.');
      raw+=decoder.decode(part.value,{stream:true});
    }
    signal?.throwIfAborted();
    return parse(raw+decoder.decode());
  } finally {
    if(!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function read(scope,options) {
  // The existing authorized DescribeVpcs is regional. Verify exactly one
  // matching row from that reply rather than inventing a wire selector.
  const snapshot=parseVpcTagSnapshot(await query(scope,description,{},options),scope.id);
  selectedScope.set(snapshot,scope);
  return snapshot;
}
export const loadVpcTagSnapshot=(request,region,vpc,options)=>read(select(request,region,vpc),options);

export async function changeVpcTag(request,region,vpc,snapshot,change,options) {
  const scope=select(request,region,vpc), previousScope=selectedScope.get(snapshot);
  const {kind,key,value:desired}=change??{};
  if(previousScope?.request!==request||previousScope.region!==region||previousScope.id!==scope.id||
     !validVpcTagKey(key)||!(kind==='upsert'&&validVpcTagValue(desired)||kind==='remove'&&desired===undefined)) invalid();
  const previous=snapshot.tags.find(row=>row.key===key)?.value;
  if(kind==='remove'&&previous===undefined) invalid();
  const fresh=await read(scope,options);
  if(fresh.tags.find(row=>row.key===key)?.value!==previous) invalid();
  if(kind==='upsert'&&previous===desired) return fresh;
  if(kind==='upsert'&&previous===undefined&&fresh.tags.length>=maxTags) throw Error('VPC tag limit reached.');
  const action=kind==='upsert'?upsert:removal;
  // A delete from the Console always supplies the last observed exact value,
  // including the empty string; it is never a key-only removal.
  const reply=await query(scope,action,{'ResourceId.1':scope.id,'Tag.1.Key':key,'Tag.1.Value':kind==='remove'?previous:desired},options);
  if(value(envelope(reply,action,['return']),'return')!=='true') invalid();
  const verified=await read(scope,options);
  if(verified.tags.find(row=>row.key===key)?.value!==(kind==='remove'?undefined:desired)) invalid();
  return verified;
}

export function vpcTagFailure(error) {
  const message=String(error?.message??'');
  if(message==='VPC tag limit reached.')return 'tag-limit';
  if(message==='VPC tags inventory exceeds the Console response limit.')return 'response-too-large';
  if(/^(401|403|UnauthorizedOperation)(?=:|$)/.test(message)||message==='Sign in to continue.')return 'denied';
  return 'unconfirmed';
}
