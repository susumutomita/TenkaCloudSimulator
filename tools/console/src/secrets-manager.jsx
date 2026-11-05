import React,{useEffect,useLayoutEffect,useRef,useState} from 'react';
import {disableMotion} from '@cloudscape-design/global-styles';
import {useLocale} from './i18n.jsx';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
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
import Tabs from '@cloudscape-design/components/tabs';
import SecretVersions from './secret_versions.jsx';
import Textarea from '@cloudscape-design/components/textarea';
import TextFilter from '@cloudscape-design/components/text-filter';
import Wizard from '@cloudscape-design/components/wizard';

const identifier=text=><span className="resource-identifier">{text}</span>;
const freshDraft=()=>({name:'',description:'',text:'',pairs:[{key:'',value:''}],format:'pairs',confirm:'',force:false});
const warning='Local practice database stores dummy values without encryption. AWS KMS, automatic rotation, replicas, binary secrets and recovery windows are not implemented. Do not store real credentials.';

// Canonical presentation state only. Native messages and secret data stay opaque.
const localeErrors = {
 "unique": [
  "Enter unique, nonempty keys.",
  "空でない一意のキーを入力してください。"
 ],
 "pairs": [
  "Enter at least one key/value pair.",
  "キーと値の組を1つ以上入力してください。"
 ],
 "size": [
  "Secret value must contain 1–65536 UTF-8 bytes.",
  "シークレット値は1〜65536 UTF-8バイトで入力してください。"
 ],
 "name": [
  "Enter a valid secret name of 1–512 characters.",
  "有効なシークレット名を1〜512文字で入力してください。"
 ],
 "description": [
  "Description is limited to 2048 characters.",
  "説明は2048文字以内で入力してください。"
 ],
 "response": [
  "Secret value response did not match the selected resource.",
  "シークレット値の応答が選択したリソースと一致しません。"
 ],
 "deletion": [
  "Confirm force deletion and enter the exact secret name.",
  "強制削除を確認し、正確なシークレット名を入力してください。"
 ]
};
const localeNotices = {
 "stored": [
  "Secret stored in the local practice database.",
  "シークレットをローカルの練習用データベースに保存しました。"
 ],
 "version": [
  "A new local secret version was saved.",
  "新しいローカルシークレットのバージョンを保存しました。"
 ],
 "deleted": [
  "Secret and all stored versions were deleted.",
  "シークレットと保存済みのすべてのバージョンを削除しました。"
 ],
 "rollback": [
  "Previous version is now current. This simulator helper calls the native version-stage API.",
  "前のバージョンを現在のバージョンにしました。この補助機能はバージョンステージの API を呼び出します。"
 ]
};
class SecretViewError extends Error {
 constructor(code){super(localeErrors[code][0]);this.code=code}
}
const captureError=error=>error instanceof SecretViewError?{kind:'client',code:error.code}:error.message;
const presentError=(error,t)=>typeof error==='object'&&error?.kind==='client'?t(...localeErrors[error.code]):error;
const presentNotice=(notice,t)=>notice&&Object.hasOwn(localeNotices,notice.kind)?t(...localeNotices[notice.kind]):'';

// Parent remounts on region/identity/navigation changes. All API state is native;
// the component keeps only transient form/selection/reveal state in memory.
export default function SecretsManagerView({region,request,onSaving}) {
 const {t}=useLocale();
 const motionRoot=useRef(null);
 useLayoutEffect(()=>{const target=motionRoot.current;if(!target)return;disableMotion(true,target);return()=>disableMotion(false,target)},[]);
 const [secrets,setSecrets]=useState([]),[detail,setDetail]=useState(null),[mode,setMode]=useState(''),[draft,setDraft]=useState(freshDraft),[step,setStep]=useState(0),[filter,setFilter]=useState(''),[error,setError]=useState(''),[notice,setNotice]=useState(''),[loading,setLoading]=useState(false),[busy,setBusy]=useState(false),[revealed,setRevealed]=useState(null),[revealing,setRevealing]=useState(false);
 const [section,setSection]=useState('secrets');
 const epoch=useRef(0),alive=useRef(true),controller=useRef(new AbortController()),mutationToken=useRef('');
 const change=(name,value)=>setDraft(old=>({...old,[name]:value}));
 const call=async(operation,input={})=>{
  const response=await request('/',{method:'POST',signal:controller.current.signal,headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'secretsmanager.'+operation,'X-Simulator-Service':'secretsmanager','X-Simulator-Region':region},body:JSON.stringify(input)});
  return response.json();
 };
 const invalidate=()=>{epoch.current++;setRevealed(null);setRevealing(false);return epoch.current};
 async function browse(arn=null){
  const ticket=invalidate();setLoading(true);setError('');
  try{
   if(arn){const metadata=await call('DescribeSecret',{SecretId:arn});if(alive.current&&ticket===epoch.current){setDetail(metadata);setMode('')}}
   else{const items=[];let next;do{const page=await call('ListSecrets',{MaxResults:100,SortBy:'name',SortOrder:'asc',...(next?{NextToken:next}:{})});items.push(...page.SecretList);next=page.NextToken}while(next&&alive.current&&ticket===epoch.current);if(alive.current&&ticket===epoch.current){setSecrets(items);setDetail(null);setMode('')}}
  }catch(e){if(alive.current&&ticket===epoch.current)setError({kind:'metadata',message:e.message,arn})}finally{if(alive.current&&ticket===epoch.current)setLoading(false)}
 }
 useEffect(()=>{browse();return()=>{alive.current=false;epoch.current++;controller.current.abort();onSaving?.(false)}},[]);
 function open(next){invalidate();setDraft(freshDraft());setStep(0);setMode(next);setError('');setNotice('');mutationToken.current=crypto.randomUUID()}
 function cancel(){invalidate();setDraft(freshDraft());mutationToken.current='';setMode('');setError('')}
 function material(){
  let value=draft.text;
  if(draft.format==='pairs'){
   const data={};let count=0;
   for(const pair of draft.pairs){if(!pair.key&&!pair.value)continue;if(!pair.key||Object.hasOwn(data,pair.key))throw new SecretViewError('unique');Object.defineProperty(data,pair.key,{value:pair.value,enumerable:true});count++}
   if(!count)throw new SecretViewError('pairs');value=JSON.stringify(data);
  }
  const size=new TextEncoder().encode(value).length;if(size<1||size>65536)throw new SecretViewError('size');return value;
 }
 function validateName(){if(!/^[A-Za-z0-9/_+=.@-]{1,512}$/.test(draft.name))throw new SecretViewError('name');if(Array.from(draft.description).length>2048)throw new SecretViewError('description')}
 async function mutate(operation,input,message){
  if(busy)return;const ticket=invalidate();setBusy(true);onSaving?.(true);setError('');
  try{const result=await call(operation,input);if(!alive.current||ticket!==epoch.current)return;setMode('');setDetail(null);setDraft(freshDraft());mutationToken.current='';setNotice(message);if(operation==='DeleteSecret')setSecrets(items=>items.filter(secret=>secret.ARN!==result.ARN));await browse(operation==='DeleteSecret'?null:result.ARN)}catch(e){if(alive.current&&ticket===epoch.current)setError(captureError(e))}finally{if(alive.current){setBusy(false);onSaving?.(false)}}
 }
 function store(){try{validateName();const value=material();mutate('CreateSecret',{Name:draft.name,Description:draft.description,ClientRequestToken:mutationToken.current,SecretString:value},{kind:'stored'})}catch(e){setError(captureError(e))}}
 async function retrieve(){
  const arn=detail.ARN,ticket=invalidate();setRevealing(true);setError('');
  try{const value=await call('GetSecretValue',{SecretId:arn});if(!alive.current||ticket!==epoch.current)return;if(value.ARN!==arn||typeof value.SecretString!=='string')throw new SecretViewError('response');setRevealed({text:value.SecretString,version:value.VersionId})}catch(e){if(alive.current&&ticket===epoch.current)setError(captureError(e))}finally{if(alive.current&&ticket===epoch.current)setRevealing(false)}
 }
 const hide=()=>{invalidate();setError('')};
 const metadataError=error?.kind==='metadata';
 const pairsEditor=<SpaceBetween size="m">{draft.pairs.map((pair,index)=><SpaceBetween key={index} direction="horizontal" size="s"><FormField label={t("Key ","キー ")+(index+1)}><Input value={pair.key} autoComplete="off" onChange={({detail})=>change('pairs',draft.pairs.map((p,i)=>i===index?{...p,key:detail.value}:p))}/></FormField><FormField label={t("Value ","値 ")+(index+1)}><Input type="password" value={pair.value} autoComplete="off" onChange={({detail})=>change('pairs',draft.pairs.map((p,i)=>i===index?{...p,value:detail.value}:p))}/></FormField><Button formAction="none" ariaLabel={t("Remove pair ","キーと値の組を削除 ")+(index+1)} onClick={()=>change('pairs',draft.pairs.filter((_,i)=>i!==index))}>{t("Remove","削除")}</Button></SpaceBetween>)}<Button formAction="none" onClick={()=>change('pairs',[...draft.pairs,{key:'',value:''}])}>{t("Add row","行を追加")}</Button></SpaceBetween>;
 const valueEditor=<Tabs activeTabId={draft.format} onChange={({detail})=>change('format',detail.activeTabId)} tabs={[{id:'pairs',label:t("Key/value pairs","キーと値の組"),content:pairsEditor},{id:'text',label:t("Plaintext","平文"),content:<FormField label={t("Plaintext secret value","平文のシークレット値")} description={t("This explicitly entered draft is visible while editing. It is hidden on review and cleared after save or navigation.","入力した値は編集中に表示されます。確認画面では非表示になり、保存後や画面の移動時に消去されます。")}><Textarea value={draft.text} rows={7} onChange={({detail})=>change('text',detail.value)}/></FormField>}]} />;
 const labeled=detail?Object.entries(detail.VersionIdsToStages||{}).map(([id,stages])=>({id,stages})):[];
 const current=labeled.find(v=>v.stages.includes('AWSCURRENT'))?.id;
 const previous=labeled.find(v=>v.stages.includes('AWSPREVIOUS'))?.id;
 return <div data-secrets-motion ref={motionRoot}><SpaceBetween size="l"><Alert type="warning">{t(warning,"ローカルの練習用データベースにはダミー値を暗号化せず保存します。AWS KMS、自動ローテーション、レプリカ、バイナリシークレット、復旧期間は未対応です。実際の認証情報を保存しないでください。")}</Alert>{section==='secrets'&&error&&<Alert type="error" dismissible dismissAriaLabel={t("Dismiss error","エラーを閉じる")} onDismiss={()=>setError('')} header={metadataError?t("Metadata refresh failed","メタデータの取得に失敗しました"):undefined} action={metadataError?<Button formAction="none" disabled={busy||loading} onClick={()=>browse(error.arn)}>{t("Retry metadata refresh","メタデータを再取得")}</Button>:undefined}>{metadataError?<SpaceBetween size="xs"><Box>{error.message}</Box><Box>{t("Retrying only reloads metadata.","再試行ではメタデータの読み取りだけを行います。")}</Box></SpaceBetween>:presentError(error,t)}</Alert>}{section==='secrets'&&notice&&<Alert type="success" dismissible dismissAriaLabel={t("Dismiss notification","通知を閉じる")} onDismiss={()=>setNotice('')}>{presentNotice(notice,t)}</Alert>}
 <Tabs activeTabId={section} onChange={({detail})=>{if(busy||mode)return;invalidate();setLoading(false);setError('');setSection(detail.activeTabId);if(detail.activeTabId==='secrets')browse()}} tabs={[{id:'secrets',label:t('Secrets','シークレット'),content:<>{mode==='create'?<Wizard activeStepIndex={step} isLoadingNextStep={busy} onCancel={()=>!busy&&cancel()} onSubmit={store} onNavigate={({detail})=>{if(busy)return;try{if(detail.requestedStepIndex>step){if(step===0)material();if(step===1)validateName()}setError('');setStep(detail.requestedStepIndex)}catch(e){setError(captureError(e))}}} i18nStrings={{stepNumberLabel:n=>t("Step ","ステップ ")+n,collapsedStepsLabel:(n,total)=>t("Step ","ステップ ")+n+t(" of "," / ")+total,skipToButtonLabel:step=>t("Skip to ","移動: ")+step.title,navigationAriaLabel:t("Secret creation steps","シークレットの作成手順"),cancelButton:t("Cancel","キャンセル"),previousButton:t("Previous","前へ"),nextButton:t("Next","次へ"),submitButton:t("Store","保存"),optional:t("optional","任意")}} steps={[
  {title:t("Choose secret type","シークレットの種類を選択"),content:<SpaceBetween size="l"><Container header={<Header variant="h2">{t("Secret type","シークレットの種類")}</Header>}><Box>{t("Other type of secret","その他のシークレット")}</Box></Container><Container header={<Header variant="h2">{t("Secret value","シークレット値")}</Header>}>{valueEditor}</Container><Container header={<Header variant="h2">{t("Encryption","暗号化")}</Header>}><Alert type="warning">{t("Local database storage is not encrypted. AWS KMS keys are unavailable in this increment.","ローカルデータベースの保存内容は暗号化されません。この機能では AWS KMS キーは使用できません。")}</Alert></Container></SpaceBetween>},
  {title:t("Configure secret","シークレットを設定"),content:<Container header={<Header variant="h2">{t("Secret name and description","シークレットの名前と説明")}</Header>}><SpaceBetween size="l"><FormField label={t("Secret name","シークレット名")} constraintText={t("1–512 ASCII letters, digits and /_+=.@- characters.","1～512 文字の ASCII 英数字と /_+=.@- を使用できます。")}><Input value={draft.name} onChange={({detail})=>change('name',detail.value)}/></FormField><FormField label={t("Description","説明")}><Textarea value={draft.description} onChange={({detail})=>change('description',detail.value)}/></FormField></SpaceBetween></Container>},
  {title:t("Configure rotation","ローテーションを設定"),content:<Container header={<Header variant="h2">{t("Automatic rotation","自動ローテーション")}</Header>}><SpaceBetween size="l"><Checkbox disabled checked={false}>{t("Automatic rotation","自動ローテーション")}</Checkbox><Box>{t("Rotation is not implemented. Updating a local version does not update any database or external credential.","ローテーションは未対応です。ローカルのバージョンを更新しても、データベースや外部サービスの認証情報は更新されません。")}</Box></SpaceBetween></Container>},
  {title:t("Review","確認"),content:<Container header={<Header variant="h2">{t("Review secret","シークレットを確認")}</Header>}><KeyValuePairs columns={1} items={[{label:t("Secret type","シークレットの種類"),value:t("Other type of secret","その他のシークレット")},{label:t("Secret name","シークレット名"),value:identifier(draft.name)},{label:t("Description","説明"),value:draft.description||'—'},{label:t("Secret value","シークレット値"),value:t("Entered value is hidden","入力した値は非表示です")},{label:t("Encryption","暗号化"),value:t("Local database; not encrypted","ローカルデータベース（暗号化なし）")},{label:t("Automatic rotation","自動ローテーション"),value:t("Not implemented","未対応")}]}/></Container>}
 ]}/>:mode==='edit'||mode==='delete'?<ContentLayout header={<Header variant="h1">{mode==='edit'?t("Edit secret value","シークレット値を編集"):t("Force delete secret","シークレットを強制削除")}</Header>}><form onSubmit={event=>{event.preventDefault();try{if(mode==='edit')mutate('PutSecretValue',{SecretId:detail.ARN,ClientRequestToken:mutationToken.current,SecretString:material()},{kind:'version'});else{if(!draft.force||draft.confirm!==detail.Name)throw new SecretViewError('deletion');mutate('DeleteSecret',{SecretId:detail.ARN,ForceDeleteWithoutRecovery:true},{kind:'deleted'})}}catch(e){setError(captureError(e))}}}><Form actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={cancel}>{t("Cancel","キャンセル")}</Button><Button formAction="submit" variant="primary" loading={busy}>{mode==='edit'?t("Save","保存"):t("Delete permanently","完全に削除")}</Button></SpaceBetween>}><Container><SpaceBetween size="l">{mode==='edit'?valueEditor:<><Alert type="warning">{t("Only force deletion without recovery is implemented. This permanently removes every local version. AWS recovery windows and restore are unavailable.","復元できない強制削除のみ対応しています。すべてのローカルバージョンを完全に削除します。AWS の復旧期間や復元には対応していません。")}</Alert><Checkbox checked={draft.force} onChange={({detail})=>change('force',detail.checked)}>{t("Force delete without recovery","復元できない強制削除を実行する")}</Checkbox><FormField label={t("Enter secret name to confirm","確認のためシークレット名を入力")}><Input value={draft.confirm} onChange={({detail})=>change('confirm',detail.value)}/></FormField></>}</SpaceBetween></Container></Form></form></ContentLayout>:detail?<ContentLayout header={<Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>browse()}>{t("Secrets","シークレット")}</Button><Button formAction="none" disabled={busy} onClick={()=>open('delete')}>{t("Delete secret","シークレットを削除")}</Button></SpaceBetween>}>{identifier(detail.Name)}</Header>}><SpaceBetween size="l"><Container header={<Header variant="h2" actions={<Button formAction="none" iconName="refresh" ariaLabel={t("Refresh secret metadata","シークレットのメタデータを更新")} disabled={busy} onClick={()=>browse(detail.ARN)}/>}>{t("Secret details","シークレットの詳細")}</Header>}><KeyValuePairs columns={1} items={[{label:t("Secret name","シークレット名"),value:identifier(detail.Name)},{label:t("Secret ARN","シークレット ARN"),value:identifier(detail.ARN)},{label:t("Description","説明"),value:detail.Description||'—'},{label:t("Encryption","暗号化"),value:t("Local database; not encrypted","ローカルデータベース（暗号化なし）")}]}/></Container><Container header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">{(revealed!==null||revealing)&&<Button formAction="none" onClick={hide}>{t("Hide secret value","シークレット値を非表示")}</Button>}<Button formAction="none" loading={revealing} disabled={busy} onClick={retrieve}>{t("Retrieve secret value","シークレット値を取得")}</Button>{revealed!==null&&<Button formAction="none" disabled={busy} onClick={()=>open('edit')}>{t("Edit","編集")}</Button>}</SpaceBetween>}>{t("Secret value","シークレット値")}</Header>}>{revealed===null?<Box>{t("Choose Retrieve secret value to explicitly request AWSCURRENT. Metadata views do not retrieve values.","「シークレット値を取得」を選択すると AWSCURRENT を取得します。メタデータの表示だけでは値を取得しません。")}</Box>:<SpaceBetween size="m"><Box>{t("Version ID: ","バージョン ID: ")}{identifier(revealed.version)}</Box><FormField label={t("Retrieved secret value","取得したシークレット値")}><Textarea readOnly value={revealed.text} rows={7}/></FormField></SpaceBetween>}</Container><Table items={labeled} trackBy="id" ariaLabels={{tableLabel:t("Labeled secret versions","ラベル付きのシークレットバージョン")}} columnDefinitions={[{id:'id',header:t("Version ID","バージョン ID"),cell:v=>identifier(v.id)},{id:'stages',header:t("Staging labels","ステージングラベル"),cell:v=>v.stages.join(', ')}]} header={<Header variant="h2" actions={<Button formAction="none" disabled={busy||!current||!previous} onClick={()=>mutate('UpdateSecretVersionStage',{SecretId:detail.ARN,VersionStage:'AWSCURRENT',MoveToVersionId:previous,RemoveFromVersionId:current},{kind:'rollback'})}>{t("Roll back to previous (simulator)","前のバージョンに戻す（シミュレーター）")}</Button>}>{t("Versions with labels","ラベル付きバージョン")}</Header>} empty={<Box>{t("No labeled versions.","ラベル付きバージョンはありません。")}</Box>}/><Alert type="info">{t("This table shows labeled versions only. Older retained versions can be retrieved by version ID through the API. The rollback button is a simulator helper; the public AWS guide uses CLI label moves.","この表にはラベル付きバージョンのみ表示します。保持されている古いバージョンは API でバージョン ID を指定して取得できます。元に戻すボタンはシミュレーター独自の補助機能です。AWS の公開ガイドでは CLI でラベルを移動します。")}</Alert></SpaceBetween></ContentLayout>:<Table variant="full-page" loading={loading} loadingText={t("Loading secrets","シークレットを読み込み中")} items={secrets.filter(s=>(s.Name+' '+(s.Description||'')).toLowerCase().includes(filter.toLowerCase()))} trackBy="ARN" ariaLabels={{tableLabel:t("Secrets","シークレット")}} columnDefinitions={[{id:'name',header:t("Secret name","シークレット名"),cell:s=><Link href="#" onFollow={event=>{event.preventDefault();browse(s.ARN)}}>{s.Name}</Link>},{id:'description',header:t("Description","説明"),cell:s=>s.Description||'—'},{id:'changed',header:t("Last changed","最終変更"),cell:s=>new Date(s.LastChangedDate*1000).toISOString()}]} header={<Header variant="awsui-h1-sticky" counter={'('+secrets.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" iconName="refresh" ariaLabel={t("Refresh secrets","シークレット一覧を更新")} disabled={busy} onClick={()=>browse()}/><Button formAction="none" variant="primary" disabled={busy} onClick={()=>open('create')}>{t("Store a new secret","新しいシークレットを保存")}</Button></SpaceBetween>}>{t("Secrets","シークレット")}</Header>} filter={<TextFilter filteringText={filter} filteringPlaceholder={t("Find secrets","シークレットを検索")} filteringAriaLabel={t("Find secrets","シークレットを検索")} onChange={({detail})=>setFilter(detail.filteringText)}/>} empty={<Box textAlign="center" padding="l">{t("No secrets.","シークレットはありません。")}</Box>}/>}</>},{id:'versions',label:t('Version metadata','バージョンメタデータ'),disabled:busy||!!mode,content:<SecretVersions region={region} request={request} secretARN={detail?.ARN||''} active={section==='versions'}/>}]} /></SpaceBetween></div>
}
