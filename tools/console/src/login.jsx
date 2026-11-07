import React, { useEffect, useRef, useState } from 'react';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import Button from '@cloudscape-design/components/button';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Alert from '@cloudscape-design/components/alert';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import FileInput from '@cloudscape-design/components/file-input';
import { useLocale } from './i18n.jsx';
import './login-session-experience.css';

// Closed presentation hints only. A query parameter is never authentication.
export function loginPresentationReason(search){
  const reasons=new URLSearchParams(search).getAll('reason');
  return reasons.length===1&&['signed-out','session-required'].includes(reasons[0])?reasons[0]:'';
}

// Matches the native server credential alphabet/length and CLI token-file cap.
// The file parser is deliberately one token line, not a command/JSON extractor.
const maxTokenFileBytes=512,tokenPattern=/^[A-Za-z0-9_-]{32,256}$/;

export default function Login(){
  const {locale,setLocale,t}=useLocale();
  const [token,setToken]=useState(''),[error,setError]=useState(null),[busy,setBusy]=useState(false),[fileState,setFileState]=useState('');
  const tokenInput=useRef(null);
  const inputRevision=useRef(0),pendingPaste=useRef(null),pasteTimer=useRef(null),fileRevision=useRef(null),alive=useRef(true),submitting=useRef(false);
  function showError(en,ja){setError({en,ja})}
  function clearPendingPaste(){clearTimeout(pasteTimer.current);pasteTimer.current=null;pendingPaste.current=null}
  useEffect(()=>{alive.current=true;return()=>{alive.current=false;inputRevision.current++;fileRevision.current=null;clearPendingPaste()}},[]);
  function newIntent(){inputRevision.current++;clearPendingPaste();fileRevision.current=null;setFileState('');return inputRevision.current}
  function changeToken(value){newIntent();setToken(value);setError(null)}
  function synchronizeNativeInput(){
    const input=tokenInput.current;
    // Third-party/native input events can update the element without the
    // component's change callback. Adopt that value before keyboard/focus/blur UI
    // updates; never log it or interpret routing metadata as authentication.
    if(input?.isConnected&&input.value!==token)changeToken(input.value);
  }
  function flushPendingPaste(){
    const pending=pendingPaste.current;
    if(pending&&pending.revision===inputRevision.current&&pending.input.isConnected){changeToken(pending.value);return pending.value}
    clearPendingPaste();
    // Submit the visible native control, including an intentional clear. A
    // password-manager/autofill write need not have updated React state.
    const input=tokenInput.current;
    if(input?.isConnected){
      const value=input.value;if(value!==token)changeToken(value);return value;
    }
    return token;
  }
  function capturePaste(event){
    const input=event.target;
    if(!(input instanceof HTMLInputElement)||input.type!=='password')return;
    const revision=newIntent(),clipboard=event.clipboardData,plain=clipboard?.getData('text/plain')||'';
    if(!plain){
      if(clipboard?.types?.length)showError('The clipboard has no plain-text token. Copy the token as plain text and paste again.','クリップボードにプレーンテキストのトークンがありません。テキストとしてコピーし直してください。');
      return;
    }
    const start=input.selectionStart??input.value.length,end=input.selectionEnd??start;
    // Preserve native password CR/LF sanitization and normal editing/undo.
    const value=input.value.slice(0,start)+plain.replace(/[\r\n]/g,'')+input.value.slice(end);
    pendingPaste.current={input,value,revision};pasteTimer.current=setTimeout(flushPendingPaste,0);
  }
  function fileKeyDown(event){
    const input=event.target;
    if(event.key!=='Enter'||!event.isTrusted||!(input instanceof HTMLInputElement)||input.type!=='file'||document.activeElement!==input)return;
    // Keep activation synchronous in the trusted keyboard gesture. Suppress the
    // native Enter default to avoid a second chooser or implicit form submit.
    event.preventDefault();
    if(event.repeat||busy||submitting.current||input.matches(':disabled'))return;
    input.click();
  }
  async function chooseTokenFile(files){
    if(submitting.current)return;
    const revision=newIntent();setError(null);
    if(!files.length)return; // Cancellation preserves the existing input.
    setToken('');
    if(files.length!==1||!(files[0] instanceof File)||files[0].size<32||files[0].size>maxTokenFileBytes){
      showError('Choose one token file of at most 512 bytes containing one token.','512バイト以下で、トークンを1つだけ含むファイルを選んでください。');return;
    }
    const mime=files[0].type.toLowerCase().split(';')[0].trim();
    if(mime&&!['text/plain','application/octet-stream'].includes(mime)){
      showError('Choose a plain-text token file.','プレーンテキストのトークンファイルを選んでください。');return;
    }
    fileRevision.current=revision;setFileState('loading');
    try{
      const bytes=await files[0].arrayBuffer();
      if(!alive.current||inputRevision.current!==revision||fileRevision.current!==revision)return;
      if(bytes.byteLength>maxTokenFileBytes)throw Error('token-file-size');
      const text=new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(bytes);
      const value=text.endsWith('\r\n')?text.slice(0,-2):text.endsWith('\n')?text.slice(0,-1):text;
      if(!tokenPattern.test(value)){
        showError('The file must contain one 32–256 character token, with no label or extra whitespace.','ファイルには32〜256文字のトークンを1つだけ入れてください。ラベルや余分な空白は使えません。');setFileState('');return;
      }
      // Parsing is not authentication. Only the existing sign-in POST decides.
      changeToken(value);setFileState('loaded');
    }catch{
      if(alive.current&&inputRevision.current===revision&&fileRevision.current===revision){showError('Could not read that token file. Choose it again.','トークンファイルを読み取れませんでした。選び直してください。');setFileState('')}
    }finally{
      if(alive.current&&inputRevision.current===revision&&fileRevision.current===revision){fileRevision.current=null;setFileState(current=>current==='loading'?'':current)}
    }
  }
  async function submit(event){
    event.preventDefault();if(submitting.current||fileRevision.current!==null)return;
    const submitted=flushPendingPaste();
    if(!submitted){showError('Enter your access token. If paste left this blank, copy the token as plain text.','アクセストークンを入力してください。貼り付けが空になる場合は、テキストとしてコピーするかファイルを選んでください。');return}
    submitting.current=true;setBusy(true);setError(null);
    try{
      let response;
      try{response=await fetch('/_simulator/login',{method:'POST',body:new URLSearchParams({token:submitted})})}
      catch{if(alive.current)showError('Could not reach this simulator. Check the connection and try again.','シミュレーターに接続できません。接続を確認して再試行してください。');return}
      if(!alive.current)return;
      if(!response.ok){
        if(response.status===401)showError('Invalid or expired access token','トークンが無効、または期限切れです');
        else showError(`Sign-in failed (HTTP ${response.status}). Try again or contact your host.`,`サインインに失敗しました（HTTP ${response.status}）。再試行するか、主催者に確認してください。`);
        return;
      }
      location.assign('/_simulator/console/');
    }finally{submitting.current=false;if(alive.current)setBusy(false)}
  }
  const reason=loginPresentationReason(location.search);
  return <main className="simulator-login"><div className="simulator-login__content">
    <nav className="simulator-login__languages" aria-label={t('Sign-in language','サインイン画面の言語')}><Button formAction="none" disabled={locale==='en'} onClick={()=>setLocale('en')}>English</Button><Button formAction="none" disabled={locale==='ja'} onClick={()=>setLocale('ja')}>日本語</Button></nav>
    <ContentLayout header={<Header variant="h1">{t('Open your local sandbox','ローカルサンドボックスにサインイン')}</Header>}><Container><form onSubmit={submit}>
      <Form errorText={error?t(error.en,error.ja):''}>
        <SpaceBetween size="l">
          {reason&&<Alert type={reason==='signed-out'?'success':'info'}>{reason==='signed-out'?t('You signed out. Enter a token to start a new browser session.','サインアウトしました。新しいブラウザーセッションを開始するには、トークンを入力してください。'):t('Sign in to continue. Your browser session may have ended, or this simulator address may not have a session.','続けるにはサインインしてください。ブラウザーセッションが終了したか、この接続先にセッションがない可能性があります。')}</Alert>}
          <p className="simulator-login__intro">{t('Use the access token supplied by your host. This is an independent local simulator.','主催者から受け取ったアクセストークンを使ってください。これは独立したローカルシミュレーターです。')}</p>
          <SpaceBetween size="s">
            <FormField label={t('Access token','アクセストークン')} description={t('Paste the token itself as plain text. It stays hidden; do not include a command or label.','トークンだけをテキストとして貼り付けてください。入力は隠されます。コマンドやラベルは含めないでください。')}>
              <div onPasteCapture={capturePaste} onInputCapture={synchronizeNativeInput} onFocusCapture={synchronizeNativeInput} onKeyDownCapture={synchronizeNativeInput}><Input value={token} type="password" onChange={({detail})=>changeToken(detail.value)} onBlur={synchronizeNativeInput} autoComplete="off" disableBrowserAutocorrect spellcheck={false} nativeInputAttributes={{ref:tokenInput,name:'simulator-access-token','data-1p-ignore':'true','data-op-ignore':'true'}}/></div>
            </FormField>
            <div className="simulator-login__submit"><Button variant="primary" loading={busy} disabled={fileState==='loading'} formAction="submit">{t('Open sandbox','サンドボックスを開く')}</Button></div>
          </SpaceBetween>
          <FormField label={t('Token file – optional','トークンファイル（任意）')} description={t('If paste does not work, explicitly choose the token file supplied by your host. Only one UTF-8 token line, at most 512 bytes. The file is read locally, not uploaded.','貼り付けできない場合は、主催者から受け取ったトークンファイルを選べます。512バイト以下のUTF-8テキスト1行だけを使います。ファイルはブラウザー内で読み取り、アップロードしません。')}>
            <fieldset disabled={busy} onKeyDown={fileKeyDown} aria-label={t('Choose a local token file','ローカルのトークンファイルを選択')} style={{border:0,padding:0,margin:0}}><FileInput value={[]} multiple={false} onChange={({detail})=>chooseTokenFile(detail.value)}>{t('Choose token file','トークンファイルを選択')}</FileInput></fieldset>
            <div role="status" aria-live="polite">{fileState==='loading'?t('Reading token file…','トークンファイルを読み取り中…'):fileState==='loaded'?t('Token loaded. Choose Open sandbox to verify it.','トークンを読み込みました。「サンドボックスを開く」で確認してください。'):''}</div>
          </FormField>
          <p className="simulator-login__session-note">{t('Operator sessions can renew during explicit actions: 15 minutes idle and at most 60 minutes from sign-in. Participant sessions keep their original 15-minute parent limit. Restart or revocation can end access sooner; use the same simulator address.','操作者は明示的な操作時に更新でき、無操作15分・ログインから最長60分です。参加者は元の親トークンの15分期限を超えません。再起動や失効で早く終了する場合があります。同じ接続先を使ってください。')}</p>
        </SpaceBetween>
      </Form>
    </form></Container></ContentLayout>
  </div></main>;
}
