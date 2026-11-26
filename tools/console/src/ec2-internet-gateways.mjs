const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const operations=['DescribeInternetGateways','CreateInternetGateway','DeleteInternetGateway'];
const gatewayPattern=/^igw-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const vpcPattern=/^vpc-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const forbiddenXml=/[\u0000-\u0008\u000b\u000c\u000e-\u001f\ud800-\udfff\ufffe\uffff]/u;
const bytes=new TextEncoder();
const bindings=new WeakMap();
const invalid=()=>{throw Error('Could not confirm the saved internet gateway configuration.');};
const xml=text=>new DOMParser().parseFromString(text,'application/xml');
const exact=(value,pattern)=>typeof value==='string'&&pattern.exec(value)?.[0]===value;

export function internetGatewaysAvailable(capabilities,authenticationRequired) {
  const ec2=capabilities?.services?.ec2;
  return authenticationRequired===true&&!capabilities?.consoleScope&&ec2?.console===true&&
    Array.isArray(ec2.internetGatewayLifecycle)&&ec2.internetGatewayLifecycle.length===operations.length&&Array.isArray(ec2.operations)&&
    operations.every(id=>ec2.internetGatewayLifecycle.includes(id)&&ec2.operations.includes(id));
}

export function validInternetGatewayName(name) {
  return typeof name==='string'&&!forbiddenXml.test(name)&&bytes.encode(name).length<=256;
}

function entries(node) {
  if (!node||[...node.childNodes].some(n=>n.nodeType!==1&&(n.nodeType!==3||n.textContent.trim()))) invalid();
  const elements=[...node.children];
  if (elements.some(n=>n.namespaceURI!==ns)) invalid();
  return elements;
}

function members(node,names) {
  const result=new Map();
  for (const entry of entries(node)) {
    if (!names.includes(entry.localName)||result.has(entry.localName)) invalid();
    result.set(entry.localName,entry);
  }
  if (result.size!==names.length) invalid();
  return result;
}

function text(node) {
  if (!node||[...node.childNodes].some(n=>n.nodeType!==3&&n.nodeType!==4)) invalid();
  return node.textContent;
}

function list(node,bound) {
  const found=entries(node);
  if (found.length>bound||found.some(n=>n.localName!=='item')) invalid();
  return found;
}

function gateway(node) {
  const f=members(node,['internetGatewayId','ownerId','attachmentSet','tagSet','simulatorManagedBy']);
  const id=text(f.get('internetGatewayId')),managedBy=text(f.get('simulatorManagedBy'));
  if (!exact(id,gatewayPattern)||text(f.get('ownerId'))!=='000000000000'||!['ec2','cloudformation'].includes(managedBy)) invalid();
  const attachments=list(f.get('attachmentSet'),1);
  if (managedBy==='ec2'&&attachments.length) invalid();
  let vpc='';
  if (attachments.length) {
    const a=members(attachments[0],['vpcId','state']);
    vpc=text(a.get('vpcId'));
    if (!exact(vpc,vpcPattern)||text(a.get('state'))!=='available') invalid();
  }
  const tags=new Map();
  for (const item of list(f.get('tagSet'),50)) {
    const tag=members(item,['key','value']),key=text(tag.get('key')),value=text(tag.get('value'));
    if (!key||bytes.encode(key).length>128||key.toLowerCase().startsWith('aws:')||
        forbiddenXml.test(key)||tags.has(key)||!validInternetGatewayName(value)) invalid();
    tags.set(key,value);
  }
  return Object.freeze({id,managedBy,vpc,name:tags.get('Name')??'',tagCount:tags.size});
}

async function send(request,region,action,params,signal) {
  if (!exact(region,/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/)||region.length>32) invalid();
  signal?.throwIfAborted();
  const response=await request('/',{
    method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params}),
  });
  if (!response?.ok) throw Error((response?.status??0)+': Internet gateway request failed.');
  if (!['text/xml','application/xml'].includes(response.headers?.get('Content-Type')?.split(';')[0].trim().toLowerCase())) invalid();
  const reader=response.body?.getReader();
  if (!reader) invalid();
  const decoder=new TextDecoder('utf-8',{fatal:true});
  let raw='',size=0,done=false;
  try {
    for (;;) {
      signal?.throwIfAborted();
      const chunk=await reader.read();
      if (chunk.done) {done=true;break;}
      size+=chunk.value.byteLength;
      if (size>16*1024*1024) invalid();
      raw+=decoder.decode(chunk.value,{stream:true});
    }
    signal?.throwIfAborted();
    const document=xml(raw+decoder.decode()),root=document.documentElement;
    if (document.doctype||root?.namespaceURI!==ns||root.localName!==action+'Response') invalid();
    const member={CreateInternetGateway:'internetGateway',DescribeInternetGateways:'internetGatewaySet',DeleteInternetGateway:'return'}[action];
    const fields=members(root,['requestId',member]);
    if (!text(fields.get('requestId')).trim()) invalid();
    return fields.get(member);
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

export async function describeInternetGateways(request,region,signal) {
  const result=await send(request,region,'DescribeInternetGateways',{},signal);
  const rows=list(result,1000).map(gateway), ids=new Set(), vpcs=new Set();
  for (const row of rows) {
    if (ids.has(row.id)||row.vpc&&vpcs.has(row.vpc)) invalid();
    ids.add(row.id);
    if (row.vpc) vpcs.add(row.vpc);
    bindings.set(row,{request,region});
  }
  return rows;
}

export async function createInternetGateway(request,region,name,signal) {
  if (!validInternetGatewayName(name)) invalid();
  const params=name?{'TagSpecification.1.ResourceType':'internet-gateway','TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name}:{};
  const created=gateway(await send(request,region,'CreateInternetGateway',params,signal));
  if (created.managedBy!=='ec2'||created.name!==name||created.tagCount!==(name?1:0)) invalid();
  const rows=await describeInternetGateways(request,region,signal);
  if (!rows.some(row=>row.id===created.id&&row.managedBy==='ec2'&&row.name===name&&row.tagCount===created.tagCount)) invalid();
  return {id:created.id,rows};
}

export async function deleteInternetGateway(request,region,row,signal) {
  const scope=bindings.get(row);
  if (!scope||scope.request!==request||scope.region!==region||row.managedBy!=='ec2'||row.vpc) invalid();
  if (text(await send(request,region,'DeleteInternetGateway',{InternetGatewayId:row.id},signal))!=='true') invalid();
  const rows=await describeInternetGateways(request,region,signal);
  if (rows.some(found=>found.id===row.id)) invalid();
  return {id:row.id,rows};
}

export function internetGatewayError(error) {
  return /^(?:401:|403:|UnauthorizedOperation:|Sign in to continue\.)/.test(error?.message??'')?'denied':'unconfirmed';
}
