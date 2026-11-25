import React from 'react';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import RadioGroup from '@cloudscape-design/components/radio-group';
import {useLocale} from './i18n.jsx';

export default function VPCSubnetFields({enabled, values, disabled, change}) {
  const {t} = useLocale();
  if (!enabled) return null;
  const paired = values.mode === 'vpc-subnet';
  return <>
    <FormField label={t('Resources to create', '作成するリソース')}><RadioGroup
      value={paired ? 'vpc-subnet' : 'vpc-only'} disabled={disabled}
      onChange={({detail}) => change('mode', detail.value)} items={[
        {value: 'vpc-only', label: t('VPC only', 'VPC のみ')},
        {value: 'vpc-subnet', label: t('VPC and one subnet (CloudFormation-managed)', 'VPC とサブネット1つ（CloudFormation 管理）'),
          description: t('Host only. Creates a new stack and both resources together. Delete them through CloudFormation.', '主催者のみ利用できます。同じ新規スタックに両方を作成し、削除は CloudFormation から行います。')},
      ]}/></FormField>
    {paired && <>
      <FormField label={t('CloudFormation stack name', 'CloudFormation スタック名')}
        constraintText={t('Start with a letter; use up to 128 letters, numbers, and hyphens.', '英字で始め、英数字とハイフンを128文字以内で入力してください。')}>
        <Input value={values.stackName} disabled={disabled} onChange={({detail}) => change('stackName', detail.value)} placeholder="workshop-network"/>
      </FormField>
      <FormField label={t('Subnet IPv4 CIDR', 'サブネット IPv4 CIDR')}
        constraintText={t('A /16–/28 block inside the new VPC, in the selected region’s first local Availability Zone.', '新しい VPC に収まる /16〜/28 の範囲を指定します。選択リージョンの最初のローカル AZ に作成されます。')}>
        <Input value={values.subnetCIDR} disabled={disabled} onChange={({detail}) => change('subnetCIDR', detail.value)} placeholder="10.0.1.0/24"/>
      </FormField>
    </>}
  </>;
}
