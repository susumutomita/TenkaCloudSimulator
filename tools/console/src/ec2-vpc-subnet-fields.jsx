import React from 'react';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Tiles from '@cloudscape-design/components/tiles';
import {useLocale} from './i18n.jsx';

export function VPCCreateMode({enabled, values, disabled, change}) {
  const {t} = useLocale();
  const items = [{
    value: 'vpc-only',
    label: t('VPC only', 'VPC のみ'),
    description: t('Create a VPC with a main route table. No subnet is created.', 'VPC とメインルートテーブルを作成します。サブネットは作成しません。'),
    disabled,
  }];
  if (enabled) items.push({
    value: 'vpc-subnet',
    label: t('VPC and more', 'VPC など'),
    description: t('Creates a VPC and one subnet in a new CloudFormation stack.', '新規の CloudFormation スタックで VPC とサブネット 1 つを作成します。'),
    disabled,
  });
  return <FormField label={t('Resources to create', '作成するリソース')}>
    <Tiles value={enabled && values.mode === 'vpc-subnet' ? 'vpc-subnet' : 'vpc-only'}
      columns={items.length} items={items} ariaControls="vpc-topology-settings"
      onChange={({detail}) => change('mode', detail.value)}/>
  </FormField>;
}

export function VPCStackOwnedFields({enabled, values, disabled, change, errors = {}}) {
  const {t} = useLocale();
  if (!enabled || values.mode !== 'vpc-subnet') return null;
  return <SpaceBetween size="l">
    <FormField label={t('CloudFormation stack name', 'CloudFormation スタック名')}
      errorText={errors.stack}
      constraintText={t('Start with a letter; use up to 128 letters, numbers, and hyphens.', '英字で始め、英数字とハイフンを128文字以内で入力してください。')}>
      <Input value={values.stackName} disabled={disabled} onChange={({detail}) => change('stackName', detail.value)} placeholder="workshop-network"/>
    </FormField>
    <FormField label={t('Subnet IPv4 CIDR', 'サブネット IPv4 CIDR')}
      errorText={errors.subnet}
      constraintText={t('Enter a /16–/28 block inside the VPC’s IPv4 CIDR.', 'VPC の IPv4 CIDR に収まる /16〜/28 の範囲を指定します。')}>
      <Input value={values.subnetCIDR} disabled={disabled} onChange={({detail}) => change('subnetCIDR', detail.value)} placeholder="10.0.1.0/24"/>
    </FormField>
  </SpaceBetween>;
}
