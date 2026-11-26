import {parseEC2RouteTablePage,routeTableInventoryLimit,routeTablePageSize} from './ec2-response.mjs';

const regionShape=/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const tableShape=/^rtb-[0-9a-f]{17}$/;
const responseBytes=16*1024*1024,exactBytes=512*1024;
const invalid=()=>{throw Error('Invalid or incomplete EC2 route table response.');};
const xml=text=>new DOMParser().parseFromString(text,'application/xml');
const shape=(value,pattern)=>typeof value==='string'&&pattern.exec(value)?.[0]===value;

export async function readEC2RouteTableReply(response,signal,limit=responseBytes) {
  const type=response.headers?.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  const reader=response.body?.getReader();
  if (!reader) invalid();
  const decoder=new TextDecoder('utf-8',{fatal:true});
  let data='',size=0,done=false;
  try {
    if (type!=='text/xml'&&type!=='application/xml') invalid();
    for (;;) {
      signal?.throwIfAborted();
      const chunk=await reader.read();
      if (chunk.done) {done=true;break;}
      if (chunk.value.byteLength>limit-size) invalid();
      size+=chunk.value.byteLength;
      data+=decoder.decode(chunk.value,{stream:true});
    }
    signal?.throwIfAborted();
    return data+decoder.decode();
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function fetchPage(request,region,selector,signal,parse,exact) {
  signal?.throwIfAborted();
  const response=await request('/',{method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:'DescribeRouteTables',Version:'2016-11-15',...selector})});
  signal?.throwIfAborted();
  if (!response?.ok) throw Error((response?.status??0)+': Route table request failed.');
  const text=await readEC2RouteTableReply(response,signal,exact?exactBytes:responseBytes);
  signal?.throwIfAborted();
  return parse(text);
}

// Accumulate bounded typed rows, not XML documents. Neither rows nor mutation
// confirmation leave this function until the regional sequence completes.
export async function loadEC2RouteTablePages(request,region,{signal,selectedSubnet='',exactID='',parse=xml}={}) {
  if (!shape(region,regionShape)||region.length>32||exactID&&(!shape(exactID,tableShape)||selectedSubnet)) invalid();
  const result={tables:[],regionalIDs:new Set(),regionalAssociations:new Set(),regionalSubnets:new Set()};
  const seenTokens=new Set();
  let next=null,lastID='',count=0;
  const selector=exactID?{'RouteTableId.1':exactID}:{MaxResults:routeTablePageSize,
    ...(selectedSubnet?{'Filter.1.Name':'association.subnet-id','Filter.1.Value.1':selectedSubnet}:{})};
  function unique(set,id) {
    if (set.has(id)) invalid();
    set.add(id);
  }
  for (let page=0;page<Math.ceil(routeTableInventoryLimit/routeTablePageSize);page++) {
    const document=await fetchPage(request,region,{...selector,...(next===null?{}:{NextToken:next})},signal,parse,!!exactID);
    const {tables,token}=parseEC2RouteTablePage(document,{selectedSubnet,paginated:!exactID,rowLimit:exactID?1:routeTablePageSize});
    if (exactID&&(tables.length!==1||tables[0].id!==exactID) || next!==null&&tables.length===0 ||
        token!==null&&(tables.length!==routeTablePageSize||seenTokens.has(token))) invalid();
    for (const t of tables) {
      if (t.id<=lastID) invalid();
      lastID=t.id;
      unique(result.regionalIDs,t.id);
      for (const a of t.associations) {
        unique(result.regionalAssociations,a.id);
        unique(result.regionalSubnets,a.subnet);
      }
      count+=1+t.associations.length+t.routes.length;
      if (count>routeTableInventoryLimit) invalid();
      result.tables.push(t);
    }
    signal?.throwIfAborted();
    if (token===null) return result;
    unique(seenTokens,token);
    next=token;
  }
  return invalid();
}
