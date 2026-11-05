import {useLocale} from './i18n.jsx';
import {localizeAwsNotice} from './aws-child-notices.js';
import React, {useEffect, useState} from 'react';
import S3Tags from './s3-tags.jsx';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Box from '@cloudscape-design/components/box';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import RadioGroup from '@cloudscape-design/components/radio-group';
import SpaceBetween from '@cloudscape-design/components/space-between';

const text=(xml,key)=>xml.getElementsByTagName(key)[0]?.textContent||'';
const parse=body=>new DOMParser().parseFromString(body,'application/xml');
export default function S3Settings({bucket,tab,request,onSaving}){
 const {t}=useLocale();
 const labels={BlockPublicAcls:t("Block public access to buckets and objects granted through new access control lists (ACLs)", "新しいアクセスコントロールリスト（ACL）によるバケットとオブジェクトへのパブリックアクセスをブロック"),IgnorePublicAcls:t("Block public access to buckets and objects granted through any access control lists (ACLs)", "すべてのアクセスコントロールリスト（ACL）によるバケットとオブジェクトへのパブリックアクセスをブロック"),BlockPublicPolicy:t("Block public access to buckets and objects granted through new public bucket or access point policies", "新しいパブリックバケットポリシーまたはアクセスポイントポリシーによるパブリックアクセスをブロック"),RestrictPublicBuckets:t("Block public and cross-account access to buckets and objects through any public bucket or access point policies", "すべてのパブリックバケットポリシーまたはアクセスポイントポリシーによるパブリックアクセスとクロスアカウントアクセスをブロック")};
 const kind=tab==='properties'?'versioning':'publicAccessBlock';
 const [state,setState]=useState(null),[draft,setDraft]=useState(null),[editing,setEditing]=useState(false),[loading,setLoading]=useState(true),[saving,setSaving]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState(''),[confirming,setConfirming]=useState(false),[confirmation,setConfirmation]=useState(''),[tagSaving,setTagSaving]=useState(false);
 useEffect(()=>{const controller=new AbortController();setLoading(true);setError('');setEditing(false);setMessage('');
  request('/'+bucket+'?'+kind,{signal:controller.signal}).then(r=>r.text()).then(body=>{if(controller.signal.aborted)return;const xml=parse(body);setState(kind==='versioning'?(text(xml,'Status')||'Disabled'):Object.fromEntries(Object.keys(labels).map(k=>[k,text(xml,k)==='true'])))}).catch(e=>{if(!controller.signal.aborted){if(kind==='publicAccessBlock'&&e.message.startsWith('NoSuchPublicAccessBlockConfiguration:')){setState(Object.fromEntries(Object.keys(labels).map(k=>[k,false])))}else{setState(null);setError(e.message)}}}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});return()=>controller.abort();
 },[bucket,kind]);
 const edit=()=>{setDraft(kind==='versioning'?(state==='Disabled'?'Enabled':state):{...state});setEditing(true);setError('');setMessage('')};
 async function save(){if(tagSaving)return;if(kind==='publicAccessBlock'&&!confirming){setConfirmation('');setConfirming(true);return}if(kind==='publicAccessBlock'&&confirmation!=='confirm'){setError('Enter confirm to save these settings.');return}setSaving(true);onSaving(true);setError('');try{
  const body=kind==='versioning'?`<VersioningConfiguration><Status>${draft}</Status></VersioningConfiguration>`:`<PublicAccessBlockConfiguration>${Object.entries(draft).map(([k,v])=>`<${k}>${v}</${k}>`).join('')}</PublicAccessBlockConfiguration>`;
  await request('/'+bucket+'?'+kind,{method:'PUT',body,headers:{'Content-Type':'application/xml'}});setState(draft);setEditing(false);setConfirming(false);setMessage('Changes saved.');
 }catch(e){setError(e.message)}finally{setSaving(false);onSaving(false)}}
 const actions=editing?<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving||tagSaving} onClick={()=>{setEditing(false);setError('')}}>{t("Cancel", "キャンセル")}</Button><Button variant="primary" disabled={tagSaving} loading={saving} onClick={save}>{t("Save changes", "変更を保存")}</Button></SpaceBetween>:<Button disabled={loading||state===null||tagSaving} onClick={edit}>{t("Edit", "編集")}</Button>;
 return <SpaceBetween size="l"><Modal visible={confirming} onDismiss={()=>!saving&&setConfirming(false)} header={t("Edit Block public access (bucket settings)", "パブリックアクセスのブロックを編集（バケット設定）")} closeAriaLabel={t("Close confirmation", "確認を閉じる")} footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={()=>setConfirming(false)}>{t("Cancel", "キャンセル")}</Button><Button variant="primary" loading={saving} onClick={save}>{t("Confirm", "確認")}</Button></SpaceBetween></Box>}><Form errorText={error}><FormField label={t("Type confirm to save changes", "変更を保存するには confirm と入力")}><Input value={confirmation} onChange={({detail})=>setConfirmation(detail.value)}/></FormField></Form></Modal>{error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success">{localizeAwsNotice(message,t)}</Alert>}<Container header={<Header variant="h2" actions={actions}>{kind==='versioning'?t("Bucket Versioning", "バケットのバージョニング"):t("Block public access (bucket settings)", "パブリックアクセスをブロック（バケット設定）")}</Header>}>
 {loading?t("Loading bucket settings", "バケット設定を読み込み中"):state===null?t("Bucket settings could not be read.", "バケット設定を読み取れませんでした。"):kind==='versioning'?editing?<Form><FormField label={t("Bucket Versioning", "バケットのバージョニング")} description={t("Preserve, retrieve, and restore every version of an object. Suspending versioning retains existing versions.", "オブジェクトのすべてのバージョンを保持、取得、復元します。バージョニングを停止しても既存のバージョンは保持されます。")}><RadioGroup value={draft} onChange={({detail})=>setDraft(detail.value)} items={[{value:'Enabled',label:t("Enable", "有効化")},{value:'Suspended',label:t("Suspend", "停止"),disabled:state==='Disabled'}]}/></FormField></Form>:<KeyValuePairs columns={1} items={[{label:t("Bucket Versioning", "バケットのバージョニング"),value:state}]}/>:editing?<SpaceBetween size="m"><Checkbox checked={Object.values(draft).every(Boolean)} onChange={({detail})=>setDraft(Object.fromEntries(Object.keys(labels).map(k=>[k,detail.checked])))}>{t("Block all public access", "パブリックアクセスをすべてブロック")}</Checkbox>{Object.entries(labels).map(([k,label])=><Checkbox key={k} checked={draft[k]} onChange={({detail})=>setDraft({...draft,[k]:detail.checked})}>{label}</Checkbox>)}</SpaceBetween>:<KeyValuePairs columns={1} items={[{label:t("Block all public access", "パブリックアクセスをすべてブロック"),value:Object.values(state).every(Boolean)?'On':'Off'},...Object.entries(labels).map(([k,label])=>({label,value:state[k]?'On':'Off'}))]}/>}</Container>
 {kind==='versioning'&&<S3Tags bucket={bucket} request={request} disabled={saving} onSaving={value=>{setTagSaving(value);onSaving(value)}}/>}
 <Alert type="info">{kind==='versioning'?t("Version changes take effect immediately in this simulator. MFA Delete, lifecycle expiration, replication and Object Lock are unavailable.", "このシミュレーターではバージョン変更が即座に反映されます。MFA Delete、ライフサイクルの有効期限、レプリケーション、Object Lock は未対応です。"):t("These are bucket-level settings. Public ACLs, resource policies, access points and cross-account grants are unavailable in this simulator, so turning a setting off does not grant public access.", "これらはバケット単位の設定です。パブリック ACL、リソースポリシー、アクセスポイント、クロスアカウントの許可は未対応のため、設定をオフにしてもパブリックアクセスは許可されません。")}</Alert></SpaceBetween>;
}
