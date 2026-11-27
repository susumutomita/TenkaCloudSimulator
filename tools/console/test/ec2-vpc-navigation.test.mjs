import test from 'node:test';
import assert from 'node:assert/strict';
import {vpcConsoleAvailability,vpcConsoleHref,vpcConsoleRoute,vpcConsoleSections,vpcResourceHref} from '../src/ec2-vpc-navigation.mjs';
import {internetGatewayPageOperations} from '../src/ec2-internet-gateway-console.mjs';

const vpc='vpc-0123456789abcdef0', region='ap-northeast-1';
const operations=['DescribeVpcs','DescribeInstances','DescribeSubnets','CreateSubnet','DeleteSubnet','DescribeRouteTables','DescribeInternetGateways',
  'CreateInternetGateway','DeleteInternetGateway','AttachInternetGateway','DetachInternetGateway',
  'DescribeNetworkAcls','CreateNetworkAcl','DeleteNetworkAcl','DescribeSecurityGroups'];
const ec2={console:true,operations,routeTableReadback:'DescribeRouteTables',securityGroupReadback:'DescribeSecurityGroups',
  describeSelectors:{DescribeNetworkAcls:['vpc-id']},detachedConfigurationOnly:['native network ACLs'],
  subnetLifecycle:['DescribeSubnets','CreateSubnet','DeleteSubnet'],internetGatewayPageLimit:100,
  internetGatewayLifecycle:['DescribeInternetGateways','CreateInternetGateway','DeleteInternetGateway','AttachInternetGateway','DetachInternetGateway']};
const cap=(overrides={})=>({services:{ec2:{...ec2,...overrides}}});
const target=prefix=>prefix+'-0123456789abcdef0';

test('each VPC inventory and exact typed resource link round trips with region and validated parent',()=>{
  const kinds={vpc:'vpcs',subnet:'subnets','route-table':'route-tables','network-acl':'network-acls',
    'security-group':'security-groups','internet-gateway':'internet-gateways'};
  const ids={vpc,subnet:target('subnet'),'route-table':target('rtb'),'network-acl':target('acl'),
    'security-group':target('sg'),'internet-gateway':target('igw')};
  const available=vpcConsoleAvailability(cap(),true);
  assert.deepEqual(Object.keys(available),vpcConsoleSections);
  for(const [kind,section] of Object.entries(kinds)){
    const vpcHint=(kind==='vpc'||kind==='internet-gateway')?'':vpc;
    const targetUrl=vpcResourceHref(available,{kind,id:ids[kind],region,vpcId:vpc});
    assert.deepEqual(vpcConsoleRoute(targetUrl),{section,region,target:ids[kind],vpc:vpcHint});
    assert.deepEqual(vpcConsoleRoute(vpcConsoleHref(region,section)),{section,region,target:'',vpc:''});
  }
  for (const section of ['vpcs','subnets']) assert.equal(vpcConsoleRoute(vpcConsoleHref(region,section,'create')).target,'create');
  assert.deepEqual(vpcConsoleRoute('#/vpc?view=list'),{section:'vpcs',region:'',target:'',vpc:''});
});

test('rejects malformed IDs, duplicate/external scope and SG detail without its VPC',()=>{
  for(const location of ['#/vpc/vpcs','#/vpc/create','#/vpc/unknown','#/vpc/subnets/rtb-01234567',
    '#/vpc/route-tables/create','#/vpc/security-groups/sg-01234567','#/vpc/route-tables/rtb-01234567\n',
    '#/vpc/internet-gateways?region=us-east-1&vpc='+vpc,
    '#/vpc/subnets?region=https://example.test','#/vpc/subnets?vpc='+vpc+'&vpc='+vpc,
    '#/vpc/subnets?region=us-east-1&region=us-west-2','#/vpc/subnets?view=create',
    '#/vpc/subnets?vpc=vpc-01234567%0A','https://example.test/#/vpc']){
    assert.equal(vpcConsoleRoute(location),null,location);
  }
  const available=vpcConsoleAvailability(cap(),true);
  for(const id of [target('acl'),'https://example.test','#/vpc','local',target('igw')+'\n']){
    assert.equal(vpcResourceHref(available,{kind:'internet-gateway',id,region}), '');
  }
  assert.equal(vpcResourceHref(available,{kind:'security-group',id:target('sg'),region,vpcId:target('igw')}),'');
});

test('ordinary authenticated read disclosure excludes handoff and requires exact advertised Describe operations',()=>{
  for(const section of vpcConsoleSections.slice(1)){
    assert.equal(vpcConsoleAvailability(cap(),false)[section],false);
    assert.equal(vpcConsoleAvailability({...cap(),consoleScope:{instanceId:target('i')}},true)[section],undefined);
    assert.equal(vpcResourceHref(vpcConsoleAvailability(cap(),false),{kind:'route-table',id:target('rtb'),region}),'');
  }
  const required={subnets:'DescribeSubnets','route-tables':'DescribeRouteTables','network-acls':'DescribeNetworkAcls',
    'security-groups':'DescribeSecurityGroups','internet-gateways':'DescribeInternetGateways'};
  for(const [section,action] of Object.entries(required)){
    assert.equal(vpcConsoleAvailability(cap({operations:operations.filter(op=>op!==action)}),true)[section],false);
  }
  assert.deepEqual(vpcConsoleAvailability(cap({operations:operations.filter(op=>op!=='DescribeVpcs')}),true),{});
});

test('gateway operations require both the declared lifecycle operation and the selected read support',()=>{
  assert.deepEqual(internetGatewayPageOperations(cap(),true),{read:true,vpcs:true,create:true,delete:true,attach:true,detach:true});
  for(const [field,action] of Object.entries({create:'CreateInternetGateway',delete:'DeleteInternetGateway',
    attach:'AttachInternetGateway',detach:'DetachInternetGateway'})){
    for(const changed of ['operations','internetGatewayLifecycle']){
      const partial=internetGatewayPageOperations(cap({[changed]:ec2[changed].filter(op=>op!==action)}),true);
      assert.equal(partial[field],false);
      assert.equal(partial.read,true);
    }
  }
  const noVpc=internetGatewayPageOperations(cap({operations:operations.filter(op=>op!=='DescribeVpcs')}),true);
  assert.deepEqual(noVpc,{read:true,vpcs:false,create:true,delete:true,attach:false,detach:false});
  assert.deepEqual(internetGatewayPageOperations(cap({internetGatewayPageLimit:1000}),true),
    {read:false,vpcs:false,create:false,delete:false,attach:false,detach:false});
});
