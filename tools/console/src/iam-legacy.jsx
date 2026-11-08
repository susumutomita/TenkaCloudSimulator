import {useLocale} from './i18n.jsx';
import {localizeAwsNotice} from './aws-child-notices.js';
import React,{useEffect,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import TextFilter from '@cloudscape-design/components/text-filter';

const root='/_simulator/v1/iam/principals';
const arn=name=>'arn:aws:iam::000000000000:role/'+name;
const denyAll=JSON.stringify({Version:'2012-10-17',Statement:[{Effect:'Deny',Action:'*',Resource:'*'}]},null,2);
const identifier=text=><span className="resource-identifier">{text}</span>;

export default function LegacyIAMView({request,onSaving,onLambda,excludeNames=[]}){
 const {t}=useLocale();
 const [roles,setRoles]=useState([]),[loading,setLoading]=useState(true),[error,setError]=useState(''),[message,setMessage]=useState(''),[filter,setFilter]=useState(''),[creating,setCreating]=useState(false),[name,setName]=useState(''),[policy,setPolicy]=useState(denyAll),[detail,setDetail]=useState(null),[editing,setEditing]=useState(false),[saving,setSaving]=useState(false);
 const revision=useRef(0),alive=useRef(true),controller=useRef(new AbortController());
 const api=async(path='',options={})=>{const response=await request(root+path,{...options,signal:controller.current.signal,headers:{'Content-Type':'application/json',...options.headers}});return response.json()};
 async function load(){setLoading(true);setError('');try{const data=await api();if(alive.current)setRoles(data.principals.filter(name=>name!=='simulator-admin'&&!excludeNames.some(native=>native.toLowerCase()===name.toLowerCase())))}catch(e){if(alive.current)setError(e.message)}finally{if(alive.current)setLoading(false)}}
 useEffect(()=>{load();return()=>{alive.current=false;controller.current.abort();onSaving(false)}},[]);
 useEffect(()=>{onSaving(saving)},[saving]);
 async function inspect(role){const current=++revision.current;setError('');try{const document=await api('/'+encodeURIComponent(role)+'/policy');if(alive.current&&current===revision.current){setDetail(role);setPolicy(JSON.stringify(document,null,2));setEditing(false)}}catch(e){if(alive.current)setError(e.message)}}
 async function save(){setSaving(true);setError('');setMessage('');try{
  const role=creating?name:detail;
  if(!/^[a-zA-Z0-9_-]{1,64}$/.test(role)||role==='simulator-admin')throw Error('Use a non-reserved role name with 1–64 letters, digits, hyphens or underscores.');
  let document;try{document=JSON.parse(policy)}catch{throw Error('Enter valid permissions policy JSON.');}
  await api('/'+encodeURIComponent(role)+'/policy',{method:'PUT',headers:creating?{'If-None-Match':'*'}:{},body:JSON.stringify(document)});
  if(alive.current){setCreating(false);setEditing(false);setDetail(role);setMessage('Role permissions saved.');await load()}
 }catch(e){if(alive.current)setError(e.message)}finally{if(alive.current)setSaving(false)}}
 const start=()=>{revision.current++;setCreating(true);setName('');setPolicy(denyAll);setDetail(null);setError('');setMessage('')};
 const fields=<SpaceBetween size="l">{creating&&<FormField label={t("Role name", "ロール名")} constraintText={t("1–64 letters, digits, hyphens or underscores. Existing identities cannot be overwritten.", "英数字、ハイフン、アンダースコアで 1～64 文字です。既存の ID は上書きできません。")}><Input autoFocus value={name} onChange={({detail})=>setName(detail.value)}/></FormField>}<FormField label={t("Permissions policy JSON", "許可ポリシー JSON")} description={t("The default explicit Deny grants no service access. Replace it with the least permissions your function needs; an explicit Deny takes precedence over Allow.", "既定の明示的な Deny はサービスへのアクセスを許可しません。関数に必要な最小限の許可に置き換えてください。明示的な Deny は Allow より優先されます。")}><Textarea rows={12} value={policy} onChange={({detail})=>setPolicy(detail.value)}/></FormField></SpaceBetween>;
 const visible=roles.filter(role=>role.toLowerCase().includes(filter.toLowerCase()));
 return <SpaceBetween size="l">
  <Alert type="info" header={t("Legacy local identities", "従来のローカル ID")}>{t("These global execution roles use the simulator's existing identity policies. Users and roles share one local name space. These legacy identities do not evaluate trust policies and are separate from native IAM roles. Native role names cannot be edited here. This creates no AWS role or CloudWatch permissions.", "このグローバル実行ロールは既存のシミュレーター ID ポリシーを使用します。ユーザーとロールはローカルの名前空間を共有します。従来の ID では信頼ポリシーを評価せず、ネイティブ IAM ロールとは別管理です。ネイティブロール名はここでは編集できません。AWS ロールや CloudWatch の許可は作成されません。")}</Alert>
  {error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success" dismissible dismissAriaLabel={t("Dismiss notification", "通知を閉じる")} onDismiss={()=>setMessage('')}>{localizeAwsNotice(message,t)}</Alert>}
  {creating||editing?<ContentLayout header={<Header variant="h1">{creating?t("Create role", "ロールを作成"):t("Edit permissions", "許可を編集")}</Header>}><form onSubmit={e=>{e.preventDefault();save()}}><Form actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={()=>{setCreating(false);setEditing(false);setError('')}}>{t("Cancel", "キャンセル")}</Button><Button variant="primary" formAction="submit" loading={saving}>{creating?t("Create role", "ロールを作成"):t("Save changes", "変更を保存")}</Button></SpaceBetween>}><Container header={<Header variant="h2">{creating?t("Role details", "ロールの詳細"):t("Permissions", "許可")}</Header>}>{fields}</Container></Form></form></ContentLayout>:detail?<ContentLayout header={<Header variant="h1" actions={<Button onClick={()=>{setDetail(null);setError('');setMessage('')}}>{t("Roles", "ロール")}</Button>}>{identifier(detail)}</Header>}><SpaceBetween size="l"><Container header={<Header variant="h2">{t("Summary", "概要")}</Header>}><KeyValuePairs columns={1} items={[{label:t("Role ARN", "ロール ARN"),value:identifier(arn(detail))},{label:t("Scope", "スコープ"),value:t("Global local identity", "グローバルなローカル ID")},{label:t("Trusted entities", "信頼されたエンティティ"),value:t("Not evaluated by this simulator", "このシミュレーターでは評価しません")}]}/></Container><Container header={<Header variant="h2" actions={<Button onClick={()=>setEditing(true)}>{t("Edit permissions", "許可を編集")}</Button>}>{t("Permissions", "許可")}</Header>}><FormField label={t("Permissions policy JSON", "許可ポリシー JSON")}><Textarea readOnly rows={12} value={policy}/></FormField></Container><Button variant="primary" onClick={onLambda}>{t("Create a Lambda function", "Lambda 関数を作成")}</Button></SpaceBetween></ContentLayout>:<Table variant="full-page" loading={loading} loadingText={t("Loading roles", "ロールを読み込み中")} items={visible} trackBy={role=>role} ariaLabels={{tableLabel:t("IAM roles", "IAM ロール")}} columnDefinitions={[{id:'name',header:t("Role name", "ロール名"),cell:role=><Link href="#" onFollow={e=>{e.preventDefault();inspect(role)}}>{identifier(role)}</Link>},{id:'arn',header:'ARN',cell:role=>identifier(arn(role))}]} header={<Header variant="awsui-h1-sticky" counter={'('+roles.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel={t("Refresh roles", "ロールを更新")} onClick={load}/><Button variant="primary" onClick={start}>{t("Create role", "ロールを作成")}</Button></SpaceBetween>}>{t("Roles", "ロール")}</Header>} filter={<TextFilter filteringText={filter} filteringPlaceholder={t("Find roles", "ロールを検索")} filteringAriaLabel={t("Find roles", "ロールを検索")} onChange={({detail})=>setFilter(detail.filteringText)}/>} empty={<Box textAlign="center" margin={{vertical:'l'}}><SpaceBetween size="m"><Box variant="h2">{filter?t("No matching roles", "一致するロールがありません"):t("No roles yet", "ロールはまだありません")}</Box><Button onClick={start}>{t("Create role", "ロールを作成")}</Button></SpaceBetween></Box>}/>}
 </SpaceBetween>
}
