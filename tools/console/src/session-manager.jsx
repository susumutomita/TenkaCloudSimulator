import React, {useEffect, useRef, useState} from 'react';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Table from '@cloudscape-design/components/table';
import Button from '@cloudscape-design/components/button';
import Alert from '@cloudscape-design/components/alert';
import Container from '@cloudscape-design/components/container';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Textarea from '@cloudscape-design/components/textarea';
import {useLocale} from './i18n.jsx';
import {createTerminalClient} from './ssm-terminal-client.mjs';
import {READINESS_INTERVAL_MS, canStart, connectionState, validReadiness} from './session-manager-state.mjs';

// Systems Manager -> Session Manager for the one instance of this console
// session. The terminal channel and its limits are the existing participant
// terminal client; output stays inert text and reconnecting is explicit.
export default function SessionManager({scope}) {
  const {t}=useLocale();
  const [readiness,setReadiness]=useState(null),[phase,setPhase]=useState('idle');
  const [output,setOutput]=useState(''),[input,setInput]=useState(''),[error,setError]=useState('');
  const [selected,setSelected]=useState([{id:scope.instanceId}]);
  const client=useRef(null);
  useEffect(()=>{
    let active=true;
    const connection=createTerminalClient({request:fetch,socket:url=>new WebSocket(url),origin:location.origin,
      onOutput:text=>{if(active)setOutput(old=>(old+text).slice(-1048576));},onState:value=>{if(active)setPhase(value);}});
    client.current=connection;
    return()=>{active=false;connection.dispose();client.current=null;};
  },[]);
  useEffect(()=>{
    let active=true,timer;
    async function poll(){
      let next='unavailable';
      try{
        const response=await fetch('/_simulator/v1/terminal-readiness',{credentials:'same-origin',redirect:'error',cache:'no-store'});
        if(response.ok){const value=await response.json();if(validReadiness(value,scope))next=value;}
      }catch{/* reported as unavailable; the next poll retries */}
      if(!active)return;
      setReadiness(next);
      timer=setTimeout(poll,READINESS_INTERVAL_MS);
    }
    poll();
    return()=>{active=false;clearTimeout(timer);};
  },[scope.instanceId,scope.region]);
  async function act(action){
    const connection=client.current;
    if(!connection)return;
    setError('');
    try{await action(connection);}catch(e){if(client.current===connection)setError(e.message);}
  }
  const state=connectionState(readiness);
  const status=state==='ready'?<StatusIndicator type="success">{t('Connectable','接続できます')}</StatusIndicator>
    :state==='preparing'?<StatusIndicator type="in-progress">{t('Preparing','準備中')}</StatusIndicator>
    :state==='unavailable'?<StatusIndicator type="error">{t('Not connectable','接続できません')}</StatusIndicator>
    :<StatusIndicator type="loading">{t('Checking','確認中')}</StatusIndicator>;
  const session=phase!=='idle';
  return <ContentLayout header={<Header variant="h1">Session Manager</Header>}><SpaceBetween size="l">
    {state==='preparing'&&<Alert type="info" header={t('The server is preparing','サーバーを準備しています')}>{t('This instance is not connectable yet because its guest has not answered. Start session becomes available when it does. No URL registration is needed.','このインスタンスは、ゲストがまだ応答していないため接続できません。応答すると「セッションを開始」が押せるようになります。URL の登録は不要です。')}</Alert>}
    {state==='unavailable'&&<Alert type="warning" header={t('Interactive sessions are unavailable','対話セッションを利用できません')}>{t('The Simulator did not report this instance as connectable. It retries automatically.','Simulator がこのインスタンスを接続可能と報告していません。自動で再確認します。')}</Alert>}
    {error&&<Alert type="error">{error}</Alert>}
    <Table trackBy="id" selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>setSelected(detail.selectedItems)}
      ariaLabels={{selectionGroupLabel:t('Select an instance','インスタンスを選択'),itemSelectionLabel:(_,item)=>t('Select ','選択: ')+item.id}}
      items={[{id:scope.instanceId,region:scope.region}]}
      columnDefinitions={[{id:'id',header:t('Instance ID','インスタンスID'),cell:item=><span className="resource-identifier">{item.id}</span>},{id:'region',header:t('Region','リージョン'),cell:item=>item.region},{id:'status',header:t('Status','状態'),cell:()=>status}]}
      header={<Header actions={<Button variant="primary" disabled={!canStart(state,phase)||selected.length===0} onClick={()=>act(connection=>connection.start(scope.instanceId))}>{t('Start session','セッションを開始')}</Button>}>{t('Start a session','セッションを開始')}</Header>}/>
    {session&&<Container header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button disabled={!client.current?.sessionId||!['disconnected','unknown'].includes(phase)} onClick={()=>act(connection=>connection.resume())}>{t('Reconnect','再接続')}</Button>
      <Button disabled={!client.current?.sessionId||phase==='terminated'} onClick={()=>act(connection=>connection.terminate())}>{t('Terminate','終了')}</Button></SpaceBetween>}>{scope.instanceId} · {phase}</Header>}>
      <SpaceBetween size="s">
        <pre aria-label={t('Terminal output','端末の出力')} tabIndex={0} style={{whiteSpace:'pre-wrap',overflow:'auto',height:400,margin:0}}>{output}</pre>
        <form onSubmit={event=>{event.preventDefault();act(connection=>{connection.input(input+'\n');if(client.current===connection)setInput('');});}}>
          <SpaceBetween size="xs">
            <Textarea value={input} rows={2} ariaLabel={t('Input','入力')} disabled={phase!=='open'} onChange={({detail})=>setInput(detail.value.slice(0,8192))}/>
            <Button formAction="submit" disabled={phase!=='open'}>{t('Send','送信')}</Button>
          </SpaceBetween>
        </form>
      </SpaceBetween></Container>}
  </SpaceBetween></ContentLayout>;
}
