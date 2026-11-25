import React,{useState} from 'react';
import {useLocale} from './i18n.jsx';
import {appStreamAppBlockInput} from './appstream-app-block.mjs';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import RadioGroup from '@cloudscape-design/components/radio-group';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Textarea from '@cloudscape-design/components/textarea';
import Toggle from '@cloudscape-design/components/toggle';

// Bounded metadata portion of the AWS Applications Manager create flow.
// No S3 picker, builder, package creation or script execution is represented.
export default function AppStreamAppBlockForm({busy,error,onCancel,onCreate}){
 const {t}=useLocale();
 const [values,setValues]=useState({packaging:'APPSTREAM2',importMode:'new'});
 const [problems,setProblems]=useState({}),[review,setReview]=useState(null);
 const custom=values.packaging==='CUSTOM', scriptKey=custom?'setup':'postSetup';
 const set=(key,value)=>{setValues(v=>({...v,[key]:value}));setProblems(p=>({...p,[key]:undefined}));};
 const setScript=(key,value)=>{setValues(v=>({...v,[scriptKey]:{...v[scriptKey],[key]:value}}));setProblems(p=>({...p,[key]:undefined}));};
 const changePackaging=packaging=>{setValues(v=>({...v,packaging,source:''}));setProblems({});};
 const optionNames={APPSTREAM2:t('WorkSpaces Applications','WorkSpaces Applications'),CUSTOM:t('Custom','カスタム')};
 const save=e=>{
  e.preventDefault();
  if(busy)return;
  if(review){onCreate(review);return;}
  const {input,errors}=appStreamAppBlockInput(values);setProblems(errors);
  if(input){if(custom)onCreate(input);else setReview(input)}
 };
 const input=(key,label,options={})=><FormField key={key} label={label} controlId={'appblock-'+key} errorText={problems[key]}>
   <Input value={(options.script?values[scriptKey]?.[key]:values[key])??''} disabled={busy}
    inputMode={key==='timeout'?'numeric':undefined} placeholder={options.placeholder}
    onChange={e=>(options.script?setScript:set)(key,e.detail.value)}/>
  </FormField>;
 const scriptFields=post=><SpaceBetween size="m">
  {input('uri',post?t('Post setup script object in S3','S3 内のセットアップ後スクリプト'):t('Setup script object in S3','S3 内のセットアップスクリプト'),{script:true,placeholder:'s3://example-bucket/setup.ps1'})}
  {input('executable',post?t('Post setup script executable','セットアップ後の実行ファイル'):t('Setup script executable','セットアップの実行ファイル'),{script:true})}
  {input('args',t('Setup script executable arguments (optional)','実行引数（任意）'),{script:true})}
  {input('timeout',t('Execution duration in seconds','実行時間（秒）'),{script:true})}
 </SpaceBetween>;
 const disclaimer=<Alert type="info">{t('Only configuration metadata is stored. S3 objects are not read, no script is run, and no app block builder, package, fleet or streaming session is started.','保存するのは設定メタデータのみです。S3 オブジェクトの読み込みやスクリプト実行は行わず、App Block Builder、パッケージ作成、フリート、ストリーミングセッションは起動しません。')}</Alert>;
 const sourceLabel=custom?t('Virtual hard disk object in S3','S3 内の仮想ハードディスク'):
  values.importMode==='existing'?t('App package object in S3','S3 内のアプリパッケージ'):t('S3 location','S3 の場所');
 return <form onSubmit={save}><Form errorText={error||undefined} actions={<SpaceBetween direction="horizontal" size="xs">
  <Button formAction="none" variant="link" disabled={busy} onClick={onCancel}>{t('Cancel','キャンセル')}</Button>
  {review&&<Button formAction="none" disabled={busy} onClick={()=>setReview(null)}>{t('Previous','前へ')}</Button>}
  <Button formAction="submit" variant="primary" loading={busy}>{review||custom?t('Create app block','App Block を作成'):t('Next','次へ')}</Button>
 </SpaceBetween>}>
 <SpaceBetween size="l">
  {disclaimer}
  {review?<Container header={<Header variant="h2">{t('Review app block details','App Block の詳細を確認')}</Header>}>
     <KeyValuePairs columns={2} items={[{label:t('Name','名前'),value:review.Name},
      {label:t('Packaging','パッケージ形式'),value:optionNames[review.PackagingType]},
      {label:t('Display name','表示名'),value:review.DisplayName||'–'},
      {label:t('Description','説明'),value:review.Description||'–'},
      {label:t('S3 location','S3 の場所'),value:values.source},
      ...(review.PostSetupScriptDetails?[{label:t('Post setup script','セットアップ後のスクリプト'),value:values.postSetup.uri},
       {label:t('Executable','実行ファイル'),value:review.PostSetupScriptDetails.ExecutablePath},
       {label:t('Executable arguments','実行引数'),value:review.PostSetupScriptDetails.ExecutableParameters||'–'},
       {label:t('Execution duration in seconds','実行時間（秒）'),value:String(review.PostSetupScriptDetails.TimeoutInSeconds)}]:[]),
      ...(review.Tags?[{label:t('Tags','タグ'),value:Object.entries(review.Tags).map(([key,value])=>key+'='+value).join(', ')}]:[])]}/>
    </Container>:<>
   <Container header={<Header variant="h2">{t('App block packaging','App Block のパッケージ形式')}</Header>}>
    <FormField label={t('Packaging','パッケージ形式')} errorText={problems.packaging}>
     <RadioGroup value={values.packaging} onChange={e=>changePackaging(e.detail.value)} items={Object.entries(optionNames).map(([value,label])=>({value,label,disabled:busy}))}/>
    </FormField>
   </Container>
   <Container header={<Header variant="h2">{t('App block details','App Block の詳細')}</Header>}>
    <SpaceBetween size="m">{input('name',t('Name','名前'))}
     {input('displayName',t('Display name (optional)','表示名（任意）'))}
     <FormField label={t('Description (optional)','説明（任意）')} controlId="appblock-description" errorText={problems.description}>
      <Textarea value={values.description??''} disabled={busy} onChange={e=>set('description',e.detail.value)}/>
     </FormField>
    </SpaceBetween>
   </Container>
   {custom?<Container header={<Header variant="h2">{t('Script settings','スクリプト設定')}</Header>}>
     <SpaceBetween size="m">{input('source',sourceLabel,{placeholder:'s3://example-bucket/app.vhd'})}{scriptFields(false)}</SpaceBetween>
    </Container>:<>
    <ExpandableSection variant="container" headerText={t('Advanced options','詳細オプション')}>
     <SpaceBetween size="m"><Toggle checked={!!values.withPostSetup} disabled={busy} onChange={e=>set('withPostSetup',e.detail.checked)}>{t('Configure a post setup script','セットアップ後のスクリプトを設定')}</Toggle>
     {values.withPostSetup&&scriptFields(true)}</SpaceBetween>
    </ExpandableSection>
    <Container header={<Header variant="h2">{t('Import settings','インポート設定')}</Header>}>
     <SpaceBetween size="m"><FormField label={t('App block application file','App Block アプリケーションファイル')}>
      <RadioGroup value={values.importMode} onChange={e=>{setValues(v=>({...v,importMode:e.detail.value,source:''}));setProblems(p=>({...p,source:undefined}));}}
       items={[{value:'new',label:t('Create new app block application file','新しい App Block アプリケーションファイル'),disabled:busy},
        {value:'existing',label:t('Use an existing app package','既存のアプリパッケージを使用'),disabled:busy}]}/>
     </FormField>{input('source',sourceLabel,{placeholder:values.importMode==='existing'?'s3://example-bucket/app.vhd':'s3://example-bucket'})}</SpaceBetween>
    </Container>
   </>}
   <ExpandableSection variant="container" headerText={t('Tags (optional)','タグ（任意）')}>
    <FormField label={t('Tags','タグ')} controlId="appblock-tags" description={t('One key=value per line.','1 行に 1 つの key=value を入力します。')} errorText={problems.tags}>
     <Textarea value={values.tags??''} disabled={busy} onChange={e=>set('tags',e.detail.value)}/>
    </FormField>
   </ExpandableSection>
  </>}
 </SpaceBetween>
 </Form></form>;
}
