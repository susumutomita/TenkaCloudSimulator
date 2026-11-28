import React,{useEffect,useRef,useState} from 'react';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import {limitedBytes,localMediaPath,maxFragmentBytes,readMediaChunks,uploadReceipt} from './kinesis-video-media.mjs';

class Refused extends Error {}
function requireSuccess(response) {
  if(response.status===200)return;
  const code=response.headers.get('x-amzn-errortype')?.split(':')[0];
  const message='HTTP '+response.status+(code&&/^[A-Za-z]+$/.test(code)?' '+code:'');
  if(response.status>=400&&response.status<500)throw new Refused(message);
  throw Error(message);
}
function saveChunk(chunk) {
  const url=URL.createObjectURL(chunk.blob);
  const link=document.createElement('a');
  link.href=url;
  link.download='fragment-'+chunk.number+'.mkv';
  link.click();
  setTimeout(()=>URL.revokeObjectURL(url),1000);
}

export default function KinesisVideoMedia({stream,region,mediaRequest,t}) {
  const active=useRef(null),picker=useRef(null);
  const [file,setFile]=useState(null),[busy,setBusy]=useState(false);
  const [unknown,setUnknown]=useState(false),[notice,setNotice]=useState(''),[error,setError]=useState('');
  const [chunks,setChunks]=useState(null);
  useEffect(()=>()=>{const operation=active.current;active.current=null;operation?.controller.abort()},[]);
  function discard() {
    if(picker.current)picker.current.value='';
    setFile(null);
  }
  function cancel() {
    const operation=active.current;
    if(!operation)return;
    active.current=null;
    operation.controller.abort();
    setBusy(false);
    setChunks(null);
    setError('');
    if(operation.sent){setUnknown(true);discard();setNotice('')}
    else setNotice(t('Transfer canceled.','転送をキャンセルしました。'));
  }
  async function transfer(mode,token) {
    if(active.current || (mode==='upload'&&(!file||unknown)))return;
    const operation={controller:new AbortController(),sent:false};
    active.current=operation;
    setBusy(true);setError('');setNotice('');setChunks(null);
    const headers={'X-Simulator-Service':'kinesisvideo','X-Simulator-Region':region};
    const options={method:'POST',redirect:'error',credentials:'same-origin',signal:operation.controller.signal};
    const jsonHeaders={...headers,'Content-Type':'application/json'};
    try{
      const api=mode==='upload'?'PUT_MEDIA':'GET_MEDIA';
      // Validate the address before sending a file or performing endpoint discovery.
      const origin=window.location.origin;
      localMediaPath(origin,origin,api);
      const discovery=await mediaRequest('/getDataEndpoint',{...options,headers:jsonHeaders,body:JSON.stringify({StreamARN:stream.StreamARN,APIName:api})});
      requireSuccess(discovery);
      const endpoint=await discovery.json();
      const path=localMediaPath(endpoint.DataEndpoint,origin,api);
      if(active.current!==operation)return;
      if(mode==='upload') {
        operation.sent=true;
        const response=await mediaRequest(path,{...options,headers:{...headers,'Content-Type':'application/octet-stream',
          'X-Amzn-Stream-Arn':stream.StreamARN,'X-Amzn-Fragment-Timecode-Type':'RELATIVE',
          'X-Amzn-Producer-Start-Timestamp':(Date.now()/1000).toFixed(3)},body:file});
        requireSuccess(response);
        const receipt=uploadReceipt(await limitedBytes(response,8192));
        if(active.current!==operation)return;
        setNotice(receipt.number?t('Stored fragment ','保存したフラグメント ')+receipt.number:
          t('Fragment received with zero retention.','フラグメントを受信しました。保持期間は 0 です。'));
        discard();
      } else {
        const selector=token?{StartSelectorType:'CONTINUATION_TOKEN',ContinuationToken:token}:{StartSelectorType:'EARLIEST'};
        const response=await mediaRequest(path,{...options,headers:jsonHeaders,body:JSON.stringify({StreamARN:stream.StreamARN,StartSelector:selector})});
        requireSuccess(response);
        if(response.headers.get('Content-Type')?.split(';')[0]!=='video/x-matroska')throw Error(t('Unexpected media response.','メディアの応答形式が正しくありません。'));
        const observed=readMediaChunks(await limitedBytes(response));
        if(active.current!==operation)return;
        setChunks(observed);
        if(!token)setUnknown(false);
        if(!observed.length)setNotice(t('No retained fragments in this read.','今回の読み取りで保持されたフラグメントはありません。'));
      }
    }catch(cause){
      if(active.current!==operation)return;
      if(operation.sent&&!(cause instanceof Refused)){setUnknown(true);discard()}
      setError(cause instanceof Refused?cause.message:t('The transfer could not be confirmed.','転送を確認できませんでした。'));
    }finally{
      if(active.current===operation){active.current=null;setBusy(false)}
    }
  }
  return <Container header={<Header variant="h2">{t('Stream fragments','ストリームのフラグメント')}</Header>}>
    <SpaceBetween size="m">
      <FormField label={t('Matroska file','Matroska ファイル')} controlId="kinesis-fragment-file" description={t('Choose a single H.264 MKV fragment, up to 4 MiB.','H.264 の MKV フラグメントを 1 件選択してください。上限は 4 MiB です。')}>
        <input ref={picker} id="kinesis-fragment-file" type="file" accept=".mkv,video/x-matroska" disabled={busy||unknown}
          style={{maxWidth:'100%'}} onChange={e=>{
            const next=e.target.files[0];
            setError('');setNotice('');
            if(next&&(next.size===0||next.size>maxFragmentBytes)){discard();setError(t('Choose a file between 1 byte and 4 MiB.','1 バイト以上、4 MiB 以下のファイルを選択してください。'));return}
            setFile(next||null);
          }}/>
      </FormField>
      {file&&<span style={{overflowWrap:'anywhere'}}>{file.name} ({file.size} B)</span>}
      <SpaceBetween direction="horizontal" size="xs">
        <Button disabled={busy||!file||unknown} onClick={()=>transfer('upload')}>{t('Upload fragment','フラグメントを送信')}</Button>
        {file&&!busy&&<Button onClick={discard}>{t('Discard file','ファイルの選択を解除')}</Button>}
        <Button disabled={busy} onClick={()=>transfer('read')}>{t('Read earliest fragments','先頭からフラグメントを読み取る')}</Button>
        {busy&&<Button onClick={cancel}>{t('Cancel transfer','転送をキャンセル')}</Button>}
      </SpaceBetween>
      {unknown&&<div role="alert">{t('The upload outcome is unknown. Read the stream before choosing another file.','送信結果は不明です。次のファイルを選択する前にストリームを読み取ってください。')}</div>}
      <div role="status">{error||notice}</div>
      {chunks?.map(chunk=><section key={chunk.number} aria-label={t('Fragment ','フラグメント ')+chunk.number}>
        <Header variant="h3">{t('Fragment ','フラグメント ')+chunk.number}</Header>
        <dl><dt>{t('Producer timestamp','プロデューサーのタイムスタンプ')}</dt><dd>{chunk.producer}</dd>
          <dt>{t('Server timestamp','サーバーのタイムスタンプ')}</dt><dd>{chunk.server}</dd>
          <dt>{t('Milliseconds behind now','現在との差 (ミリ秒)')}</dt><dd>{chunk.lag}</dd></dl>
        <SpaceBetween direction="horizontal" size="xs">
          <Button onClick={()=>saveChunk(chunk)}>{t('Download MKV','MKV をダウンロード')}</Button>
          <Button disabled={busy} onClick={()=>transfer('read',chunk.token)}>{t('Read after this fragment','このフラグメントの次から読み取る')}</Button>
        </SpaceBetween>
      </section>)}
    </SpaceBetween>
  </Container>;
}
