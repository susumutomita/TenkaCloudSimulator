import React,{useEffect,useRef,useState} from 'react';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Textarea from '@cloudscape-design/components/textarea';
import {useLocale} from './i18n.jsx';
import {canonicalDomain,checkedReceipt,composeMessage,deliveryPath,domainProof,identitiesPath,mailbox,messageId,receiptList} from './ses-mail.mjs';

const initial = {from:'',to:mailbox,mode:'simple',subject:'',text:'',html:'',template:'',data:'{}'};

export default function SESMail({region,request,onSaving}) {
  const {t} = useLocale();
  const [name,setName] = useState(''), [proof,setProof] = useState(null);
  const [form,setForm] = useState(initial), [rows,setRows] = useState(null);
  const [ack,setAck] = useState(''), [pending,setPending] = useState(false), [reading,setReading] = useState(false);
  const [unknown,setUnknown] = useState(null), [error,setError] = useState('');
  const [selected,setSelected] = useState(null), [confirm,setConfirm] = useState('');
  const read = useRef(null), write = useRef(null), mounted = useRef(false), guarded = useRef(null);
  const sink = '/_simulator/v1/ses/sink/'+encodeURIComponent(region)+'/messages';
  const nativeHeaders = {'X-Simulator-Region':region,'X-Simulator-Service':'ses'};
  useEffect(()=>{
    mounted.current=true;
    return ()=>{mounted.current=false; read.current?.abort(); write.current?.abort();};
  },[]);
  function lock(target) {guarded.current=target;setUnknown(target);}
  function clearSensitive() {setRows(null);setSelected(null);setProof(null);setConfirm('');setAck('');}
  function change(key,value) {setForm(current=>({...current,[key]:value}));}
  function stopRead() {read.current?.abort();read.current=null;setReading(false);}
  async function observe(load,apply) {
    if (write.current) return;
    stopRead();
    const c=new AbortController(); read.current=c;setReading(true);setError('');
    try {
      const data=await load(c.signal);
      if (mounted.current && read.current===c) apply(data);
    } catch(e) {
      if (mounted.current && read.current===c) {clearSensitive();setError(e.message);}
    } finally {
      if (mounted.current && read.current===c) {read.current=null;setReading(false);}
    }
  }
  function refreshDomain(value,recover=false) {
    let domain;
    try {domain=canonicalDomain(value);} catch(e) {setError(e.message);return;}
    setProof(null);
    observe(async signal=>{
      try {
        const res=await request(identitiesPath+'/'+encodeURIComponent(domain),{headers:nativeHeaders,signal});
        return domainProof(domain,await res.json());
      } catch(e) {
        if (recover && /^404:/.test(e.message)) return null;
        throw e;
      }
    },data=>{
      setProof(data);
      if(recover && guarded.current?.domain===domain) lock(null);
      if(!data)setError(t('The domain identity is absent.','対象のドメイン ID は存在しません。'));
    });
  }
  function refreshReceipts(recover=false) {
    setRows(null);setSelected(null);setConfirm('');
    observe(async signal=>receiptList(await (await request(sink,{signal})).json()),data=>{
      setRows(data);
      if(recover && guarded.current?.scope==='sink') lock(null);
    });
  }
  async function mutate(target,send,success) {
    if (write.current || guarded.current) return;
    stopRead();setError('');setConfirm('');
    const c=new AbortController();write.current=c;setPending(true);onSaving?.(true);
    try {
      const value=await send(c.signal);
      if(mounted.current && write.current===c) success(value);
    } catch(e) {
      if(mounted.current && write.current===c) {
        clearSensitive();setError(e.message);
        if(!/^(400|403|404|409|429):/.test(e.message)) lock(target);
      }
    } finally {
      if(write.current===c){write.current=null;onSaving?.(false);if(mounted.current)setPending(false);}
    }
  }
  function createDomain() {
    let domain;
    try {domain=canonicalDomain(name);} catch(e) {setError(e.message);return;}
    setProof(null);
    mutate({scope:'identity',domain},async signal=>{
      const res=await request(identitiesPath,{method:'POST',signal,headers:{...nativeHeaders,'Content-Type':'application/json'},body:JSON.stringify({EmailIdentity:domain})});
      const data=await res.json();
      return domainProof(domain,{...data,VerificationStatus:data?.DkimAttributes?.Status});
    },setProof);
  }
  function submit() {
    let body;
    try {body=composeMessage(form);} catch(e) {setError(e.message);return;}
    setAck('');setSelected(null);setRows(null);
    mutate({scope:'sink'},async signal=>{
      const res=await request(deliveryPath,{method:'POST',signal,headers:{...nativeHeaders,'Content-Type':'application/json'},body:JSON.stringify(body)});
      return messageId(await res.json());
    },setAck);
  }
  function inspect(id) {
    setSelected(null);setConfirm('');
    observe(async signal=>{
      const row=checkedReceipt(await (await request(sink+'/'+id,{signal})).json());
      if(row.messageId!==id)throw Error('Unexpected message identifier.');
      return row;
    },setSelected);
  }
  function download(row) {
    observe(async signal=>{
      const res=await request(sink+'/'+row.messageId+'/content',{signal});
      if(res.headers.get('Content-Type')!=='application/octet-stream' || !res.headers.get('Content-Disposition')?.startsWith('attachment;'))throw Error('Invalid MIME attachment.');
      const blob=await res.blob();
      if(blob.size!==row.contentBytes)throw Error('Incomplete MIME attachment.');
      return blob;
    },blob=>{
      const url=URL.createObjectURL(blob), a=document.createElement('a');
      a.href=url;a.download=row.messageId+'.eml';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
    });
  }
  function remove(id) {
    mutate({scope:'sink'},async signal=>{
      const res=await request(sink+'/'+id,{method:'DELETE',signal});
      if(res.status!==204)throw Error('Missing removal acknowledgement.');
    },()=>{setSelected(null);setRows(current=>current?.filter(row=>row.messageId!==id)??null);if(ack===id)setAck('');});
  }
  const disabled=pending||!!unknown;
  const field=(label,key,multiline=false)=> <FormField label={label}>{multiline?
    <Textarea value={form[key]} disabled={disabled} onChange={e=>change(key,e.detail.value)}/>:
    <Input value={form[key]} disabled={disabled} onChange={e=>change(key,e.detail.value)}/>}</FormField>;
  const choices=[{value:'simple',label:t('Simple message','直接入力')},{value:'template',label:t('Saved template','保存済みテンプレート')}];
  return <ExpandableSection variant="container" headerText={t('Local email','ローカルメール')}>
    <SpaceBetween size="m">
      {error&&<div role="alert">{error}</div>}
      {unknown&&<div role="alert"><SpaceBetween size="s">
        <strong>{t('The write acknowledgement is unknown.','書き込み結果を確認できませんでした。')}</strong>
        <span>{t('Read the current state before submitting another change. A matching message cannot identify the earlier submission.','次の変更の前に現在の状態を確認してください。同じ内容のメールでも、先ほどの送信とは特定できません。')}</span>
        <Button disabled={pending||reading} onClick={()=>unknown.scope==='identity'?refreshDomain(unknown.domain,true):refreshReceipts(true)}>{t('Read current state','現在の状態を確認')}</Button>
      </SpaceBetween></div>}
      <Container header={<Header variant="h2">{t('Sender domain','送信元ドメイン')}</Header>}>
        <SpaceBetween size="s">
          <FormField label={t('Local .test domain','ローカル .test ドメイン')}><Input disabled={disabled} value={name} onChange={e=>{stopRead();setName(e.detail.value);setProof(null);}}/></FormField>
          <SpaceBetween direction="horizontal" size="s">
            <Button disabled={disabled||!name} onClick={createDomain}>{t('Create domain identity','ドメイン ID を作成')}</Button>
            <Button disabled={disabled||!name} onClick={()=>refreshDomain(name)}>{t('Check DNS proof','DNS 確認状態を取得')}</Button>
          </SpaceBetween>
          {proof&&<div><p><strong>{proof.name}</strong> · {proof.status}</p>
            <p>{t('Publish these three CNAME records in the existing local public hosted zone, then check DNS proof.','既存のローカル公開ホストゾーンに次の CNAME レコード 3 件を追加し、DNS 確認状態を取得してください。')}</p>
            <dl style={{overflowWrap:'anywhere'}}>{proof.records.map(rr=><React.Fragment key={rr.name}><dt>{rr.name}</dt><dd>{rr.value}</dd></React.Fragment>)}</dl>
          </div>}
        </SpaceBetween>
      </Container>
      <Container header={<Header variant="h2">{t('Compose email','メールを作成')}</Header>}>
        <SpaceBetween size="s">
          {field(t('From address','送信元アドレス'),'from')}{field(t('To address','宛先アドレス'),'to')}
          <FormField label={t('Message content','メールの形式')}><Select disabled={disabled} selectedOption={choices.find(item=>item.value===form.mode)} options={choices} onChange={e=>change('mode',e.detail.selectedOption.value)}/></FormField>
          {form.mode==='simple'?<>{field(t('Subject','件名'),'subject')}{field(t('Plain text','本文（テキスト）'),'text',true)}{field(t('HTML body','本文（HTML）'),'html',true)}</>:
            <>{field(t('Template name','テンプレート名'),'template')}{field(t('Template data (JSON)','テンプレートデータ（JSON）'),'data',true)}</>}
          <Button variant="primary" disabled={disabled} onClick={submit}>{t('Send to local sink','ローカル受信先に送信')}</Button>
          {ack&&<div role="status" style={{overflowWrap:'anywhere'}}>{t('Submission accepted','送信を受理しました')} · <code>{ack}</code></div>}
        </SpaceBetween>
      </Container>
      <Container header={<Header variant="h2" actions={<Button disabled={pending||!!unknown} onClick={()=>refreshReceipts()}>{t('Refresh receipts','受信記録を更新')}</Button>}>{t('Delivery receipts','ローカルの配信記録')}</Header>}>
        <SpaceBetween size="s">
          {rows===null?<Box>{t('Read the local sink with your team operator account.','チームのオペレーター権限で受信記録を読み込んでください。')}</Box>:
            rows.length===0?<Box>{t('No saved submissions.','保存された送信記録はありません。')}</Box>:
            <ul style={{overflowWrap:'anywhere',paddingInlineStart:'1.25em'}}>{rows.map(row=><li key={row.messageId}><button type="button" disabled={pending||!!unknown} onClick={()=>inspect(row.messageId)}>{row.messageId}</button> {row.to} · {row.outcome}</li>)}</ul>}
          {selected&&<div style={{overflowWrap:'anywhere'}}><p><strong>{selected.messageId}</strong></p><p>{selected.from} → {selected.to}</p><p>{selected.acceptedAt} · {selected.outcome} · {selected.reason||selected.contentBytes+' bytes'}</p>
            <SpaceBetween direction="horizontal" size="xs">
              {selected.outcome==='delivered'&&<Button disabled={disabled} onClick={()=>download(selected)}>{t('Download MIME','MIME をダウンロード')}</Button>}
              {confirm===selected.messageId?<><Button disabled={disabled} onClick={()=>remove(selected.messageId)}>{t('Confirm removal','削除を確定')}</Button><Button onClick={()=>setConfirm('')}>{t('Cancel','キャンセル')}</Button></>:
                <Button disabled={disabled} onClick={()=>setConfirm(selected.messageId)}>{t('Remove receipt','受信記録を削除')}</Button>}
            </SpaceBetween>
          </div>}
        </SpaceBetween>
      </Container>
      {reading&&<Button onClick={()=>{stopRead();clearSensitive();}}>{t('Cancel read','読み込みをキャンセル')}</Button>}
    </SpaceBetween>
  </ExpandableSection>;
}
