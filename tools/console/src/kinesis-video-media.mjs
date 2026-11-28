export const maxFragmentBytes=4*1024*1024;
const maxResponseBytes=32*1024*1024+64*1024;
const positive=/^[1-9][0-9]{0,18}$/;
const milliseconds=/^(?:0|[1-9][0-9]{0,15})$/;
const seconds=/^(?:0|[1-9][0-9]{0,11})\.[0-9]{3}$/;

export function currentStream(info,region) {
  const parts=/^arn:aws:kinesisvideo:([a-z0-9-]+):([0-9]{12}):stream\/([a-zA-Z0-9_.-]{1,256})\/([0-9]+)$/.exec(info?.StreamARN);
  return !!parts && parts[1]===region && parts[3]===info.StreamName && info.Status==='ACTIVE' &&
    info.KmsKeyId==='alias/aws/kinesisvideo' && Number.isInteger(info.DataRetentionInHours) &&
    info.DataRetentionInHours>=0 && info.DataRetentionInHours<=87600;
}

export function localMediaPath(endpoint,origin,operation) {
  if (operation!=='PUT_MEDIA' && operation!=='GET_MEDIA') throw Error('Unknown media operation.');
  const url=new URL(origin);
  if (url.protocol!=='http:' || endpoint!==url.origin || origin!==url.origin ||
      !(/^127\.(?:[0-9]{1,3}\.){2}[0-9]{1,3}$/.test(url.hostname)||url.hostname==='[::1]')) {
    throw Error('Media requires this simulator’s numeric HTTP loopback address.');
  }
  return url.origin+(operation==='PUT_MEDIA'?'/putMedia':'/getMedia');
}

export async function limitedBytes(response,maximum=maxResponseBytes) {
  const declared=response.headers.get('Content-Length');
  if(declared!==null && (!/^[0-9]+$/.test(declared)||Number(declared)>maximum))throw Error('Media response exceeds the local limit.');
  const reader=response.body.getReader();
  const chunks=[];let count=0;
  try{
    for(;;){
      const {value,done}=await reader.read();if(done)break;
      count+=value.length;
      if(count>maximum)throw Error('Media response exceeds the local limit.');
      chunks.push(value);
    }
  }catch(error){await reader.cancel().catch(()=>{});throw error;}finally{reader.releaseLock();}
  const all=new Uint8Array(count);let pos=0;
  for(const chunk of chunks){all.set(chunk,pos);pos+=chunk.length;}
  return all;
}

export function uploadReceipt(bytes) {
  const lines=new TextDecoder('utf-8',{fatal:true}).decode(bytes).trim().split(/\r?\n/);
  const events=lines.map(line=>JSON.parse(line));
  const expected=events.length===3?['BUFFERING','RECEIVED','PERSISTED']:['BUFFERING','RECEIVED'];
  if(events.length!==expected.length || events.some((ack,i)=>ack?.EventType!==expected[i] ||
      !Number.isSafeInteger(ack.FragmentTimecode) || ack.FragmentTimecode<0 ||
      ack.FragmentTimecode!==events[0].FragmentTimecode ||
      (expected.length===3 ? (typeof ack.FragmentNumber!=='string' || !positive.test(ack.FragmentNumber) || ack.FragmentNumber!==events[0].FragmentNumber) : ack.FragmentNumber!==undefined)))throw Error('Incomplete fragment acknowledgement.');
  return {events:expected,number:events[0].FragmentNumber,timecode:events[0].FragmentTimecode};
}

// Read element IDs and finite lengths. Encoded AVC, optional codec data and
// unknown elements are opaque; only the server’s segment-level tags are read.
function element(data,pos,end) {
  function vint(keepMarker,max) {
    const first=data[pos++];if(!first)throw Error('Invalid Matroska element.');
    let length=1,marker=128;while(!(first&marker)){length++;marker>>=1;}
    if(length>max || pos+length-1>end)throw Error('Truncated Matroska element.');
    let num=keepMarker?first:first-marker;
    for(let i=1;i<length;i++){num=num*256+data[pos++];if(!Number.isSafeInteger(num))throw Error('Unsupported Matroska length.');}
    if(!keepMarker && num===2**(7*length)-1)throw Error('Unbounded Matroska element.');
    return num;
  }
  const id=vint(true,4),length=vint(false,8),last=pos+length;
  if(last>end)throw Error('Truncated Matroska payload.');
  return {id,begin:pos,end:last};
}
function children(data,item,limit) {
  const result=[];
  for(let i=item.begin;i<item.end;){const part=element(data,i,item.end);if(result.length===limit)throw Error('Too many Matroska elements.');result.push(part);i=part.end;}
  return result;
}
function one(elements,id) {
  const list=elements.filter(item=>item.id===id);if(list.length!==1)throw Error('Missing or duplicate Matroska element.');
  return list[0];
}
function textField(data,item) {
  if(item.end-item.begin>256)throw Error('Oversized media tag.');
  return new TextDecoder('utf-8',{fatal:true}).decode(data.subarray(item.begin,item.end));
}
export function readMediaChunks(data) {
  if(!(data instanceof Uint8Array) || data.length>maxResponseBytes)throw Error('Invalid media response.');
  const result=[],numbers=new Set(),tokens=new Set();
  for(let pos=0;pos<data.length;){
    const header=element(data,pos,data.length);
    if(header.id!==0x1a45dfa3)throw Error('Missing Matroska header.');
    const segment=element(data,header.end,data.length);
    if(segment.id!==0x18538067 || result.length>=16)throw Error('Invalid media segment.');
    const values=children(data,segment,32);
    one(values,0x1f43b675);one(values,0x1654ae6b);one(values,0x1549a966);
    const tag=one(children(data,one(values,0x1254c367),8),0x7373);
    const fields=new Map();
    for(const item of children(data,tag,20).filter(p=>p.id===0x67c8)){
      const parts=children(data,item,8),name=textField(data,one(parts,0x45a3)),value=textField(data,one(parts,0x4487));
      if(fields.has(name))throw Error('Duplicate media tag.');
      fields.set(name,value);
    }
    const get=key=>fields.get('AWS_KINESISVIDEO_'+key);
    const number=get('FRAGMENT_NUMBER'),token=get('CONTINUATION_TOKEN'),producer=get('PRODUCER_TIMESTAMP'),server=get('SERVER_TIMESTAMP'),lag=get('MILLIS_BEHIND_NOW');
    if(!positive.test(number)||!/^[-_a-zA-Z0-9]{32}$/.test(token)||!seconds.test(producer)||!seconds.test(server)||
       !milliseconds.test(lag)||numbers.has(number)||tokens.has(token))throw Error('Invalid or repeated stored fragment metadata.');
    numbers.add(number);tokens.add(token);result.push({number,token,producer,server,lag,blob:new Blob([data.subarray(pos,segment.end)],{type:'application/octet-stream'})});
    pos=segment.end;
  }
  return result;
}
