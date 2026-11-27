import React, {useEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import {useLocale} from './i18n.jsx';
import VPCSecurityGroups from './ec2-vpc-security-groups.jsx';
import {securityGroupInventoryAvailable, securityGroupInventoryVPCs, securityGroupVPCProfile} from './ec2-security-group-inventory.mjs';

/** Mount with the existing region/principal/navigation key and scoped request callback. */
export default function SecurityGroupsConsole({request, region, capabilities, authenticationRequired, onSaving, vpcId='', groupId='', onSelectResource}) {
  const {t} = useLocale();
  const available = securityGroupInventoryAvailable(capabilities, authenticationRequired);
  const [refresh, setRefresh] = useState(0), [read, setRead] = useState(null);
  const [choice, setChoice] = useState(null), [writing, setWriting] = useState(false);
  const lease = useRef(null);
  const scope = useMemo(() => ({request, region, available, refresh}), [request, region, available, refresh]);
  const current = read?.scope === scope ? read : null;
  const vpcs = current?.rows?.flatMap(vpc => {
    const profile = securityGroupVPCProfile(vpc, capabilities, authenticationRequired);
    return profile ? [{vpc, profile}] : [];
  }) ?? [];
  const options = vpcs.map(({vpc}) => ({value: vpc.id, label: vpc.name || vpc.id,
    description: vpc.name ? vpc.id : undefined}));
  const selected = vpcs.find(row => row.vpc.id === (choice?.scope === scope ? choice.id : vpcId));

  useEffect(() => {
    if (!scope.available) return;
    const controller = new AbortController();
    securityGroupInventoryVPCs(scope.request, scope.region, {signal: controller.signal}).then(rows => {
      if (!controller.signal.aborted) setRead({scope, rows});
    }, error => {
      if (!controller.signal.aborted) setRead({scope, denied: /^(401|403|UnauthorizedOperation):|^Sign in to continue/.test(error?.message ?? '')});
    });
    return () => controller.abort();
  }, [scope]);

  function acquireWrite() {
    if (lease.current) return null;
    const release = () => {
      if (lease.current !== release) return;
      lease.current = null;
      setWriting(false);
      onSaving?.(false);
    };
    lease.current = release;
    setWriting(true);
    onSaving?.(true);
    return release;
  }
  const title = t('Security groups', 'セキュリティグループ');
  return <ContentLayout header={<Header variant="h1" actions={available &&
      <Button disabled={writing || !current} onClick={() => {
        if (!lease.current) setRefresh(n => n + 1);
      }}>{t('Refresh VPCs', 'VPC を更新')}</Button>}>{title}</Header>}>
    <SpaceBetween size="l">
      <Alert type="info">{t('Saved configuration only. Security group rules do not affect traffic in the simulator.',
        '保存された設定を表示します。セキュリティグループのルールはシミュレーター内の通信に影響しません。')}</Alert>
      {!available ? <Alert type="warning">{t('Security group inventory is unavailable for this simulator session.',
        'このシミュレーターセッションではセキュリティグループを参照できません。')}</Alert>
      : !current ? <StatusIndicator type="loading">{t('Loading VPCs', 'VPC を読み込み中')}</StatusIndicator>
      : !current.rows ? <Alert type="error">{current.denied
        ? t('The current identity cannot read the VPC inventory.', '現在の ID では VPC 一覧を参照できません。')
        : t('Could not read VPCs. Refresh VPCs to try again.', 'VPC 一覧を取得できません。「VPC を更新」で再試行してください。')}</Alert>
      : !options.length ? <p role="status">{t('No VPCs with supported security group readback.', 'セキュリティグループを表示できる VPC はありません。')}</p>
      : <>
        <FormField label={t('VPC filter', 'VPC フィルター')} description={t('Select the VPC whose saved security groups you want to see.',
          '保存済みセキュリティグループを表示する VPC を選択します。')}>
          <Select options={options} selectedOption={options.find(o => o.value === selected?.vpc.id) ?? null}
            placeholder={t('Select a VPC', 'VPC を選択')} filteringType="auto" disabled={writing}
            onChange={({detail}) => {
              const id = detail.selectedOption.value;
              if (!lease.current && options.some(row => row.value === id)) {
                if(onSelectResource) onSelectResource('', id);
                else setChoice({scope, id});
              }
            }}/>
        </FormField>
        {vpcId&&!selected&&<Alert type="info">{t('This VPC is unavailable for security group readback in this region.',
          'このリージョンでは、この VPC のセキュリティグループを確認できません。')}</Alert>}
        {selected && <VPCSecurityGroups key={selected.vpc.id + '|' + selected.profile} vpc={selected.vpc}
          region={region} request={request} inventory nativeLifecycle={selected.profile !== 'stack-read-only'}
          nativeRules={selected.profile === 'native-rules'} acquireWrite={acquireWrite} groupId={groupId}
          onOpenGroup={onSelectResource ? (id,replace)=>onSelectResource(id,selected.vpc.id,replace) : undefined}/>}
      </>}
    </SpaceBetween>
  </ContentLayout>;
}
