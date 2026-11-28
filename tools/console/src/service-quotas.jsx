import React,{useEffect,useRef,useState} from 'react';
import {useLocale} from './i18n.jsx';
import {quotaIdentity,quotaCall,quotaValue,quotaRecord,quotaHistory,quotaEpoch,definitiveQuotaRefusal} from './service-quotas.mjs';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
export default function ServiceQuotas({request,region,account}){
 const {t}=useLocale(),[quota,setQuota]=useState(null),[published,setPublished]=useState(null),[error,setError]=useState(''),[loading,setLoading]=useState(false),[rows,setRows]=useState([]),[next,setNext]=useState(''),[tokens,setTokens]=useState(['']),[index,setIndex]=useState(0),[status,setStatus]=useState('ALL'),[level,setLevel]=useState('ACCOUNT'),[historyKind,setHistoryKind]=useState('quota'),[desired,setDesired]=useState('8'),[support,setSupport]=useState(false),[record,setRecord]=useState(null),[lookup,setLookup]=useState(''),[writing,setWriting]=useState(false),[uncertain,setUncertain]=useState(false),[acknowledged,setAcknowledged]=useState(false);
 const reads=useRef(quotaEpoch()),alive=useRef(true),write=useRef(null);
 const call=(op,input,signal)=>quotaCall(request,region,op,input,signal);
 useEffect(()=>()=>{alive.current=false;reads.current.cancel();write.current?.abort()},[]);
 function clear(){setQuota(null);setPublished(null);setRows([]);setNext('');setRecord(null);setTokens(['']);setIndex(0)}
 async function refreshQuota(){const task=reads.current.begin();clear();setError('');setLoading(true);try{
  const services=await call('ListServices',{},task.signal);
  const listedDefaults=await call('ListAWSDefaultServiceQuotas',{ServiceCode:quotaIdentity.ServiceCode},task.signal);
  const listedApplied=await call('ListServiceQuotas',{ServiceCode:quotaIdentity.ServiceCode,QuotaAppliedAtLevel:'ACCOUNT'},task.signal);
  if(!Array.isArray(services.Services)||!Array.isArray(listedDefaults.Quotas)||!Array.isArray(listedApplied.Quotas))throw Error('Invalid quota discovery response.');
  if(!services.Services.some(item=>item.ServiceCode===quotaIdentity.ServiceCode)||!listedDefaults.Quotas.length||!listedApplied.Quotas.length)throw Error(t('No supported account quota is available in this Region.','このリージョンには対応するアカウントクォータがありません。'));
  const defaults=await call('GetAWSDefaultServiceQuota',quotaIdentity,task.signal);const applied=await call('GetServiceQuota',quotaIdentity,task.signal);
  const a=quotaValue(defaults.Quota),b=quotaValue(applied.Quota);if(listedDefaults.Quotas.length!==1||listedApplied.Quotas.length!==1||quotaValue(listedDefaults.Quotas[0]).Value!==a.Value||quotaValue(listedApplied.Quotas[0]).Value!==b.Value)throw Error('Quota discovery and selected reads disagree.');if(b.QuotaAppliedAtLevel!=='ACCOUNT')throw Error('Invalid applied account scope.');
  if(task.valid()){setPublished(a);setQuota(b)}
 }catch(e){if(task.valid())setError(e.message)}finally{if(task.valid())setLoading(false)}}
 useEffect(()=>{refreshQuota()},[region,request]);
 async function history(token='',page=0){const task=reads.current.begin();setRows([]);setRecord(null);setNext('');setError('');setLoading(true);try{
  const result=quotaHistory(await call(historyKind==='quota'?'ListRequestedServiceQuotaChangeHistoryByQuota':'ListRequestedServiceQuotaChangeHistory',{...(historyKind==='quota'?quotaIdentity:{}),MaxResults:1,QuotaRequestedAtLevel:level,...(status!=='ALL'?{Status:status}:{}),...(token?{NextToken:token}:{})},task.signal));
  if(task.valid()){setRows(result.items);setNext(result.next);setIndex(page);setTokens(old=>page===0?['']:old.slice(0,page).concat(token));if(uncertain)setAcknowledged(true)}
 }catch(e){if(task.valid()){clear();setError(e.message)}}finally{if(task.valid())setLoading(false)}}
 async function inspect(id){const task=reads.current.begin();setRecord(null);setError('');setLoading(true);try{const value=quotaRecord((await call('GetRequestedServiceQuotaChange',{RequestId:id},task.signal)).RequestedQuota);if(value.Id!==id)throw Error('Request ID mismatch.');if(task.valid())setRecord(value)}catch(e){if(task.valid()){clear();setError(e.message)}}finally{if(task.valid())setLoading(false)}}
 async function submit(){if(writing||uncertain||!quota)return;const value=Number(desired);if(!Number.isFinite(value)||value<=quota.Value){setError(t('Desired value must exceed applied capacity.','希望値は適用済み上限を超える必要があります。'));return}
  reads.current.cancel();setWriting(true);setRecord(null);setError('');const controller=new AbortController();write.current=controller;
  try{const saved=quotaRecord((await call('RequestServiceQuotaIncrease',{...quotaIdentity,DesiredValue:value,SupportCaseAllowed:support},controller.signal)).RequestedQuota);if(alive.current){setRecord(saved);setLookup(saved.Id);setRows([]);setNext('')}}
  catch(e){if(alive.current){if(!definitiveQuotaRefusal(e)){setUncertain(true);setAcknowledged(false)}setError(e.message)}}finally{if(alive.current)setWriting(false);write.current=null}
 }
 const disabled=loading||writing;
 return <SpaceBetween size="l"><Header variant="h2">Service Quotas</Header>
 <Alert type="info">{t('Local account-level B2B Data Interchange Profiles capacity. Requests do not change capacity or open support cases.','ローカルのアカウント単位 B2B Data Interchange Profiles 上限です。リクエストで上限は変更されず、サポートケースも作成されません。')}</Alert>
 <KeyValuePairs columns={2} items={[{label:t('Region','リージョン'),value:region},{label:t('Account','アカウント'),value:account},{label:'ServiceCode',value:quotaIdentity.ServiceCode},{label:'QuotaCode',value:quotaIdentity.QuotaCode},{label:t('Published AWS default','公開 AWS デフォルト'),value:published?String(published.Value):t('Not loaded','未取得')},{label:t('Applied account capacity','適用済みアカウント上限'),value:quota?String(quota.Value):t('Not loaded','未取得')}]}/>
 <Button disabled={disabled} loading={loading} onClick={refreshQuota}>{t('Refresh quota','クォータを更新')}</Button>
 {error&&<Alert type="error">{error}</Alert>}
 {uncertain&&<Alert type="warning" header={t('Request outcome unknown','リクエスト結果が不明です')}>{t('The request may have been saved. It has no idempotency token. Do not retry it. Refresh history in this same account, Region and identity; matching amounts are not proof of the lost request ID. Leaving this view clears this local warning.','リクエストが保存された可能性があります。冪等性トークンはありません。再送しないでください。同じアカウント・リージョン・IDで履歴を更新してください。同じ希望値から失われたIDを特定することはできません。この画面を離れるとローカルの警告は消えます。')}
 <Button disabled={disabled} onClick={()=>history()}>{t('Refresh same-scope history','同じスコープの履歴を更新')}</Button>
 {acknowledged&&<Button disabled={disabled} onClick={()=>{setUncertain(false);setAcknowledged(false);setError('')}}>{t('Start a separate new request','別の新規リクエストを開始')}</Button>}</Alert>}
 <Container header={<Header variant="h3">{t('Request increase','上限引き上げをリクエスト')}</Header>}><SpaceBetween size="m">
 <FormField label={t('Desired value','希望値')}><Input type="number" value={desired} disabled={disabled||uncertain||!quota} onChange={({detail})=>setDesired(detail.value)}/></FormField>
 <Checkbox checked={support} disabled={disabled||uncertain||!quota} onChange={({detail})=>setSupport(detail.checked)}>{t('Allow support case (stored PENDING locally only)','サポートケースを許可（ローカルで PENDING として保存するのみ）')}</Checkbox>
 <Button variant="primary" disabled={disabled||uncertain||!quota} loading={writing} onClick={submit}>{t('Submit increase request','引き上げリクエストを送信')}</Button></SpaceBetween></Container>
 <Container header={<Header variant="h3">{t('Request history','リクエスト履歴')}</Header>}><SpaceBetween size="m">
 <FormField label={t('History scope','履歴の範囲')}><Select disabled={disabled} selectedOption={{value:historyKind,label:historyKind==='quota'?'Quota':'Service'}} options={[{value:'quota',label:'Quota'},{value:'service',label:'Service'}]} onChange={({detail})=>{reads.current.cancel();setHistoryKind(detail.selectedOption.value);setRows([]);setNext('');setIndex(0);setTokens(['']);setRecord(null)}}/></FormField>
 <FormField label={t('Status filter','ステータスフィルター')}><Select disabled={disabled} selectedOption={{value:status,label:status}} options={['ALL','PENDING','NOT_APPROVED'].map(value=>({value,label:value}))} onChange={({detail})=>{reads.current.cancel();setStatus(detail.selectedOption.value);setRows([]);setNext('');setRecord(null);setTokens(['']);setIndex(0)}}/></FormField>
 <FormField label="QuotaRequestedAtLevel"><Select disabled={disabled} selectedOption={{value:level,label:level}} options={['ACCOUNT','RESOURCE'].map(value=>({value,label:value}))} onChange={({detail})=>{reads.current.cancel();setLevel(detail.selectedOption.value);setRows([]);setNext('');setRecord(null);setTokens(['']);setIndex(0)}}/></FormField>
 <SpaceBetween direction="horizontal" size="xs"><Button disabled={disabled} onClick={()=>history()}>{t('Refresh history','履歴を更新')}</Button><Button disabled={disabled||index===0} onClick={()=>history(tokens[index-1],index-1)}>{t('Previous history page','前の履歴ページ')}</Button><Button disabled={disabled||!next} onClick={()=>history(next,index+1)}>{t('Next history page','次の履歴ページ')}</Button></SpaceBetween>
 <Table loading={loading} items={rows} trackBy="Id" empty={t('No request records loaded.','リクエストレコードは読み込まれていません。')} columnDefinitions={[{id:'id',header:'Request ID',cell:item=><Link onFollow={e=>{e.preventDefault();setLookup(item.Id);inspect(item.Id)}} href="#">{item.Id}</Link>},{id:'value',header:t('Desired value','希望値'),cell:item=>String(item.DesiredValue)},{id:'status',header:t('Status','ステータス'),cell:item=>item.Status},{id:'date',header:t('Created','作成日時'),cell:item=>new Date(item.Created*1000).toISOString()}]}/>
 <FormField label="Request ID"><Input value={lookup} disabled={disabled} onChange={({detail})=>setLookup(detail.value)}/></FormField><Button disabled={disabled||!lookup} onClick={()=>inspect(lookup)}>{t('Get request','リクエストを取得')}</Button>
 {record&&<KeyValuePairs columns={1} items={['Id','DesiredValue','Status','Created','QuotaRequestedAtLevel'].map(label=>({label,value:String(record[label])}))}/>}
 <Link external href="/_simulator/docs/service-quotas.html">{t('Service Quotas API reference (us-east-1)','Service Quotas API リファレンス（us-east-1）')}</Link>
 </SpaceBetween></Container></SpaceBetween>;
}
