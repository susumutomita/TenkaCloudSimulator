import React,{useLayoutEffect,useMemo,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Textarea from '@cloudscape-design/components/textarea';
import {useLocale} from './i18n.jsx';
import {contentClient,encodeBytes,maxFileSize,objectId,repositoryPath} from './codecommit-content.mjs';

const blank = {names:[],next:'',empty:false,branch:'',tip:'',commit:null,file:null};
const option = value => value ? {label:value,value} : null;
const denied = e => /^(401|403):|Sign in to continue/.test(e.message);

export default function CodeCommitContent({request,region,repository,onSaving}) {
  const {t}=useLocale();
  const api=useMemo(()=>contentClient(request,region,repository.repositoryName,repository.repositoryId),
    [request,region,repository.repositoryName,repository.repositoryId]);
  const [view,setView]=useState(blank),[working,setWorking]=useState('');
  const [error,setError]=useState(''),[message,setMessage]=useState(''),[unknown,setUnknown]=useState(null);
  const [path,setPath]=useState(''),[revision,setRevision]=useState(''),[fork,setFork]=useState('');
  const [draft,setDraft]=useState(null);
  const active=useRef(null),saving=useRef(onSaving);
  saving.current=onSaving;

  function resetView() {
    setView(blank);setDraft(null);setPath('');setRevision('');setFork('');
  }
  async function run(kind,work,accept,reject) {
    if(active.current?.kind==='write')return;
    active.current?.controller.abort();
    const job={kind,controller:new AbortController()};
    active.current=job;setWorking(kind);setError('');setMessage('');
    if(kind==='write')saving.current(true);
    try {
      const result=await work(job.controller.signal);
      if(active.current===job)accept(result);
    } catch(e) {
      if(active.current===job) {
        if(denied(e)||e.message.includes('selected repository has changed'))resetView();
        setError(e.message);
        reject?.(e);
      }
    } finally {
      if(active.current===job){active.current=null;setWorking('');if(kind==='write')saving.current(false);}
    }
  }
  async function loadBranch(name,signal) {
    const tip=await api.branch(name,signal);
    const commit=await api.commit(tip,signal);
    return {branch:name,tip,commit,file:null};
  }
  function refresh(affected=null) {
    const wanted=affected?.name || view.branch;
    setDraft(null);setRevision('');setFork('');setView(blank);
    run('read',async signal=>{
      const metadata=await api.identity(signal);
      let page=await api.branches('',signal);
      const names=[...page.branches],tokens=new Set();
      while(wanted && !names.includes(wanted) && page.nextToken){
        if(tokens.has(page.nextToken) || names.length>=32)throw Error('Invalid branch continuation.');
        tokens.add(page.nextToken);
        page=await api.branches(page.nextToken,signal);names.push(...page.branches);
      }
      if(!names.length && (metadata.defaultBranch || page.nextToken))throw Error('Invalid empty repository response.');
      const name=wanted && names.includes(wanted)?wanted:
        names.includes(metadata.defaultBranch)?metadata.defaultBranch:names[0];
      let data={...blank,names,next:page.nextToken||'',empty:!names.length};
      if(name)Object.assign(data,await loadBranch(name,signal));
      if(affected?.path && affected.name===name) {
        try {data.file=await api.file(data.tip,affected.path,signal);}
        catch(e){if(!/^400: .*file does not exist/i.test(e.message))throw e;}
      }
      await api.identity(signal);return data;
    },data=>{setView(data);if(affected){setPath(affected.path||'');setUnknown(null);setMessage(t('Current repository state refreshed.','現在のリポジトリ状態を再取得しました。'));}});
  }
  useLayoutEffect(()=>{
    setUnknown(null);setPath('');refresh();
    return ()=>{
      const job=active.current;active.current=null;job?.controller.abort();
      if(job?.kind==='write')saving.current(false);
    };
  },[api]);

  function select(name) {
    setDraft(null);setFork('');setPath('');setRevision('');
    setView({...view,branch:name,tip:'',commit:null,file:null});
    run('read',async signal=>{await api.identity(signal);const item=await loadBranch(name,signal);await api.identity(signal);return item;},
      item=>setView(v=>({...v,...item})));
  }
  function readCommit(id) {
    setDraft(null);setView({...view,commit:null,file:null});
    run('read',async signal=>{await api.identity(signal);const item=await api.commit(id,signal);await api.identity(signal);return item;},
      commit=>setView(v=>({...v,commit,file:null})));
  }
  function readFile() {
    const id=view.commit.commitId,target=path;
    setView({...view,file:null});
    run('read',async signal=>{await api.identity(signal);const item=await api.file(id,target,signal);await api.identity(signal);return item;},
      file=>setView(v=>({...v,file})));
  }
  function nextPage() {
    run('read',async signal=>{await api.identity(signal);const page=await api.branches(view.next,signal);await api.identity(signal);return page;},
      page=>setView(v=>({...v,names:[...v.names,...page.branches],next:page.nextToken||''})));
  }
  function edit() {
    setError('');setMessage('');
    setDraft({path,branch:view.empty?'main':view.branch,parent:view.tip,mode:view.file?.fileMode||'NORMAL',
      text:view.file?.text??'',upload:null,binary:view.file?.text===null,message:''});
  }
  function changeDraft(key,value) {setDraft(d=>({...d,[key]:value}));}
  function write(isFork) {
    const input=isFork?{branchName:fork,commitId:view.commit.commitId}:{
      branchName:draft.branch,filePath:draft.path,fileMode:draft.mode,commitMessage:draft.message,
      ...(draft.parent?{parentCommitId:draft.parent}:{})
    };
    let sent=false;
    run('write',async signal=>{
      if(!isFork){
        const bytes=draft.upload?new Uint8Array(await draft.upload.arrayBuffer()):new TextEncoder().encode(draft.text);
        if(bytes.length>maxFileSize)throw Error(t('Files must be 64 KiB or smaller.','ファイルは 64 KiB 以下にしてください。'));
        input.fileContent=encodeBytes(bytes);
      }
      return api.change(input,isFork,signal,()=>{sent=true;});
    },saved=>{
      setView(v=>({...v,empty:false,branch:input.branchName,tip:saved.commit.commitId,commit:saved.commit,file:saved.file,
        names:v.names.includes(input.branchName)?v.names:[...v.names,input.branchName]}));
      setPath(input.filePath||'');setRevision('');setDraft(null);setFork('');
      setMessage(isFork?t('Branch created and verified.','ブランチの作成を確認しました。'):t('Commit saved and verified.','コミットの保存を確認しました。'));
    },e=>{if(sent && !e.writeRejected){
      setUnknown({name:input.branchName,path:input.filePath});setDraft(null);setView(blank);
    }});
  }
  function download() {
    const file=view.file,url=URL.createObjectURL(new Blob([file.bytes]));
    const link=document.createElement('a');link.href=url;link.download=file.filePath.split('/').pop();link.click();
    setTimeout(()=>URL.revokeObjectURL(url),1000);
  }
  const locked=!!unknown || working==='write', available=!locked&&!working;
  const textInput=(label,value,onChange,props={})=><FormField label={label}><Input value={value} disabled={locked} onChange={e=>onChange(e.detail.value)} {...props}/></FormField>;
  const button=(label,handler,disabled=false,props={})=><Button disabled={disabled} onClick={handler} {...props}>{label}</Button>;
  return <section data-testid="codecommit-contents"><Container header={<Header variant="h2">{t('Repository contents','リポジトリの内容')}</Header>}>
    <SpaceBetween size="m">
      {error&&<Alert type="error">{error}</Alert>}
      {message&&<StatusIndicator type="success">{message}</StatusIndicator>}
      {unknown&&<Alert type="warning">{t('The write outcome is unknown. Refresh the affected branch before making another change.','書き込み結果を確認できません。次の変更の前に対象ブランチを再取得してください。')} <strong>{unknown.name}</strong></Alert>}
      <SpaceBetween size="xs" direction="horizontal">
        {button(t('Refresh repository','リポジトリを再取得'),()=>refresh(unknown),working==='write')}
        {working==='read'&&button(t('Cancel read','読み込みをキャンセル'),()=>{const job=active.current;active.current=null;job?.controller.abort();setWorking('');})}
      </SpaceBetween>
      {working==='read'&&<StatusIndicator type="loading">{t('Reading repository','読み込み中')}</StatusIndicator>}
      {!!view.names.length&&<FormField label={t('Branch','ブランチ')}><Select selectedOption={option(view.branch)} options={view.names.map(option)}
        disabled={locked||!!draft} onChange={e=>select(e.detail.selectedOption.value)}/></FormField>}
      {view.next&&button(t('Load more branches','ブランチをさらに表示'),nextPage,!available||!!draft)}
      {view.empty&&<Box>{t('This repository has no branches. Commit the first file to create one.','ブランチがありません。最初のファイルをコミットするとブランチが作成されます。')}</Box>}
      {view.commit&&<KeyValuePairs columns={1} items={[
        {label:t('Commit ID','コミット ID'),value:<code>{view.commit.commitId}</code>},
        {label:t('Tree ID','ツリー ID'),value:<code>{view.commit.treeId}</code>},
        {label:t('Commit message','コミットメッセージ'),value:view.commit.message||'—'},
        ...['author','committer'].map((key,i)=>({label:i?t('Committer','コミッター'):t('Author','作成者'),value:view.commit[key].name+' <'+view.commit[key].email+'> · '+view.commit[key].date})),
        {label:t('Parent commit','親コミット'),value:view.commit.parents[0]?
          button(view.commit.parents[0],()=>readCommit(view.commit.parents[0]),locked||!!draft,{variant:'inline-link'}):'—'}
      ]}/>}
      {view.tip&&<SpaceBetween size="xs">
        {textInput(t('Commit to inspect','表示するコミット ID'),revision,setRevision,{disabled:locked||!!draft})}
        <SpaceBetween size="xs" direction="horizontal">
          {button(t('View commit','コミットを表示'),()=>readCommit(revision),!objectId(revision)||locked||!!draft)}
          {view.commit?.commitId!==view.tip&&button(t('Return to branch tip','ブランチの先頭に戻る'),()=>select(view.branch),locked||!!draft)}
        </SpaceBetween>
      </SpaceBetween>}
      {view.commit&&!draft&&<SpaceBetween size="xs">
        {textInput(t('File path','ファイルパス'),path,value=>{if(active.current?.kind==='read'){active.current.controller.abort();active.current=null;setWorking('');}
          setPath(value);setView(v=>({...v,file:null}));})}
        {button(t('Read file','ファイルを読み込む'),readFile,!repositoryPath(path)||!available)}
        {view.file&&<><Box>{view.file.filePath} · {view.file.fileSize} B · {view.file.fileMode} · <code>{view.file.blobId}</code></Box>
          {view.file.text!==null&&<pre style={{whiteSpace:'pre-wrap',overflowWrap:'anywhere'}}>{view.file.text}</pre>}
          {button(t('Download file','ファイルをダウンロード'),download)}</>}
      </SpaceBetween>}
      {!draft&&(view.empty||view.tip&&view.commit?.commitId===view.tip)&&button(t('Add or edit file','ファイルを追加・編集'),edit,!available)}
      {draft&&<SpaceBetween size="s">
        {view.empty&&textInput(t('First branch name','最初のブランチ名'),draft.branch,v=>changeDraft('branch',v))}
        {textInput(t('Commit file path','コミットするファイルパス'),draft.path,v=>changeDraft('path',v))}
        <FormField label={t('Text contents','テキストの内容')}><Textarea value={draft.text} disabled={locked||!!draft.upload||draft.binary}
          onChange={e=>changeDraft('text',e.detail.value)}/></FormField>
        <FormField controlId="codecommit-upload" label={t('Upload file','ファイルをアップロード')} description={t('Up to 64 KiB. Paths allow 256 bytes and eight components.','64 KiB 以下。パスは 256 バイト、8 階層までです。')}>
          <input id="codecommit-upload" type="file" disabled={locked} onChange={e=>changeDraft('upload',e.target.files[0]||null)}/></FormField>
        <FormField label={t('File mode','ファイルモード')}><Select selectedOption={option(draft.mode)} disabled={locked}
          options={['NORMAL','EXECUTABLE'].map(option)} onChange={e=>changeDraft('mode',e.detail.selectedOption.value)}/></FormField>
        {textInput(t('New commit message','新しいコミットメッセージ'),draft.message,v=>changeDraft('message',v))}
        <SpaceBetween size="xs" direction="horizontal">
          {button(t('Commit file','ファイルをコミット'),()=>write(false),!available||!draft.branch||!repositoryPath(draft.path)||
            (draft.binary&&!draft.upload)||draft.upload?.size>maxFileSize,{variant:'primary'})}
          {button(t('Cancel editing','編集をキャンセル'),()=>setDraft(null),locked)}
        </SpaceBetween>
      </SpaceBetween>}
      {view.commit&&!draft&&<SpaceBetween size="xs">
        {textInput(t('New branch name','新しいブランチ名'),fork,setFork)}
        {button(t('Create branch at this commit','このコミットからブランチを作成'),()=>write(true),!available||!fork)}
      </SpaceBetween>}
    </SpaceBetween>
  </Container></section>;
}
