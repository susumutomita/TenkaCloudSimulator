import React, {useEffect, useMemo, useState} from 'react';
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
import {loadVpcRouteTables, routeTableFailure, vpcRouteScope} from './ec2-route-tables.mjs';

const wrap = value => <span className="resource-identifier">{value}</span>;

function Details({table}) {
  const {t} = useLocale();
  const route = table.routes[0];
  return <SpaceBetween size="m">
    <div><Box variant="h4">{t('Enabled IPv4 default route','有効な IPv4 デフォルトルート')}</Box>
      {route ? <span>{wrap(route.cidr)} → {wrap(route.gateway)}</span> : t('No enabled IPv4 default route is recorded.', '有効な IPv4 デフォルトルートは記録されていません。')}</div>
    <Table variant="embedded" trackBy="id" items={table.associations}
      header={<Box variant="h4">{t('Explicit subnet associations (not main)', '明示的なサブネット関連付け（メインではありません）')}</Box>}
      columnDefinitions={[
        {id:'subnet',header:t('Subnet ID','サブネット ID'),cell:a=>wrap(a.subnet)},
        {id:'association',header:t('Association ID','関連付け ID'),cell:a=>wrap(a.id)},
      ]} empty={t('No explicit subnet associations are recorded.','明示的なサブネット関連付けは記録されていません。')}/>
    <Table variant="embedded" trackBy="key" items={table.tags}
      header={<Box variant="h4">{t('Route table tags','ルートテーブルのタグ')}</Box>}
      columnDefinitions={[
        {id:'key',header:t('Key','キー'),cell:tag=>wrap(tag.key)},
        {id:'value',header:t('Value','値'),cell:tag=>tag.value===''?t('(empty)','（空）'):wrap(tag.value)},
      ]} empty={t('No tags are recorded.','タグは記録されていません。')}/>
  </SpaceBetween>;
}

export function RouteTableReadout({status,tables=[],preferred='',onSelect}) {
  const {t} = useLocale();
  if (status==='native') return <Box>{t('This view reads CloudFormation-managed VPCs only. No lookup was made.','この画面の参照対象は CloudFormation 管理の VPC のみです。問い合わせは行っていません。')}</Box>;
  if (status==='unverified') return <Box>{t('Select a verified CloudFormation-managed VPC to read its route tables.','ルートテーブルを表示するには CloudFormation 管理と確認できる VPC を選択してください。')}</Box>;
  if (status==='loading') return <StatusIndicator type="loading">{t('Loading route tables','ルートテーブルを読み込み中')}</StatusIndicator>;
  if (status==='denied') return <Alert type="error" header={t('Route table access denied','ルートテーブルの参照が拒否されました')}>{t('A signed-in identity needs ec2:DescribeRouteTables. No route table data was shown.','サインイン済みの ID に ec2:DescribeRouteTables 権限が必要です。ルートテーブル情報は表示していません。')}</Alert>;
  if (status!=='ready') return <Alert type="error" header={t('Could not read route tables','ルートテーブルを読み取れませんでした')}>{t('Use Refresh route tables to try again.','「ルートテーブルを更新」で再試行してください。')}</Alert>;
  if (!tables.length) return <div role="status">{t('No explicit route tables are recorded for this VPC.', 'この VPC に記録された明示的なルートテーブルはありません。')}</div>;
  const table = tables.find(item=>item.id===preferred) ?? tables[0];
  const options = tables.map(item=>({label:item.id,value:item.id}));
  return <SpaceBetween size="m">
    <FormField label={t('Route table','ルートテーブル')}><Select options={options}
      selectedOption={options.find(option=>option.value===table.id)} onChange={({detail})=>onSelect(detail.selectedOption.value)}/></FormField>
    <Details table={table}/>
  </SpaceBetween>;
}

export default function VPCRouteTables({vpc,region,request}) {
  const {t} = useLocale();
  const [result,setResult] = useState(null), [preferred,setPreferred] = useState(''), [revision,refresh] = useState(0);
  const {id,managedBy} = vpc;
  const target = vpcRouteScope(vpc);
  // Bind each result to its request identity, VPC, region and refresh generation.
  // Old data must disappear even before effect cleanup and if fetch ignores abort.
  const scope = useMemo(()=>({request,region,id,managedBy,revision}),[request,region,id,managedBy,revision]);
  useEffect(()=>{
    if (target!=='stack') return;
    const controller = new AbortController();
    loadVpcRouteTables(scope.request,scope.region,scope,{signal:controller.signal}).then(tables=>{
      if (!controller.signal.aborted) setResult({scope,status:'ready',tables});
    }, error=>{
      if (!controller.signal.aborted) setResult({scope,status:routeTableFailure(error)});
    });
    return ()=>controller.abort();
  }, [scope,target]);
  const current = result?.scope===scope ? result : null;
  const status = target==='stack' ? current?.status ?? 'loading' : target;
  return <SpaceBetween size="s">
    <Header variant="h3" actions={target==='stack'&&<Button iconName="refresh" loading={status==='loading'}
      onClick={()=>refresh(revision=>revision+1)}>{t('Refresh route tables','ルートテーブルを更新')}</Button>}>
      {t('Route tables','ルートテーブル')}
    </Header>
    <Box color="text-body-secondary">{t('Saved configuration only. This view does not add implicit main tables or local routes, and does not test network reachability.', '保存済みの設定のみを表示します。暗黙のメインテーブルやローカルルートは補わず、通信の到達性は検証しません。')}</Box>
    <RouteTableReadout status={status} tables={current?.tables} preferred={preferred} onSelect={setPreferred}/>
  </SpaceBetween>;
}
