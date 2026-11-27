import React from 'react';
import Alert from '@cloudscape-design/components/alert';
import AttributeEditor from '@cloudscape-design/components/attribute-editor';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import {nativeSubnetTagLimit} from './ec2-native-subnets.mjs';
import {useLocale} from './i18n.jsx';

export function SubnetNameField({available,disabled,value,onChange,error}) {
  const {t}=useLocale();
  return <FormField label={t('Subnet name - optional','サブネット名 - オプション')}
    constraintText={available ? t('Saved as the Name tag. Up to 256 UTF-8 bytes.','Name タグとして保存します。UTF-8 で 256 バイトまでです。')
      : t('This session does not support naming or tagging new subnets.','このセッションではサブネット作成時の名前やタグを設定できません。')}
    errorText={available&&error ? t('Use valid text of up to 256 UTF-8 bytes.','UTF-8 で 256 バイト以内の有効な文字を入力してください。') : undefined}>
    <Input value={available ? value : ''} disabled={!available||disabled} onChange={({detail})=>onChange(detail.value)}/>
  </FormField>;
}

export default function SubnetTagFields({rows,onChange,disabled,draft,hasName}) {
  const {t}=useLocale();
  const rowLimit = nativeSubnetTagLimit - (hasName ? 1 : 0);
  const keyErrors={
    invalid:t('Enter a non-reserved key of 1–128 UTF-8 bytes. Keys cannot start with aws:.','予約済みでない UTF-8 で 1〜128 バイトのキーを入力してください。キーを aws: から始めることはできません。'),
    duplicate:t('Tag keys must be unique.','同じタグキーを複数指定できません。'),
    name:t('Set Name in the subnet name field above.','Name は上のサブネット名欄で設定してください。'),
  };
  const edit=(index,field,value)=>{
    if (!disabled) onChange(rows.map((row,i)=>i===index ? {...row,[field]:value} : row));
  };
  return <Container header={<Header variant="h2">{t('Tags - optional','タグ - オプション')}</Header>}>
    <SpaceBetween size="m">
      {draft.overLimit&&<Alert type="error">{t('A subnet can have up to 50 tags, including Name.','サブネットに設定できるタグは、Name を含めて最大 50 個です。')}</Alert>}
      <AttributeEditor items={rows} addButtonText={t('Add new tag','新しいタグを追加')}
        removeButtonText={t('Remove','削除')} removeButtonAriaLabel={row=>row.key ? t('Remove tag: ','タグを削除: ')+row.key : t('Remove empty tag','空のタグを削除')}
        disableAddButton={disabled||rows.length>=rowLimit} isItemRemovable={()=>!disabled}
        additionalInfo={t('Up to 50 tags, including Name. Values can be empty. Tags are saved only at creation.','Name を含めて 50 個までです。値は空でも構いません。この画面でタグを保存できるのは作成時のみです。')}
        onAddButtonClick={()=>{if(!disabled&&rows.length<rowLimit)onChange([...rows,{key:'',value:''}]);}}
        onRemoveButtonClick={({detail})=>{if(!disabled)onChange(rows.filter((_,index)=>index!==detail.itemIndex));}}
        definition={[
          {label:t('Key','キー'),
            errorText:(_,index)=>keyErrors[draft.errors[index]?.key],
            control:(row,index)=><Input value={row.key} disabled={disabled}
              ariaLabel={t('Tag key ','タグキー ')+(index+1)} onChange={({detail})=>edit(index,'key',detail.value)}/>},
          {label:t('Value - optional','値 - オプション'),
            errorText:(_,index)=>draft.errors[index]?.value ? t('Use valid text of up to 256 UTF-8 bytes.','UTF-8 で 256 バイト以内の有効な文字を入力してください。') : undefined,
            control:(row,index)=><Input value={row.value} disabled={disabled}
              ariaLabel={t('Tag value ','タグの値 ')+(index+1)} onChange={({detail})=>edit(index,'value',detail.value)}/>},
        ]}/>
    </SpaceBetween>
  </Container>;
}
