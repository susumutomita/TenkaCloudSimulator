import React, {useEffect,useRef,useState} from 'react';
import {createTerminalClient} from './ssm-terminal-client.mjs';

// Output remains inert text. This bounded terminal does not interpret escape
// sequences, execute HTML, or expose the one-use data-channel token to the URL.
export default function SSMTerminal() {
  const [context,setContext]=useState(null), [phase,setPhase]=useState('idle');
  const [output,setOutput]=useState(''), [input,setInput]=useState(''), [error,setError]=useState('');
  const [rows,setRows]=useState(24), [cols,setCols]=useState(80);
  const client=useRef(null);
  useEffect(()=>{
    let active=true;
    const connection=createTerminalClient({request:fetch,socket:url=>new WebSocket(url),origin:location.origin,
      onOutput:text=>{if(active)setOutput(old=>(old+text).slice(-1048576));},onState:value=>{if(active)setPhase(value);}});
    client.current=connection;
    fetch('/_simulator/v1/terminal-context',{credentials:'same-origin',redirect:'error'}).then(async response=>{
      if(!response.ok)throw Error('A participant terminal handoff is required.');
      const value=await response.json();
      if(typeof value.Target!=='string'||!/^i-[a-f0-9]+$/.test(value.Target)||typeof value.Region!=='string')throw Error('Invalid terminal scope.');
      if(active)setContext(value);
    }).catch(e=>{if(active)setError(e.message);});
    return()=>{active=false;connection.dispose();client.current=null;};
  },[]);
  async function act(action){
    const connection=client.current;
    if(!connection)return;
    setError('');
    try{await action(connection);}catch(e){if(client.current===connection)setError(e.message);}
  }
  async function signOut(connection){
    connection.dispose();
    if(client.current!==connection)return;
    setPhase('disconnected');
    const response=await fetch('/_simulator/logout',{method:'POST',credentials:'same-origin',redirect:'error'});
    if(client.current!==connection)return;
    if(!response.ok)throw Error('Sign out failed. The local connection is closed; remote session termination is not confirmed.');
    location.assign('/_simulator/terminal');
  }
  return <main style={{padding:24,maxWidth:1000,margin:'auto'}}>
    <h1>Local guest terminal / ローカルゲスト端末</h1>
    <p>Participant-scoped Simulator session. Not AWS Session Manager plugin compatible. Closing this page disconnects; use Terminate to end the session.</p>
    <button type="button" onClick={()=>act(signOut)}>Sign out / サインアウト</button>
    <p>{context ? `${context.Target} · ${context.Region}` : 'Checking participant authority…'} · {phase}</p>
    {error&&<p role="alert">{error}</p>}
    <button type="button" disabled={client.current?.disposed||!context||phase!=='idle'} onClick={()=>act(connection=>connection.start(context.Target))}>Start / 開始</button>{' '}
    <button type="button" disabled={client.current?.disposed||!client.current?.sessionId||!['disconnected','unknown'].includes(phase)} onClick={()=>act(connection=>connection.resume())}>Resume / 再接続</button>{' '}
    <button type="button" disabled={client.current?.disposed||!client.current?.sessionId||phase==='terminated'} onClick={()=>act(connection=>connection.terminate())}>Terminate / 終了</button>
    <pre aria-label="Terminal output" tabIndex={0} style={{whiteSpace:'pre-wrap',overflow:'auto',height:400,border:'1px solid',padding:12}}>{output}</pre>
    <form onSubmit={event=>{event.preventDefault();act(connection=>{connection.input(input+'\n');if(client.current===connection)setInput('');});}}>
      <label>Input / 入力 <textarea value={input} maxLength={8192} disabled={phase!=='open'} onChange={event=>setInput(event.target.value)}/></label>
      <button type="submit" disabled={phase!=='open'}>Send / 送信</button>
    </form>
    <label>Rows <input type="number" min={1} max={300} value={rows} onChange={event=>setRows(Number(event.target.value))}/></label>
    <label>Columns <input type="number" min={1} max={500} value={cols} onChange={event=>setCols(Number(event.target.value))}/></label>
    <button type="button" disabled={phase!=='open'} onClick={()=>act(connection=>connection.resize(rows,cols))}>Resize</button>
  </main>;
}
