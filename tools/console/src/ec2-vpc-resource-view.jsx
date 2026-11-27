import React,{useCallback,useEffect,useRef} from 'react';
import Alert from '@cloudscape-design/components/alert';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import EC2SubnetsConsole from './ec2-subnets-console.jsx';
import EC2RouteTableConsole from './ec2-route-table-console.jsx';
import NetworkACLInventory from './ec2-network-acl-inventory.jsx';
import SecurityGroupsConsole from './ec2-security-groups-console.jsx';
import InternetGatewaysPage from './ec2-internet-gateway-page.jsx';
import {vpcNativeSubnetPublicIPv4Available} from './ec2-native-subnets.mjs';
import {internetGatewayPageOperations} from './ec2-internet-gateway-console.mjs';
import {vpcConsoleHref,vpcResourceHref,vpcConsoleSectionName} from './ec2-vpc-navigation.mjs';
import {useLocale} from './i18n.jsx';

// One mounted view, one global write lease. Existing resource panels retain
// their own operation lock/readback; reporting and release are scoped to this
// view so retiring a route cannot unlock another view's later write.
export default function VPCResourceView({
  route, region, request, capabilities, authenticationRequired, available, acquireWrite, navigate,
}) {
  const {t}=useLocale();
  const reported=useRef(null);
  const onSaving=useCallback(open=>{
    if (open) reported.current ||= acquireWrite();
    else {reported.current?.();reported.current=null;}
  },[acquireWrite]);
  useEffect(()=>()=>onSaving(false),[onSaving]);
  const section=route?.section;
  if (!section || !available[section]) return <ContentLayout header={<Header variant="h1">
    {section?vpcConsoleSectionName(section,t):'VPC'}</Header>}>
    <Alert type="info">{t('This VPC view is unavailable in this console session.',
      'このコンソールセッションでは、この VPC 画面を利用できません。')}</Alert>
  </ContentLayout>;
  const hrefForResource=resource=>vpcResourceHref(available,{...resource,region});
  const openResource=resource=>{const href=hrefForResource(resource);if(href)navigate(href);};
  const select=(target, vpc=route.vpc, replace=false)=>{
    const href=vpcConsoleHref(region, section, target, vpc);
    if (href) navigate(href,{replace});
  };
  const shared={request,region,capabilities,authenticationRequired,onSaving};
  if (section==='subnets') return <EC2SubnetsConsole {...shared} route={route}
    canCreate={capabilities.services.ec2.operations.includes('CreateSubnet')} hrefForResource={hrefForResource}
    publicIPv4={vpcNativeSubnetPublicIPv4Available(capabilities,authenticationRequired)} showRoutes={available['route-tables']}/>;
  if (section==='route-tables') return <EC2RouteTableConsole {...shared} routeTableId={route.target}
    vpcId={route.vpc} hrefForResource={hrefForResource} onSelectRouteTable={select}/>;
  if (section==='network-acls') return <NetworkACLInventory {...shared} initialVpcId={route.vpc} initialAclId={route.target}
    onSelectAcl={select} onOpenSubnet={available.subnets?({vpcId,subnetId})=>openResource({kind:'subnet',id:subnetId,vpcId}):undefined}/>;
  if (section==='security-groups') return <SecurityGroupsConsole {...shared} vpcId={route.vpc}
    groupId={route.target} onSelectResource={select}/>;
  if (section==='internet-gateways') return <InternetGatewaysPage request={request} region={region}
    gatewayId={route.target} onNavigateGateway={id=>select(id,'')}
    onOpenVpc={available.vpcs?id=>openResource({kind:'vpc',id}):undefined}
    operations={internetGatewayPageOperations(capabilities,authenticationRequired)} acquireWrite={acquireWrite}/>;
  return null;
}
