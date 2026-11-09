// Explicit invocation-only log disclosure. Never fetch or persist log history.
export function decodeLogTail(value) {
 if(typeof value!=='string'||value.length>5464||!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value))throw Error('The invocation did not return a valid log tail.');
 const bytes=Uint8Array.from(atob(value),character=>character.charCodeAt(0));
 if(bytes.length>4096)throw Error('The invocation log tail exceeds 4096 bytes.');
 // The API byte limit can start inside a UTF-8 character. Replace that fragment.
 return new TextDecoder('utf-8').decode(bytes);
}

export async function invokeFunction(request,region,name,payload,{includeLogs=false,signal}={}) {
 const response=await request('/2015-03-31/functions/'+encodeURIComponent(name)+'/invocations',{
  method:'POST',body:payload,signal,
  headers:{'Content-Type':'application/json','X-Simulator-Service':'lambda','X-Simulator-Region':region,...(includeLogs?{'X-Amz-Log-Type':'Tail'}:{})},
 });
 return {data:await response.json(),functionError:!!response.headers.get('X-Amz-Function-Error'),
  logTail:includeLogs?decodeLogTail(response.headers.get('X-Amz-Log-Result')):null};
}
