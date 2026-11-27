import React,{useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import {batchDefinitionInput} from './batch-job-definitions.mjs';

export default function BatchJobDefinitionForm({busy,error,onCancel,onCreate,t}){
 const [values,setValues]=useState({}),[invalid,setInvalid]=useState([]);
 const fields=[
  ['jobDefinitionName',t('Job definition name','ジョブ定義名'),128,undefined,t('Use 1 to 128 letters, digits, hyphens or underscores.','英数字、ハイフン、アンダースコアを1〜128文字で入力してください。')],
  ['image',t('Image name','イメージ名'),255,undefined,t('Enter a repository name with an optional tag (no digest, URL or registry port).','リポジトリ名と任意のタグを指定してください。ダイジェスト、URL、レジストリのポートは使えません。')],
  ['vcpus',t('vCPUs','vCPU 数'),10,'numeric',t('Enter a whole number from 1 to 2,147,483,647.','1〜2,147,483,647の整数を入力してください。')],
  ['memoryMiB',t('Memory (MiB)','メモリ (MiB)'),10,'numeric',t('Enter a whole number from 4 to 2,147,483,647.','4〜2,147,483,647の整数を入力してください。')],
 ];
 function submit(event){
  event.preventDefault();
  if(busy)return;
  const result=batchDefinitionInput(values);
  setInvalid(result.invalid);
  if(result.input)onCreate(result.input);
 }
 return <form onSubmit={submit}><Form errorText={error||undefined} errorIconAriaLabel={t('Error','エラー')}
  actions={<SpaceBetween direction="horizontal" size="xs">
   <Button formAction="none" variant="link" disabled={busy} onClick={onCancel}>{t('Cancel','キャンセル')}</Button>
   <Button formAction="submit" variant="primary" loading={busy}>{t('Create','作成')}</Button>
  </SpaceBetween>}>
  <SpaceBetween size="l">
   <Alert type="info">{t('Registers a new EC2 single-container metadata revision. No image is pulled and no job is run.','EC2 の単一コンテナ設定を新しいリビジョンとして登録します。イメージの取得やジョブの実行は行いません。')}</Alert>
   <Container header={<Header variant="h2">{t('Job definition settings','ジョブ定義の設定')}</Header>}>
    <SpaceBetween size="m">{fields.map(([key,label,maxLength,inputMode,problem])=>
     <FormField key={key} controlId={'catalog-field-'+key} label={label} errorText={invalid.includes(key)?problem:undefined}>
      <Input value={values[key]??''} disabled={busy} inputMode={inputMode} nativeInputAttributes={{maxLength,'aria-required':true}}
       onChange={e=>{setValues(current=>({...current,[key]:e.detail.value}));setInvalid(current=>current.filter(field=>field!==key))}}/>
     </FormField>)}</SpaceBetween>
   </Container>
  </SpaceBetween>
 </Form></form>;
}
