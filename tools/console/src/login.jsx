import React, { useEffect, useRef, useState } from 'react';
import Input from '@cloudscape-design/components/input';
import FileInput from '@cloudscape-design/components/file-input';
import { useLocale } from './i18n.jsx';
import './login.css';

// Matches the native server credential alphabet/length and CLI token-file cap.
// The file parser is deliberately one token line, not a command/JSON extractor.
const maxTokenFileBytes=512,tokenPattern=/^[A-Za-z0-9_-]{32,256}$/;

// Capture once during module startup and erase before rendering or network I/O.
const initialHandoff=location.pathname==='/_simulator/login'?new URLSearchParams(location.hash.slice(1)).get('handoff'):null;
if(initialHandoff!==null)history.replaceState(null,'',location.pathname);

export default function Login(){
  const {locale,setLocale,t}=useLocale();
  const [token,setToken]=useState(''),[error,setError]=useState(null),[busy,setBusy]=useState(false),[fileState,setFileState]=useState('');
  const tokenInput=useRef(null);
  const inputRevision=useRef(0),pendingPaste=useRef(null),pasteTimer=useRef(null),fileRevision=useRef(null),alive=useRef(true),submitting=useRef(false);
  function showError(en,ja){setError({en,ja})}
  useEffect(()=>{
    if(initialHandoff===null)return;
    submitting.current=true;setBusy(true);
    (async()=>{
      try{
        if(!/^[a-f0-9]{64}$/.test(initialHandoff))throw Error('handoff');
        const response=await fetch('/_simulator/login',{method:'POST',credentials:'same-origin',headers:{Accept:'application/json'},body:new URLSearchParams({handoff:initialHandoff})});
        if(!response.ok)throw Error('handoff');
        const result=await response.json();
        if(typeof result.Destination!=='string'||!/^\/_simulator\/console\/#\/home\?region=(?:af|ap|ca|eu|il|me|mx|sa|us)-[a-z]+-\d{1,2}$/.test(result.Destination))throw Error('destination');
        location.replace(result.Destination);
      }catch{showError('This console link expired or could not be used. Return to TenkaCloud and open a new console link.','コンソールリンクを使用できません。TenkaCloudに戻り、新しいリンクを開いてください。');submitting.current=false;setBusy(false)}
    })();
  },[]);

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
  // Presentation follows TenkaCloud's ConsoleAuthShell / Summit brand. Keep
  // the simulator's token, paste, file and one-use handoff flows above intact.
  return <div className="tc-simulator-login">
    <aside className="tc-login-stage" aria-label="TenkaCloud Simulator">
      <div className="tc-login-brand">
        <svg width="24" height="24" viewBox="0 0 120 120" aria-hidden="true" focusable="false"><rect x="26" y="24" width="68" height="12" rx="6" fill="currentColor"/><path d="M26 90 L60 48 L94 90" fill="none" stroke="currentColor" strokeWidth="13" strokeLinecap="round" strokeLinejoin="round"/></svg>
        <span>Tenka<span className="tc-login-brand-sub">Cloud</span></span>
        <span className="tc-login-product">Simulator</span>
      </div>
      <div className="tc-login-stage-center">
        <p className="tc-login-eyebrow">LOCAL CLOUD PRACTICE</p>
        <h2>{t('Your next cloud challenge.','クラウドの挑戦を、')}<em>{t('Start locally.','ローカルから。')}</em></h2>
        <p className="tc-login-lede">{t('Practice cloud APIs and console workflows without a cloud account. Bring your TenkaCloud learning to a local sandbox.','クラウドアカウントなしで、APIとコンソールの操作を練習。TenkaCloudの学びを、手元の環境へ。')}</p>
        <div className="tc-login-environment">
          <div className="tc-login-environment-title">LOCAL SANDBOX</div>
          <dl>
            <div><dt>{t('Environment','実行環境')}</dt><dd>{t('Local simulator','ローカルシミュレーター')}</dd></div>
            <div><dt>{t('Your workflow','操作方法')}</dt><dd>Console / SDK / CLI</dd></div>
            <div><dt>{t('Your data','利用データ')}</dt><dd>{t('Practice data only','練習用データのみ')}</dd></div>
          </dl>
        </div>
      </div>
      <p className="tc-login-stage-foot">{t('Learn. Build. Challenge.','学ぶ。つくる。挑戦する。')}</p>
    </aside>
    <main className="tc-login-panel">
      <div className="tc-login-topbar">
        <div className="tc-login-language" role="group" aria-label={t('Language','表示言語')}>
          <button type="button" lang="ja" aria-label="日本語" aria-pressed={locale==='ja'} onClick={()=>setLocale('ja')}>JA</button>
          <button type="button" lang="en" aria-label="English" aria-pressed={locale==='en'} onClick={()=>setLocale('en')}>EN</button>
        </div>
      </div>
      <div className="tc-login-form-wrap">
        <p className="tc-login-kicker">TENKACLOUD SIMULATOR</p>
        <h1>{t('Open your local sandbox','ローカルサンドボックスにサインイン')}</h1>
        <p className="tc-login-subtitle">{t('Use the access token supplied by your host.','主催者から受け取ったアクセストークンを使ってください。')}</p>
        <form onSubmit={submit} aria-busy={busy}>
          <div className="tc-login-field">
            <label htmlFor="simulator-access-token">{t('Access token','アクセストークン')}</label>
            <div onPasteCapture={capturePaste} onInputCapture={synchronizeNativeInput} onFocusCapture={synchronizeNativeInput} onKeyDownCapture={synchronizeNativeInput}>
              <Input controlId="simulator-access-token" value={token} type="password" ariaDescribedby="simulator-token-help" onChange={({detail})=>changeToken(detail.value)} onBlur={synchronizeNativeInput} autoComplete="off" disableBrowserAutocorrect spellcheck={false} nativeInputAttributes={{ref:tokenInput,name:'simulator-access-token','data-1p-ignore':'true','data-op-ignore':'true'}} style={{root:{borderRadius:'12px',borderWidth:'1px',borderColor:{default:'#d2d2d7',hover:'#6e6e73',focus:'#1d1d1f'},boxShadow:{focus:'0 0 0 3px rgba(29,29,31,.18)'},fontSize:'16px',paddingBlock:'14px',paddingInline:'16px',color:{default:'#1d1d1f',focus:'#1d1d1f'}}}}/>
            </div>
            <p id="simulator-token-help" className="tc-login-help">{t('Paste the token itself as plain text. It stays hidden; do not include a command or label.','トークンだけをテキストとして貼り付けてください。入力は隠されます。コマンドやラベルは含めないでください。')}</p>
          </div>
          <div className="tc-login-file">
            <label id="simulator-token-file-label" className="tc-login-file-label" htmlFor="simulator-token-file">{t('Token file – optional','トークンファイル（任意）')}</label>
            <fieldset disabled={busy} onKeyDown={fileKeyDown} aria-label={t('Choose a local token file','ローカルのトークンファイルを選択')}>
              <FileInput controlId="simulator-token-file" ariaLabelledby="simulator-token-file-label" ariaDescribedby="simulator-token-file-help" value={[]} multiple={false} onChange={({detail})=>chooseTokenFile(detail.value)}>{t('Choose token file','トークンファイルを選択')}</FileInput>
            </fieldset>
            <p id="simulator-token-file-help" className="tc-login-help">{t('If paste does not work, explicitly choose the token file supplied by your host. Only one UTF-8 token line, at most 512 bytes. The file is read locally, not uploaded.','貼り付けできない場合は、主催者から受け取ったトークンファイルを選べます。512バイト以下のUTF-8テキスト1行だけを使います。ファイルはブラウザー内で読み取り、アップロードしません。')}</p>
            <div className="tc-login-file-status" role="status" aria-live="polite">{fileState==='loading'?t('Reading token file…','トークンファイルを読み取り中…'):fileState==='loaded'?t('Token loaded. Choose Open sandbox to verify it.','トークンを読み込みました。「サンドボックスを開く」で確認してください。'):''}</div>
          </div>
          {error&&<div className="tc-login-error" role="alert"><span aria-hidden="true">!</span><p>{t(error.en,error.ja)}</p></div>}
          <button className="tc-login-submit" type="submit" disabled={busy||fileState==='loading'}>
            {busy?<span className="tc-login-spinner" aria-hidden="true"/>:null}
            {t('Open sandbox','サンドボックスを開く')}
            {!busy&&<svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true" focusable="false"><path d="M5 12h14m-6-6 6 6-6 6" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"/></svg>}
          </button>
        </form>
        <div className="tc-login-progress" role="status" aria-live="polite">{busy?t('Signing in…','サインイン中…'):''}</div>
        <div className="tc-login-note"><svg width="16" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true" focusable="false"><rect x="5" y="10" width="14" height="11" rx="3" stroke="currentColor" strokeWidth="1.5"/><path d="M8 10V7a4 4 0 0 1 8 0v3m-4 4v3" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/></svg><p>{t('This is an independent local simulator. Use dummy credentials and practice data, never real cloud credentials.','これは独立したローカルシミュレーターです。実際のクラウド認証情報は使わず、ダミーの認証情報と練習用データを使用してください。')}</p></div>
        <footer className="tc-login-footer">TenkaCloud <span>Simulator</span></footer>
      </div>
    </main>
  </div>;
}
