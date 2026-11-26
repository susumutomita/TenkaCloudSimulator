import React, {useEffect, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {loadVpcSecurityGroups, vpcSecurityGroupsFailure} from './ec2-security-groups.mjs';

const loading = {status:'loading', groups:[]};
const wrap = text => <span className="resource-identifier">{text}</span>;
export function SecurityGroupDetails({group}) {
  const {t} = useLocale();
  const inbound = t('Recorded inbound rules (TCP/IPv4)', '記録された受信ルール (TCP/IPv4)');
  return <SpaceBetween size="m">
    <div><Box variant="h4">{t('Description', '説明')}</Box>{wrap(group.description)}</div>
    <div><Box variant="h4">{inbound}</Box>{group.ingress.length
      ? <ul aria-label={inbound}>{group.ingress.map(({from,to,cidr}, index) => <li key={index}>{wrap('TCP '+(from===to ? from : from+'–'+to)+' · '+cidr)}</li>)}</ul>
      : <Box color="text-body-secondary">{t('No inbound rules are recorded.', '受信ルールは記録されていません。')}</Box>}</div>
    <Table variant="embedded" trackBy="key" items={group.tags}
      header={<Box variant="h4">{t('Security group tags', 'セキュリティグループのタグ')}</Box>}
      columnDefinitions={[
        {id:'key',header:t('Key','キー'),cell:tag=>wrap(tag.key)},
        {id:'value',header:t('Value','値'),cell:tag=>tag.value===''?t('(empty)','（空）'):wrap(tag.value)},
      ]}
      empty={<Box color="text-body-secondary">{t('No tags are recorded.', 'タグは記録されていません。')}</Box>}/>
  </SpaceBetween>;
}

export default function VPCSecurityGroups({vpc, region, request}) {
  const {t} = useLocale();
  const [state, setState] = useState(loading), [selected, setSelected] = useState(''), [refresh, setRefresh] = useState(0);
  const id = vpc.id, managedBy = vpc.managedBy;
  const applicable = managedBy === 'cloudformation';
  useEffect(() => {
    if (!applicable) return;
    const pending = new AbortController();
    setState(loading);
    loadVpcSecurityGroups(request, region, {id, managedBy}, {signal:pending.signal}).then(groups => {
      if (!pending.signal.aborted) setState({status:'ready', groups});
    }, error => {
      if (!pending.signal.aborted) setState({status:vpcSecurityGroupsFailure(error), groups:[]});
    });
    return () => pending.abort();
  }, [request, region, id, managedBy, applicable, refresh]);

  const options = state.groups.map(group => ({value:group.id,label:group.id}));
  const group = state.groups.find(g => g.id === selected) ?? state.groups[0];
  const title = t('Security groups','セキュリティグループ');
  return <SpaceBetween size="m">
    <Header variant="h3" actions={applicable && <Button iconName="refresh" loading={state.status==='loading'} onClick={() => {
      setState(loading);setRefresh(v=>v+1);
    }}>{t('Refresh security groups','セキュリティグループを更新')}</Button>}>{title}</Header>
    {!applicable ? <Box color="text-body-secondary">{managedBy==='ec2'
      ? t('This pane displays CloudFormation-managed groups only. Detached EC2 security groups are available through the native API.', 'この画面は CloudFormation 管理のグループ専用です。未接続の EC2 セキュリティグループはネイティブ API で操作できます。')
      : t('Security group readback requires a CloudFormation-managed VPC.', 'セキュリティグループの読み取りには CloudFormation 管理の VPC が必要です。')}</Box>
      : state.status==='loading' ? <StatusIndicator type="loading">{t('Loading security groups','セキュリティグループを読み込み中')}</StatusIndicator>
      : state.status==='denied' ? <Alert type="error" header={t('Security group access denied','セキュリティグループの参照が拒否されました')}>{t('The current identity needs ec2:DescribeSecurityGroups. No group data was shown.', '現在の ID には ec2:DescribeSecurityGroups が必要です。グループ情報は表示していません。')}</Alert>
      : state.status==='error' ? <Alert type="error" header={t('Could not read security groups','セキュリティグループを読み取れませんでした')}>{t('Use Refresh security groups to try again.', '「セキュリティグループを更新」で再試行してください。')}</Alert>
      : group ? <SpaceBetween size="m">
        <FormField label={t('Security group','セキュリティグループ')} description={t('Saved rules only. This view does not verify network traffic.', '保存済みルールのみを表示しています。この画面では通信を検証しません。')}>
          <Select selectedOption={options.find(option=>option.value===group.id)} options={options} onChange={({detail})=>setSelected(detail.selectedOption.value)}/>
        </FormField>
        <SecurityGroupDetails group={group}/>
      </SpaceBetween> : <div role="status">{t('No security groups are recorded for this VPC.', 'この VPC に記録されたセキュリティグループはありません。')}</div>}
  </SpaceBetween>;
}
