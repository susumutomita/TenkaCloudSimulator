const namespace = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const describe = 'DescribeVpcAttribute', modify = 'ModifyVpcAttribute';
const support = 'enableDnsSupport', hostnames = 'enableDnsHostnames';
export const vpcDNSAttributes = Object.freeze([support, hostnames]);
const parameter = {[support]:'EnableDnsSupport.Value', [hostnames]:'EnableDnsHostnames.Value'};
const bindings = new WeakMap();
const xml = raw => new DOMParser().parseFromString(raw, 'application/xml');
const invalid = () => {throw Error('Invalid or incomplete VPC DNS attribute response.');};
const whole = (value, pattern) => typeof value==='string' && pattern.exec(value)?.[0]===value;
const validVpcID = id => whole(id,/^vpc-[a-f0-9]{8,17}$/);

export function vpcDNSSettingsAvailable(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  return authenticationRequired===true && !capabilities?.consoleScope && ec2?.console===true &&
    Array.isArray(ec2.operations) && ['DescribeVpcs', describe].every(name=>ec2.operations.includes(name));
}

export function vpcDNSSettingsWritable(capabilities, authenticationRequired) {
  return vpcDNSSettingsAvailable(capabilities,authenticationRequired) && capabilities.services.ec2.operations.includes(modify);
}

export function vpcDNSTarget(vpc) {
  return validVpcID(vpc?.id) && ['ec2','cloudformation'].includes(vpc.managedBy) ? vpc.managedBy : '';
}

function scope(request, region, vpc) {
  const owner = vpcDNSTarget(vpc);
  if (typeof request!=='function' || !owner || !whole(region,/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/) || region.length>32) invalid();
  return {request,region,id:vpc.id,owner};
}

function fields(node, names) {
  if (!node || [...node.childNodes].some(n=>n.nodeType!==1 && (n.nodeType!==3||n.textContent.trim()))) invalid();
  const result = new Map();
  for (const child of node.children) {
    if (child.namespaceURI!==namespace || !names.includes(child.localName) || result.has(child.localName)) invalid();
    result.set(child.localName,child);
  }
  if (result.size!==names.length) invalid();
  return result;
}

function text(node) {
  if (!node || [...node.childNodes].some(n=>n.nodeType!==3)) invalid();
  return node.textContent;
}

function envelope(document, action, names) {
  const root = document?.documentElement;
  if (document?.doctype || root?.localName!==action+'Response' || root.namespaceURI!==namespace) invalid();
  const result = fields(root,['requestId',...names]);
  if (!text(result.get('requestId')).trim()) invalid();
  return result;
}

export function parseVpcDNSAttribute(document, vpcId, attribute) {
  if (!validVpcID(vpcId) || !vpcDNSAttributes.includes(attribute)) invalid();
  const result = envelope(document,describe,['vpcId',attribute]);
  const value = text(fields(result.get(attribute),['value']).get('value'));
  if (text(result.get('vpcId'))!==vpcId || (value!=='true'&&value!=='false')) invalid();
  return value==='true';
}

async function send(fixed, action, values, {signal,parse=xml}) {
  signal?.throwIfAborted();
  const response = await fixed.request('/', {
    method:'POST', credentials:'same-origin', redirect:'error', signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':fixed.region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',VpcId:fixed.id,...values}),
  });
  signal?.throwIfAborted();
  if (response?.status!==200) throw Error((response?.status??0)+': VPC DNS request failed.');
  const type = response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  const reader = response.body?.getReader();
  if (!reader) invalid();
  const decoder = new TextDecoder('utf-8',{fatal:true});
  let raw='', length=0, done=false;
  try {
    if (type!=='text/xml'&&type!=='application/xml') invalid();
    for (;;) {
      signal?.throwIfAborted();
      const chunk=await reader.read();
      if (chunk.done) {done=true;break;}
      length+=chunk.value.byteLength;
      if (length>8192) invalid();
      raw+=decoder.decode(chunk.value,{stream:true});
    }
    signal?.throwIfAborted();
    return parse(raw+decoder.decode());
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function read(fixed, options) {
  const result = {};
  // AWS models each attribute as a separate Query read, not a combined snapshot.
  for (const attribute of vpcDNSAttributes) {
    const doc=await send(fixed,describe,{Attribute:attribute},options);
    result[attribute]=parseVpcDNSAttribute(doc,fixed.id,attribute);
  }
  const snapshot=Object.freeze(result);
  bindings.set(snapshot,fixed);
  return snapshot;
}

export async function loadVpcDNSAttributes(request, region, vpc, options={}) {
  return read(scope(request,region,vpc),options);
}

// Null means invalid; [] means no updates. Turning on hostnames requires
// support first. When turning both off, disable hostnames before support.
export function planVpcDNSChanges(saved, desired) {
  if (![saved,desired].every(state=>vpcDNSAttributes.every(key=>typeof state?.[key]==='boolean')) ||
      (!saved[hostnames] && desired[hostnames] && !desired[support])) return null;
  const order=desired[support] ? vpcDNSAttributes : [hostnames,support];
  return order.filter(key=>saved[key]!==desired[key]).map(attribute=>({attribute,value:desired[attribute]}));
}

export function vpcDNSFailure(error) {
  const message=error?.message??'';
  if (/^(401:|403:|UnauthorizedOperation:|AccessDenied(?:Exception)?:|Sign in to continue\.)/.test(message)) return 'denied';
  if (message.startsWith('InvalidParameterCombination:')) return 'dependency';
  if (message.startsWith('InvalidVpcID.NotFound:')) return 'missing';
  return 'unconfirmed';
}

// After a failure reconcile only with reads. No automatic replay, or later
// write, even if the server might have committed the request without a receipt.
export async function saveVpcDNSAttributes(request, region, vpc, saved, desired, options={}) {
  const fixed=scope(request,region,vpc), original=bindings.get(saved), changes=planVpcDNSChanges(saved,desired);
  if (fixed.owner!=='ec2' || !original || Object.keys(fixed).some(key=>fixed[key]!==original[key]) || !changes) invalid();
  if (!changes.length) return {snapshot:saved, confirmed:[], failed:null};
  bindings.delete(saved);
  const confirmed=[];
  let latest=saved;
  for (const {attribute,value} of changes) {
    let phase='request';
    try {
      if (latest[attribute]===value) continue; // The readback already observed this value.
      if (attribute===hostnames && value && !latest[support]) throw Error('InvalidParameterCombination: DNS resolution is disabled.');
      const doc=await send(fixed,modify,{[parameter[attribute]]:String(value)},options);
      phase='readback';
      if (text(envelope(doc,modify,['return']).get('return'))!=='true') invalid();
      latest=await read(fixed,options);
      if (latest[attribute]!==value) invalid();
      confirmed.push(attribute);
    } catch (error) {
      options.signal?.throwIfAborted();
      let snapshot=null;
      try {snapshot=await read(fixed,options);} catch {options.signal?.throwIfAborted();}
      return {snapshot,confirmed,failed:{attribute,phase,reason:vpcDNSFailure(error)}};
    }
  }
  // Independent readers/writers can change a flag between requests. Report
  // the newest observations without pretending a prior readback is still true.
  const drift=changes.find(({attribute,value})=>latest[attribute]!==value);
  return {snapshot:latest,confirmed,failed:drift?{attribute:drift.attribute,phase:'readback',reason:'changed'}:null};
}
