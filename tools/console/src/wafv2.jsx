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
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import Textarea from '@cloudscape-design/components/textarea';
import TextFilter from '@cloudscape-design/components/text-filter';
import {useLocale} from './i18n.jsx';

const actionChoices=[['Allow','許可'],['Block','ブロック'],['Count','カウント']];
// Local messages can be rendered again after changing languages. Errors
// received from the native API are left verbatim, including their codes.
class WAFViewError extends Error {
 constructor(english,japanese){super(english);this.text=[english,japanese]}
}
const visibility=name=>({CloudWatchMetricsEnabled:false,SampledRequestsEnabled:false,MetricName:name});
const identifier=value=><span className="resource-identifier">{value}</span>;
const actionOf=action=>Object.keys(action||{})[0]||'';
const emptyStatement={ByteMatchStatement:{SearchString:'Lw==',FieldToMatch:{UriPath:{}},TextTransformations:[{Priority:0,Type:'NONE'}],PositionalConstraint:'STARTS_WITH'}};

export default function WAFv2View({region,request,onSaving=()=>{}}){
 const {t}=useLocale();
 const [items,setItems]=useState([]),[detail,setDetail]=useState(null),[token,setToken]=useState(''),[resources,setResources]=useState([]),[selected,setSelected]=useState([]),[tab,setTab]=useState('rules'),[mode,setMode]=useState(''),[form,setForm]=useState({}),[filter,setFilter]=useState(''),[loading,setLoading]=useState(true),[busy,setBusy]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState(''),[conflict,setConflict]=useState(false);
 const alive=useRef(true),generation=useRef(0),abort=useRef(new AbortController()),opener=useRef(null),heading=useRef(null);
 const showError=e=>setError(e instanceof WAFViewError?e.text:e.message);
 async function call(operation,body){const response=await request('/',{method:'POST',signal:abort.current.signal,headers:{'X-Simulator-Service':'wafv2','X-Simulator-Region':region,'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'AWSWAF_20190729.'+operation},body:JSON.stringify(body)});return response.json()}
 async function load(){const revision=++generation.current;setLoading(true);try{const all=[];let marker;const seen=new Set();do{const page=await call('ListWebACLs',{Scope:'REGIONAL',Limit:100,...(marker?{NextMarker:marker}:{})});all.push(...(page.WebACLs||[]));marker=page.NextMarker;if(marker&&seen.has(marker))throw new WAFViewError('Repeated pagination marker.','同じページ送りマーカーが繰り返し返されました。');seen.add(marker)}while(marker&&alive.current&&revision===generation.current);if(alive.current&&revision===generation.current){setItems(all);setError('')}}catch(e){if(alive.current&&revision===generation.current)showError(e)}finally{if(alive.current&&revision===generation.current)setLoading(false)}}
 async function inspect(item){const revision=++generation.current;setLoading(true);try{const result=await call('GetWebACL',{Name:item.Name,Id:item.Id,Scope:'REGIONAL'});const associations=await call('ListResourcesForWebACL',{WebACLArn:result.WebACL.ARN,ResourceType:'API_GATEWAY'});if(alive.current&&revision===generation.current){setDetail(result.WebACL);setToken(result.LockToken);setResources(associations.ResourceArns||[]);setSelected([]);setMode('');setConflict(false);setError('')}}catch(e){if(alive.current&&revision===generation.current)showError(e)}finally{if(alive.current&&revision===generation.current)setLoading(false)}}
 useEffect(()=>{load();return()=>{alive.current=false;generation.current++;abort.current.abort();onSaving(false)}},[]);
 useEffect(()=>{heading.current?.focus()},[mode,detail?.Id]);
 const change=(key,value)=>setForm(old=>({...old,[key]:value}));
 function open(next,values={}){if(busy||loading)return;opener.current=document.activeElement;if(next==='create')setConflict(false);setForm(values);setError('');setMode(next)}
 function cancel(){if(busy)return;setMode('');setError('');requestAnimationFrame(()=>opener.current?.isConnected&&opener.current.focus())}
 async function mutate(operation){if(busy)return;setBusy(true);onSaving(true);setError('');try{await operation()}catch(e){if(alive.current){showError(e);if(/lock|changed|concurrent/i.test(e.message))setConflict(true)}}finally{if(alive.current){setBusy(false);onSaving(false)}}}
 // Send every mutable field from the saved GetWebACL document. Never drop unrelated rules/visibility settings.
 async function update(patch){await call('UpdateWebACL',{Name:detail.Name,Id:detail.Id,Scope:'REGIONAL',LockToken:token,DefaultAction:detail.DefaultAction,Rules:detail.Rules||[],VisibilityConfig:detail.VisibilityConfig,...(detail.Description!==undefined?{Description:detail.Description}:{}),...patch});await inspect(detail)}
 async function submit(){await mutate(async()=>{
  if(mode==='create'){
   if(!/^[\w-]{1,128}$/.test(form.name||''))throw new WAFViewError('Use 1–128 letters, digits, underscores or hyphens for the name.','名前には英数字、アンダースコア、ハイフンを1〜128文字で使用してください。');
   const result=await call('CreateWebACL',{Name:form.name,Scope:'REGIONAL',Description:form.description||'',DefaultAction:{[form.action||'Allow']:{}},Rules:[],VisibilityConfig:visibility(form.name)});await inspect(result.Summary);setMessage(['Protection pack (web ACL) created.','保護パック（ウェブ ACL）を作成しました。']);
  }else if(mode==='settings'){
   await update({Description:form.description,DefaultAction:{[form.action]:{}}});setMessage(['Web ACL settings updated.','ウェブ ACL の設定を更新しました。']);
  }else if(mode==='rule'){
   if(!/^[\w-]{1,128}$/.test(form.name||''))throw new WAFViewError('Enter a valid rule name.','有効なルール名を入力してください。');const priority=Number(form.priority);if(!/^\d+$/.test(String(form.priority))||!Number.isSafeInteger(priority))throw new WAFViewError('Enter a nonnegative integer priority.','優先度には0以上の整数を入力してください。');
   let statement;try{statement=JSON.parse(form.statement)}catch{throw new WAFViewError('Statement must be valid JSON.','ステートメントには有効な JSON を入力してください。')}if(!statement||Array.isArray(statement)||typeof statement!=='object')throw new WAFViewError('Statement must be a JSON object.','ステートメントは JSON オブジェクトである必要があります。');
   const rules=[...(detail.Rules||[])];const original=form.index===undefined?{}:rules[form.index];const rule={...original,Name:form.name,Priority:priority,Action:{[form.action]:{}},Statement:statement,VisibilityConfig:original.VisibilityConfig||visibility(form.name)};
   if(form.index===undefined)rules.push(rule);else rules[form.index]=rule;await update({Rules:rules});setMessage(['Rule saved.','ルールを保存しました。']);
  }else if(mode==='removeRule'){
   await update({Rules:(detail.Rules||[]).filter((_,index)=>index!==form.index)});setMessage(['Rule deleted.','ルールを削除しました。']);
  }else if(mode==='associate'){
   if(!form.arn?.trim())throw new WAFViewError('Enter an API Gateway REST API stage ARN.','API Gateway REST API のステージ ARN を入力してください。');await call('AssociateWebACL',{WebACLArn:detail.ARN,ResourceArn:form.arn.trim()});await inspect(detail);setMessage(['API Gateway stage associated.','API Gateway のステージを関連付けました。']);
  }else if(mode==='disassociate'){
   await call('DisassociateWebACL',{ResourceArn:form.arn});await inspect(detail);setMessage(['API Gateway stage disassociated.','API Gateway のステージの関連付けを解除しました。']);
  }else if(mode==='delete'){
   if(form.confirm!==detail.Name)throw new WAFViewError('Enter the web ACL name to confirm deletion.','削除を確認するにはウェブ ACL の名前を入力してください。');await call('DeleteWebACL',{Name:detail.Name,Id:detail.Id,Scope:'REGIONAL',LockToken:token});setDetail(null);setMode('');await load();setMessage(['Web ACL deleted.','ウェブ ACL を削除しました。']);
  }
 })}
 const disabled=busy||loading;
 const actions=actionChoices.map(([value,japanese])=>({label:t(value,japanese),value}));
 const actionLabel=value=>actions.find(option=>option.value===value)?.label??value;
 const actionSelect=(label,allowed=actions)=><FormField label={label}><Select selectedOption={allowed.find(item=>item.value===form.action)||null} options={allowed} onChange={({detail})=>change('action',detail.selectedOption.value)}/></FormField>;
 const labels={
  name:t('Name','名前'),description:t('Description','説明'),region:t('Region','リージョン'),
  priority:t('Priority','優先度'),action:t('Action','アクション'),rule:t('Rules','ルール'),
  settings:t('Web ACL settings','ウェブ ACL の設定'),resources:t('Associated AWS resources','関連付けられた AWS リソース'),
  arn:t('Resource ARN','リソース ARN'),create:t('Create web ACL','ウェブ ACL を作成'),
  delete:t('Delete web ACL','ウェブ ACL を削除'),removeRule:t('Delete rule','ルールを削除'),
  addRule:t('Add rule','ルールを追加'),editRule:t('Edit rule','ルールを編集'),
  addResource:t('Add AWS resources','AWS リソースを追加'),removeAssociation:t('Remove association','関連付けを解除'),
  find:t('Find web ACLs','ウェブ ACL を検索')
 };
 const title=mode==='create'?t('Create protection pack (web ACL)','保護パック（ウェブ ACL）を作成'):mode==='rule'?(form.index===undefined?labels.addRule:labels.editRule):mode==='settings'?t('Edit web ACL settings','ウェブ ACL の設定を編集'):mode==='associate'?labels.addResource:mode==='disassociate'?t('Remove AWS resource','AWS リソースの関連付けを解除'):mode==='removeRule'?labels.removeRule:labels.delete;
 const editRule=()=>{const rule=selected[0];open('rule',{index:detail.Rules.indexOf(rule),name:rule.Name,priority:String(rule.Priority),action:actionOf(rule.Action),statement:JSON.stringify(rule.Statement,null,2)})};
 const notices=<>
  <Alert type="info" header={t('Independent local AWS WAF simulator','独立したローカル AWS WAF シミュレーター')}>
   {t('Regional API Gateway REST stages only. Bounded AND, byte match and SQL injection statements; SQL injection detection differs from AWS. Managed rules, CloudFront, CAPTCHA, Challenge, logging, metrics and sampled requests are unavailable.',
      'リージョナル API Gateway の REST API ステージのみ対応しています。AND、バイトマッチ、SQL インジェクションの各ステートメントは対応範囲が限られており、SQL インジェクションの検出は AWS と異なります。マネージドルール、CloudFront、CAPTCHA、Challenge、ログ、メトリクス、サンプリングされたリクエストには対応していません。')}
  </Alert>
  {error&&<Alert type="error" header={conflict?t('Web ACL changed in another session','別のセッションでウェブ ACL が変更されました'):t('Request failed','リクエストに失敗しました')}>
   {Array.isArray(error)?t(...error):error}
   {conflict&&<Box margin={{top:'s'}}><Button disabled={busy} onClick={()=>inspect(detail)}>{t('Reload latest web ACL','最新のウェブ ACL を再読み込み')}</Button><Box>{t('Reload discards this draft. Review the latest rules before saving again.','再読み込みすると下書きは破棄されます。最新のルールを確認してから保存してください。')}</Box></Box>}
  </Alert>}
  {message&&<Alert type="success" dismissible i18nStrings={{dismissAriaLabel:t('Dismiss WAF notification','WAF の通知を閉じる')}} onDismiss={()=>setMessage('')}>{t(...message)}</Alert>}
 </>;
 if(mode)return <ContentLayout header={<Header variant="h1"><span ref={heading} tabIndex={-1}>{title}</span></Header>}><SpaceBetween size="l">{notices}<Form errorText={conflict?t('Reload the latest web ACL before saving.','保存する前に最新のウェブ ACL を再読み込みしてください。'):undefined} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={busy} onClick={cancel}>{t('Cancel','キャンセル')}</Button><Button variant="primary" loading={busy} disabled={conflict} onClick={submit}>{mode==='create'?labels.create:mode==='delete'?labels.delete:mode==='removeRule'?labels.removeRule:mode==='associate'?labels.addResource:mode==='disassociate'?labels.removeAssociation:t('Save','保存')}</Button></SpaceBetween>}><Container header={<Header variant="h2">{mode==='rule'?t('Rule builder','ルールビルダー'):title}</Header>}><SpaceBetween size="l">
  {mode==='create'&&<FormField label={labels.name}><Input value={form.name||''} onChange={({detail})=>change('name',detail.value)} autoFocus/></FormField>}
  {(mode==='create'||mode==='settings')&&<SpaceBetween size="l"><FormField label={t('Description - optional','説明（任意）')}><Input value={form.description||''} onChange={({detail})=>change('description',detail.value)}/></FormField><FormField label={labels.region}><Input value={region} readOnly/></FormField>{actionSelect(t('Default web ACL action','ウェブ ACL のデフォルトアクション'),actions.slice(0,2))}</SpaceBetween>}
  {mode==='rule'&&<SpaceBetween size="l"><FormField label={t('Rule name','ルール名')}><Input value={form.name||''} onChange={({detail})=>change('name',detail.value)}/></FormField><FormField label={labels.priority} description={t('Rules run in ascending priority order. Count continues to the next rule.','ルールは優先度の昇順で実行されます。カウントの場合は次のルールに進みます。')}><Input value={form.priority||''} onChange={({detail})=>change('priority',detail.value)} inputMode="numeric"/></FormField>{actionSelect(labels.action)}<FormField label={t('Statement JSON','ステートメントの JSON')} description={t('Native AWS WAF JSON. ByteMatchStatement.SearchString is base64, not plaintext. Unsupported fields are rejected by the API.','AWS WAF のネイティブ JSON を入力します。ByteMatchStatement.SearchString は平文ではなく base64 です。未対応のフィールドは API が拒否します。')}><Textarea rows={16} value={form.statement||''} onChange={({detail})=>change('statement',detail.value)}/></FormField></SpaceBetween>}
  {mode==='associate'&&<FormField label={labels.arn} description={t('Regional API Gateway REST API stage:','リージョナル API Gateway REST API のステージ:')+' arn:aws:apigateway:REGION::/restapis/API_ID/stages/STAGE'}><Input value={form.arn||''} onChange={({detail})=>change('arn',detail.value)}/></FormField>}
  {mode==='disassociate'&&<Box>{t(<>Remove protection from {identifier(form.arn)}? The web ACL and API stage remain.</>,<>{identifier(form.arn)} の保護を解除しますか？ ウェブ ACL と API ステージは残ります。</>)}</Box>}
  {mode==='removeRule'&&<Box>{t(<>Delete rule {form.name}? Other rules and web ACL settings are preserved.</>,<>ルール「{form.name}」を削除しますか？ 他のルールとウェブ ACL の設定は保持されます。</>)}</Box>}
  {mode==='delete'&&<SpaceBetween size="l"><Alert type="warning">{t('Remove resource associations before deleting. This does not delete API Gateway or Lambda resources.','削除する前にリソースの関連付けを解除してください。API Gateway や Lambda のリソースは削除されません。')}</Alert><FormField label={t('Enter web ACL name to confirm','確認のためウェブ ACL の名前を入力')}><Input value={form.confirm||''} onChange={({detail})=>change('confirm',detail.value)}/></FormField></SpaceBetween>}
 </SpaceBetween></Container></Form></SpaceBetween></ContentLayout>;
 if(detail)return <ContentLayout header={<Header variant="h1" description={t('Regional protection pack (web ACL)','リージョナル保護パック（ウェブ ACL）')} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={disabled} onClick={()=>{generation.current++;setDetail(null);setSelected([]);load()}}>{t('Web ACLs','ウェブ ACL 一覧')}</Button><Button disabled={disabled} ariaLabel={t('Refresh web ACL','ウェブ ACL を更新')} iconName="refresh" onClick={()=>inspect(detail)}/><Button disabled={disabled} onClick={()=>open('delete')}>{labels.delete}</Button></SpaceBetween>}><span ref={heading} tabIndex={-1}>{detail.Name}</span></Header>}><SpaceBetween size="l">{notices}<Tabs ariaLabel={t('Web ACL details','ウェブ ACL の詳細')} i18nStrings={{scrollLeftAriaLabel:t('Scroll tabs left','タブを左にスクロール'),scrollRightAriaLabel:t('Scroll tabs right','タブを右にスクロール')}} activeTabId={tab} onChange={({detail})=>setTab(detail.activeTabId)} tabs={[
  {id:'rules',label:labels.rule,content:<Table loading={loading} loadingText={t('Loading rules','ルールを読み込み中')} items={[...(detail.Rules||[])].sort((a,b)=>a.Priority-b.Priority)} selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>setSelected(detail.selectedItems)} ariaLabels={{tableLabel:t('Web ACL rules','ウェブ ACL のルール'),selectionGroupLabel:t('Rule selection','ルールの選択'),itemSelectionLabel:(_data,item)=>t('Select '+item.Name,item.Name+' を選択')}} columnDefinitions={[{id:'name',header:labels.name,cell:item=>item.Name},{id:'priority',header:labels.priority,cell:item=>item.Priority},{id:'action',header:labels.action,cell:item=>actionLabel(actionOf(item.Action))},{id:'statement',header:t('Statement','ステートメント'),cell:item=>Object.keys(item.Statement||{})[0]}]} header={<Header variant="h2" counter={'('+(detail.Rules||[]).length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={disabled||!selected.length} onClick={editRule}>{labels.editRule}</Button><Button disabled={disabled||!selected.length} onClick={()=>open('removeRule',{index:detail.Rules.indexOf(selected[0]),name:selected[0].Name})}>{labels.removeRule}</Button><Button disabled={disabled} variant="primary" onClick={()=>open('rule',{name:'',priority:String(Math.max(-1,...(detail.Rules||[]).map(r=>r.Priority))+1),action:'Count',statement:JSON.stringify(emptyStatement,null,2)})}>{labels.addRule}</Button></SpaceBetween>}>{labels.rule}</Header>} empty={<Box textAlign="center" padding="l">{t('No rules. Requests use the default web ACL action.','ルールはありません。リクエストにはウェブ ACL のデフォルトアクションが適用されます。')}</Box>}/>},
  {id:'resources',label:labels.resources,content:<Table loading={loading} loadingText={t('Loading associations','関連付けを読み込み中')} items={resources} ariaLabels={{tableLabel:labels.resources}} columnDefinitions={[{id:'arn',header:labels.arn,cell:identifier},{id:'remove',header:t('Actions','操作'),cell:arn=><Button disabled={disabled} onClick={()=>open('disassociate',{arn})}>{labels.removeAssociation}</Button>}]} header={<Header variant="h2" actions={<Button disabled={disabled} onClick={()=>open('associate')}>{labels.addResource}</Button>}>{labels.resources}</Header>} empty={<Box textAlign="center" padding="l">{t('No associated resources','関連付けられたリソースはありません')}</Box>}/>},
  {id:'settings',label:labels.settings,content:<Container header={<Header variant="h2" actions={<Button disabled={disabled} onClick={()=>open('settings',{description:detail.Description||'',action:actionOf(detail.DefaultAction)})}>{t('Edit settings','設定を編集')}</Button>}>{labels.settings}</Header>}><KeyValuePairs columns={1} items={[{label:labels.name,value:detail.Name},{label:labels.description,value:detail.Description||'—'},{label:'ARN',value:identifier(detail.ARN)},{label:'ID',value:identifier(detail.Id)},{label:labels.region,value:region},{label:t('Default action','デフォルトアクション'),value:actionLabel(actionOf(detail.DefaultAction))},{label:t('Visibility configuration','可視性の設定'),value:JSON.stringify(detail.VisibilityConfig)}]}/></Container>}
 ]}/></SpaceBetween></ContentLayout>;
 return <ContentLayout header={<Header variant="h1">AWS WAF</Header>}><SpaceBetween size="l">{notices}<Table loading={loading} loadingText={t('Loading web ACLs','ウェブ ACL を読み込み中')} items={items.filter(item=>item.Name.toLowerCase().includes(filter.toLowerCase()))} ariaLabels={{tableLabel:t('Regional web ACLs','リージョナルウェブ ACL')}} columnDefinitions={[{id:'name',header:labels.name,cell:item=><Link onFollow={event=>{event.preventDefault();if(!disabled)inspect(item)}}>{item.Name}</Link>},{id:'id',header:'ID',cell:item=>identifier(item.Id)},{id:'description',header:labels.description,cell:item=>item.Description||'—'},{id:'scope',header:t('Scope','スコープ'),cell:()=>t('Regional','リージョナル')}]} header={<Header variant="h2" counter={'('+items.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={disabled} ariaLabel={t('Refresh web ACLs','ウェブ ACL 一覧を更新')} iconName="refresh" onClick={load}/><Button variant="primary" disabled={disabled} onClick={()=>open('create',{action:'Allow'})}>{labels.create}</Button></SpaceBetween>}>{t('Protection packs (web ACLs)','保護パック（ウェブ ACL）')}</Header>} filter={<TextFilter filteringText={filter} filteringAriaLabel={labels.find} filteringPlaceholder={labels.find} onChange={({detail})=>setFilter(detail.filteringText)}/>} empty={<Box textAlign="center" padding="l">{filter?t('No matching web ACLs','一致するウェブ ACL はありません'):t('No web ACLs','ウェブ ACL はありません')}</Box>}/></SpaceBetween></ContentLayout>;
}
