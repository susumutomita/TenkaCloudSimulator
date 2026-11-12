// Simulator-specific bounded terminal transport; not the AWS session-manager-plugin protocol.
const maxInput = 8192;
const maxOutput = 1024 * 1024;
function encode(bytes) { let text = ''; for (const byte of bytes) text += String.fromCharCode(byte); return btoa(text); }
function decode(text) {
  if (typeof text !== 'string' || text.length > 10924 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(text)) throw Error('Invalid terminal output.');
  const bytes = Uint8Array.from(atob(text), c => c.charCodeAt(0));
  if (bytes.length > maxInput) throw Error('Terminal output exceeds frame limit.');
  return bytes;
}
export function createTerminalClient({request, socket, origin, onOutput, onState}) {
  let session = null, channel = null, phase = 'idle', busy = false, output = 0, revision = 0, disposed = false, operationEpoch = 0;
  const decoder = new TextDecoder();
  const state = next => { if (!disposed) { phase = next; onState(next); } };
  const available = () => { if (disposed) throw Error('Terminal client disposed; remote outcome may be unknown.'); };
  async function native(action, body) {
    const response = await request('/_simulator/v1/terminal-control', {method:'POST', credentials:'same-origin', redirect:'error', signal:AbortSignal.timeout(10000), headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':`AmazonSSM.${action}`}, body:JSON.stringify(body)});
    if (!response.ok) throw Error('Terminal operation refused or outcome unknown.');
    if (!response.body) throw Error('Invalid terminal response.');
    const reader=response.body.getReader(), chunks=[];let length=0;
    try {for (;;) {const next=await reader.read();if(next.done)break;length+=next.value.length;if(length>16384)throw Error('Invalid terminal response.');chunks.push(next.value);}}finally{await reader.cancel();}
    const bytes=new Uint8Array(length);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.length;}
    try{return JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(bytes));}catch{throw Error('Invalid terminal response.');}
  }
  function disconnect() { revision++; const old=channel; channel=null; old?.close(); }
  function connect(receipt) {
    available();
    if (!receipt || typeof receipt.SessionId !== 'string' || !/^[A-Za-z0-9_:.@+=,-]{1,256}$/.test(receipt.SessionId) || typeof receipt.TokenValue !== 'string' || receipt.TokenValue.length < 16 || receipt.TokenValue.length > 4096) throw Error('Invalid terminal receipt.');
    // Retain known session identity even if transport validation/connection fails.
    session=receipt.SessionId;
    const url=new URL(receipt.StreamUrl,origin), base=new URL(origin);
    if (url.protocol !== (base.protocol==='https:'?'wss:':'ws:') || url.host!==base.host || url.username || url.password || url.search || url.hash || decodeURIComponent(url.pathname)!==`/_simulator/v1/terminal-channel/${session}`) throw Error('Invalid terminal channel destination.');
    disconnect(); const current=revision;
    const ws=socket(url.href); channel=ws; state('connecting');
    const active = () => !disposed && current===revision && channel===ws;
    let opened=false;
    ws.onopen=()=>{if(active() && phase==='connecting' && !opened) { opened=true; ws.send(JSON.stringify({type:'open',token:receipt.TokenValue})); }};
    ws.onmessage=event=>{
      if(!active())return;
      try {
        if(typeof event.data!=='string'||event.data.length>12000)throw Error('Invalid terminal frame.');
        const frame=JSON.parse(event.data);
        if(frame.type==='ready'&&Object.keys(frame).length===1&&phase==='connecting'){state('open');return;}
        if(frame.type!=='output'||Object.keys(frame).sort().join(',')!=='data,type'||phase!=='open')throw Error('Invalid terminal frame.');
        const bytes=decode(frame.data); output+=bytes.length;
        if(output>maxOutput)throw Error('Terminal output limit reached.');
        onOutput(decoder.decode(bytes,{stream:true}));
      }catch{disconnect();state('disconnected');}
    };
    ws.onerror=()=>{if(active()){disconnect();state('disconnected');}};
    ws.onclose=()=>{if(active()){disconnect();state('disconnected');}};
  }
  async function operation(action, body) {
    available();
    if(busy)throw Error('Terminal operation already pending.'); busy=true;
    const epoch=++operationEpoch;
    try {
      const receipt=await native(action,body);
      available();
      if(epoch!==operationEpoch)throw Error('Terminal operation superseded; remote outcome may be unknown.');
      connect(receipt);
    } catch(error){if(!disposed && epoch===operationEpoch)state('unknown');throw error;} finally{busy=false;}
  }
  return {
    start(target){available();if(phase!=='idle')throw Error('Start outcome already retained.');return operation('StartSession',{Target:target,DocumentName:'SSM-SessionManagerRunShell'});},
    resume(){available();if(!session||busy||phase==='open'||phase==='connecting')throw Error('No disconnected session to resume.');return operation('ResumeSession',{SessionId:session});},
    input(text){available();const data=new TextEncoder().encode(text);if(phase!=='open'||data.length>maxInput)throw Error('Terminal is not ready or input exceeds limit.');channel.send(JSON.stringify({type:'input',data:encode(data)}));},
    resize(rows,cols){available();if(phase!=='open'||!Number.isInteger(rows)||rows<1||rows>300||!Number.isInteger(cols)||cols<1||cols>500)throw Error('Invalid terminal size.');channel.send(JSON.stringify({type:'resize',rows,cols}));},
    async terminate(){available();if(!session||busy)throw Error('No known session to terminate.');busy=true;disconnect();const epoch=++operationEpoch;try{await native('TerminateSession',{SessionId:session});available();if(epoch!==operationEpoch)throw Error('Terminal operation superseded; remote outcome may be unknown.');state('terminated');}catch(error){if(!disposed && epoch===operationEpoch)state('unknown');throw error;}finally{busy=false;}},
    dispose(){if(disposed)return;disposed=true;operationEpoch++;disconnect();phase='disconnected';},
    get disposed(){return disposed;}, get sessionId(){return session;}, get phase(){return phase;}
  };
}
