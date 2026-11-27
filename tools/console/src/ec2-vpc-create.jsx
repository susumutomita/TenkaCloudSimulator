import React, {useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import RadioGroup from '@cloudscape-design/components/radio-group';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import {useLocale} from './i18n.jsx';
import {VPCCreateMode, VPCStackOwnedFields} from './ec2-vpc-subnet-fields.jsx';
import {vpcCreateIssues} from './ec2-vpc-subnet.mjs';

export default function VPCCreate({values, pairedAvailable, busy, requestError, heading, change, submit, cancel}) {
  const {t} = useLocale();
  const [touched, setTouched] = useState({});
  const paired = pairedAvailable && values.mode === 'vpc-subnet';
  const issues = vpcCreateIssues(values, paired);
  const messages = {
    cidr: values.cidr.trim() ? t('Enter a valid IPv4 CIDR between /16 and /28.', '/16〜/28 の有効な IPv4 CIDR を入力してください。')
      : t('Enter an IPv4 CIDR.', 'IPv4 CIDR を入力してください。'),
    subnet: t('Enter a valid subnet IPv4 CIDR between /16 and /28.', 'サブネットに /16〜/28 の有効な IPv4 CIDR を入力してください。'),
    containment: t('The subnet must fit inside the new VPC’s IPv4 CIDR.', 'サブネットの IPv4 CIDR は新しい VPC の範囲に収めてください。'),
    name: t('The Name tag can have up to 256 UTF-8 bytes.', '名前タグは UTF-8 で 256 バイト以内にしてください。'),
    stack: t('Enter a valid CloudFormation stack name.', '有効な CloudFormation スタック名を入力してください。'),
  };
  const errors = {subnet: issues.subnet === 'containment' || touched.subnetCIDR ? messages[issues.subnet] : undefined,
    stack: touched.stackName ? messages[issues.stack] : undefined};
  const defaultTenancy = {value: 'default', label: t('Default', 'デフォルト')};
  const unsupported = t('Not available in this simulator.', 'このシミュレーターでは利用できません。');
  const edit = (key, value) => {
    if (key !== 'mode') setTouched(old => ({...old, [key]:true}));
    change(key, value);
  };
  const field = key => ({detail}) => edit(key, detail.value);

  return <ContentLayout header={<div ref={heading} tabIndex={-1}><Header variant="h1"
    description={t('Create an isolated local network using an IPv4 address range.', 'IPv4 アドレス範囲を指定して、分離されたローカルネットワークを作成します。')}>{t('Create VPC', 'VPC を作成')}</Header></div>}>
    <form onSubmit={submit} data-testid="vpc-create-form">
      <Form errorText={requestError} actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={busy} onClick={cancel}>{t('Cancel', 'キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={busy} disabled={busy || Object.keys(issues).length > 0}>{t('Create VPC', 'VPC を作成')}</Button>
      </SpaceBetween>}>
        <SpaceBetween size="l">
          <Container header={<Header variant="h2">{t('VPC settings', 'VPC の設定')}</Header>}>
            <SpaceBetween size="l">
              <VPCCreateMode enabled={pairedAvailable} values={values} disabled={busy} change={edit}/>
              <div id="vpc-topology-settings">
                <SpaceBetween size="l">
                  <FormField label={t('Name tag - optional', '名前タグ - オプション')}
                    description={t('Adds a tag with the key Name to the VPC.', 'VPC にキーが Name のタグを追加します。')}
                    errorText={touched.name ? messages[issues.name] : undefined}>
                    <Input value={values.name} placeholder="my-vpc" disabled={busy} onChange={field('name')}/>
                  </FormField>
                  <FormField label={t('IPv4 CIDR block', 'IPv4 CIDR ブロック')}>
                    <RadioGroup value="manual" disabled={busy} items={[
                      {value: 'manual', label: t('IPv4 CIDR manual input', 'IPv4 CIDR の手動入力')},
                      {value: 'ipam', label: t('IPAM-allocated IPv4 CIDR block', 'IPAM で割り当てられた IPv4 CIDR ブロック'), disabled: true, description: unsupported},
                    ]}/>
                  </FormField>
                  <FormField label="IPv4 CIDR" errorText={touched.cidr ? messages[issues.cidr] : undefined}
                    constraintText={t('CIDR block size must be between /16 and /28.', 'CIDR ブロックのサイズは /16 から /28 の間である必要があります。')}>
                    <Input value={values.cidr} placeholder="10.0.0.0/16" disabled={busy} onChange={field('cidr')}/>
                  </FormField>
                  <FormField label={t('IPv6 CIDR block', 'IPv6 CIDR ブロック')}>
                    <RadioGroup value="none" disabled={busy} items={[
                      {value: 'none', label: t('No IPv6 CIDR block', 'IPv6 CIDR ブロックなし')},
                      {value: 'amazon', label: t('Amazon-provided IPv6 CIDR block', 'Amazon 提供の IPv6 CIDR ブロック'), disabled: true, description: unsupported},
                    ]}/>
                  </FormField>
                  <FormField label={t('Tenancy', 'テナンシー')}
                    constraintText={t('Only default tenancy is available locally.', 'ローカルではデフォルトテナンシーのみ使えます。')}>
                    <Select selectedOption={defaultTenancy} options={[defaultTenancy]} disabled/>
                  </FormField>
                  <VPCStackOwnedFields enabled={pairedAvailable} values={values} disabled={busy} change={edit} errors={errors}/>
                  <Alert type="info">{paired ? t(
                    'This option creates only a VPC and one subnet. It does not create a security group, route table, or Internet/NAT gateway. Delete both resources through their stack.',
                    'この選択肢で作るのは VPC とサブネット 1 つだけです。セキュリティグループ、ルートテーブル、インターネット/NAT ゲートウェイは作りません。削除はスタックから行います。') : t(
                    'A main route table is created with this VPC. After creation, VPC details shows the options available to your current identity. This does not launch an instance.',
                    'VPC とメインルートテーブルを作成します。作成後の VPC 詳細には、現在の ID で利用できる操作が表示されます。この画面からインスタンスは起動しません。')}</Alert>
                </SpaceBetween>
              </div>
            </SpaceBetween>
          </Container>
          <Container header={<Header variant="h2">{t('Tags', 'タグ')}</Header>}>
            <SpaceBetween size="s">
              <div>{t('This form saves only the optional Name tag shown above.', 'この画面から保存できるのは、上で指定した任意の Name タグのみです。')}</div>
              {values.name && <KeyValuePairs columns={1} items={[{label: 'Name', value: values.name}]}/>}
            </SpaceBetween>
          </Container>
          {busy && <Alert type="info">{paired ? t('Creating the stack and checking what it created…', 'スタックを作成し、作成結果を確認中…') : t('Creating and reloading the VPC…', 'VPC を作成し、一覧を更新中…')}</Alert>}
        </SpaceBetween>
      </Form>
    </form>
  </ContentLayout>;
}
