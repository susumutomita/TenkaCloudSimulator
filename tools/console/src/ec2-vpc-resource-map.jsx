import React,{useEffect,useMemo,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import Pagination from '@cloudscape-design/components/pagination';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import {useLocale} from './i18n.jsx';
import {vpcConsoleAvailability,vpcResourceHref} from './ec2-vpc-navigation.mjs';
import {createVpcResourceMapScope,startVpcResourceMapRead} from './ec2-vpc-topology-read.mjs';

const pageSize=15;
const idText=id=><span className="resource-identifier">{id}</span>;

function DataState({status,t}) {
  if (status==='ready') return null;
  if (status==='unsupported') return <Box color="text-body-secondary">
    {t('This inventory is not available in this session.','このセッションでは一覧を参照できません。')}
  </Box>;
  if (status==='unconfirmed') return <Alert type="warning">
    {t('The inventories needed to confirm these relationships are not available.',
      '関連付けの確認に必要な一覧を取得できません。')}
  </Alert>;
  if (status==='inconsistent') return <Alert type="warning">
    {t('The saved records disagree. This inventory was not confirmed.',
      '保存済みの情報が一致しないため、この一覧を確認できませんでした。')}
  </Alert>;
  if (status==='partial') return <Alert type="warning">
    {t('Some saved routes did not match an attached gateway. Only confirmed relationships are shown.',
      '一部の保存済みルートを接続済みゲートウェイと照合できません。確認できた関連付けだけを表示します。')}
  </Alert>;
  return <Alert type="error">{status==='denied'
    ?t('The current identity cannot read this inventory.','現在の ID ではこの一覧を参照できません。')
    :t('The inventory could not be confirmed. Refresh the resource map to try again.',
      '一覧を確認できませんでした。「リソースマップを更新」で再試行してください。')}</Alert>;
}

function MapSection({label,section,emptyMessage,children,rowKey,t}) {
  const [page,setPage]=useState(1);
  const count=Math.ceil(section.rows.length/pageSize);
  const current=Math.min(page,Math.max(count,1));
  return <section aria-label={label}><SpaceBetween size="xs">
    <Header variant="h4">{label}</Header>
    <DataState status={section.status} t={t}/>
    {section.rows.length>0
      ?<ul aria-label={label}>{section.rows.slice((current-1)*pageSize,current*pageSize).map(row=><li key={rowKey(row)}>{children(row)}</li>)}</ul>
      :section.status==='ready'&&<Box>{emptyMessage}</Box>}
    {count>1&&<Pagination currentPageIndex={current} pagesCount={count} onChange={({detail})=>setPage(detail.currentPageIndex)}
      ariaLabels={{previousPageLabel:t('Previous page','前のページ'),nextPageLabel:t('Next page','次のページ'),
        pageLabel:n=>t('Page ','ページ ')+n,paginationLabel:label}}/>}
  </SpaceBetween></section>;
}

export function VPCResourceMapContents({snapshot,region,available,navigationBlocked=false,t=en=>en}) {
  const {vpc,subnets,routeTables,gateways,gatewayRoutes,subnetAssociationStatus}=snapshot;
  const resource=(kind,id)=>{
    const href=navigationBlocked?'':vpcResourceHref(available??{},{kind,id,region,vpcId:vpc.id});
    return href?<Link href={href}>{idText(id)}</Link>:idText(id);
  };
  const routeLabel=t('Route table:','ルートテーブル:');
  const gatewayLabel=t('Internet gateway:','インターネットゲートウェイ:');
  return <SpaceBetween size="m">
    <Box><strong>VPC:</strong> {idText(vpc.id)} · {vpc.cidr}</Box>
    {subnetAssociationStatus==='inconsistent'&&<Alert type="warning">
      {t('Subnet and route table records disagree. Subnet associations are not shown.',
        'サブネットとルートテーブルの情報が一致しないため、サブネットの関連付けを表示していません。')}
    </Alert>}
    <MapSection label={t('Subnets and route table associations','サブネットとルートテーブルの関連付け')}
      section={subnets} rowKey={row=>row.id} t={t}
      emptyMessage={t('No subnets are recorded for this VPC.','この VPC にサブネットはありません。')}>
      {row=><SpaceBetween size="xxs">
        <Box>{resource('subnet',row.id)} · {row.cidr}{row.az?' · '+row.az:''}</Box>
        <Box>{routeLabel} {row.association
          ?<>{resource('route-table',row.association.id)} {'('}{row.association.kind==='explicit'
            ?t('Explicit association','明示的な関連付け'):t('Main route table, implicit','メインルートテーブルへの暗黙の関連付け')}{')'}</>
          :t('Association not confirmed','関連付けは未確認')}</Box>
      </SpaceBetween>}
    </MapSection>
    <MapSection label={t('Route tables','ルートテーブル')} section={routeTables} rowKey={row=>row.id} t={t}
      emptyMessage={t('No route tables are recorded for this VPC.','この VPC にルートテーブルはありません。')}>
      {row=><Box>{resource('route-table',row.id)} {row.main&&t('(Verified main)','（確認済みのメイン）')}</Box>}
    </MapSection>
    <MapSection label={t('Attached internet gateways','接続済みインターネットゲートウェイ')}
      section={gateways} rowKey={row=>row.id} t={t}
      emptyMessage={t('No internet gateway is recorded as attached to this VPC.','この VPC に接続済みのインターネットゲートウェイはありません。')}>
      {row=><Box>{resource('internet-gateway',row.id)}</Box>}
    </MapSection>
    <MapSection label={t('Routes to attached internet gateways','接続済みインターネットゲートウェイへのルート')}
      section={gatewayRoutes} rowKey={row=>row.table+'|'+row.cidr} t={t}
      emptyMessage={t('No routes to attached internet gateways are recorded for this VPC.',
        'この VPC に接続済みインターネットゲートウェイへのルートはありません。')}>
      {row=><SpaceBetween size="xxs">
        <Box>{routeLabel} {resource('route-table',row.table)}</Box>
        <Box>{t('Destination:','送信先:')} {row.cidr}</Box>
        <Box>{gatewayLabel} {resource('internet-gateway',row.gateway)}</Box>
      </SpaceBetween>}
    </MapSection>
  </SpaceBetween>;
}

// Mount only in the Details view of the selected, currently read VPC. Hosts
// using a Details tab can set active=false outside the Resource map tab.
export default function VPCResourceMap({request,region,vpc,capabilities,authenticationRequired,
  active=true,navigationBlocked=false}) {
  const {t}=useLocale();
  const [revision,setRevision]=useState(0),[receipt,setReceipt]=useState(null);
  const id=vpc?.id, cidr=vpc?.cidr, managedBy=vpc?.managedBy;
  const scope=useMemo(()=>active?createVpcResourceMapScope(request,region,{id,cidr,managedBy},capabilities,authenticationRequired):null,
    [active,request,region,id,cidr,managedBy,capabilities,authenticationRequired,revision]);
  useEffect(()=>{
    if (!scope) return;
    return startVpcResourceMapRead(scope,setReceipt).cancel;
  },[scope]);
  const current=receipt?.scope===scope?receipt:null;
  if (!scope) return null;
  return <SpaceBetween size="s">
    <Header variant="h3" actions={<Button iconName="refresh" disabled={!current} onClick={()=>setRevision(n=>n+1)}>
      {t('Refresh resource map','リソースマップを更新')}
    </Button>}>{t('Resource map','リソースマップ')}</Header>
    <Box color="text-body-secondary">{t('Saved local configuration. These relationships are not a test of traffic or Internet access.',
      'ローカルの保存済み設定です。通信やインターネット接続の確認結果ではありません。')}</Box>
    {!current?<StatusIndicator type="loading">{t('Loading resource map','リソースマップを読み込み中')}</StatusIndicator>
      :current.error?<Alert type="error">{t('Could not read the resource map.','リソースマップを読み込めませんでした。')}</Alert>
      :<VPCResourceMapContents snapshot={current.snapshot} region={region}
        available={vpcConsoleAvailability(capabilities,authenticationRequired)} navigationBlocked={navigationBlocked} t={t}/>}
  </SpaceBetween>;
}
