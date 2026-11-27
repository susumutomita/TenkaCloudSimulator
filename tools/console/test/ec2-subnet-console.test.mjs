import test from 'node:test';
import assert from 'node:assert/strict';
import {subnetConsoleHref,subnetConsoleRoute,subnetRouteTable} from '../src/ec2-subnet-console-route.mjs';
import {loadRegionalSubnetInventory} from '../src/ec2-native-subnets.mjs';

const vpc='vpc-1234567890abcdef0', foreign='vpc-2234567890abcdef0', subnet='subnet-1234567890abcdef0';
const region='us-east-1';
const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const field=(localName,textContent='')=>({localName,textContent,namespaceURI:ns,children:[]});
const elem=(name,...children)=>({...field(name),children});
const response=(action,set,...items)=>({documentElement:elem(action+'Response',field('requestId','request'),elem(set,...items))});
const network=(id=vpc,owner='ec2')=>elem('item',field('vpcId',id),field('cidrBlock','10.0.0.0/16'),field('simulatorManagedBy',owner));
function item({id=subnet,parent=vpc,owner='ec2',cidr='10.0.1.0/24',zone=region+'a',tag=''}={}) {
  return elem('item',field('subnetId',id),field('vpcId',parent),field('cidrBlock',cidr),field('state','available'),
    ...(owner==='ec2'?[field('availabilityZone',zone),field('simulatorManagedBy',owner),field('mapPublicIpOnLaunch','false')]:[]),
    ...(tag?[elem('tagSet',elem('item',field('key','Name'),field('value',tag)))]:[]));
}
function transport(vpcs,subnets) {
  const calls=[];
  const request=async(path,options)=>{
    assert.equal(path,'/');
    assert.equal(options.headers['X-Simulator-Region'],region);
    assert.equal(options.redirect,'error');
    const action=options.body.get('Action');
    assert.ok(['DescribeVpcs','DescribeSubnets'].includes(action));
    calls.push(action);
    return new Response(action,{headers:{'Content-Type':'application/xml'}});
  };
  const parse=action=>action==='DescribeVpcs'?response(action,'vpcSet',...vpcs):response(action,'subnetSet',...subnets);
  return {request,parse,calls};
}

test('localized views deep link only a specific typed subnet, create, or list; selector is optional',()=>{
  assert.deepEqual(subnetConsoleRoute(subnetConsoleHref(region,subnet,vpc)),{target:subnet,vpc});
  assert.deepEqual(subnetConsoleRoute(subnetConsoleHref(region,'create')),{target:'create',vpc:''});
  assert.deepEqual(subnetConsoleRoute('#/vpc/subnets?vpc=invalid'),{target:'',vpc:''});
  for(const bad of ['#/vpc','passive','#/vpc/subnets/create/extra','#/vpc/subnets/subnet-INVALID'])
    assert.equal(subnetConsoleRoute(bad),null);
});

test('inventory confirms one regional VPC read and one subnet read, name only from the saved stack', async()=>{
  const x=transport([network(),network(foreign,'cloudformation')],
    [item(),item({id:'subnet-2234567890abcdef0',parent:foreign,owner:'cloudformation',tag:'Saved tag'})]);
  const {vpcs,subnets}=await loadRegionalSubnetInventory(x.request,region,{parse:x.parse,publicIPv4:true});
  assert.deepEqual(x.calls,['DescribeVpcs','DescribeSubnets']);
  assert.equal(vpcs.length,2);
  assert.deepEqual(subnets.map(row=>[row.owner,row.name,row.state]),[['ec2','','available'],['cloudformation','Saved tag','available']]);
});

test('unknown or cross-owner VPC, cross-range or cross-zone entries and duplicate IDs reject the complete inventory',async()=>{
  for(const rows of [[item({parent:foreign})],[item({owner:'cloudformation'})],[item({cidr:'10.1.0.0/16'})],
    [item({zone:'us-west-2a'})],[item(),item()]]) {
    const x=transport([network()],rows);
    await assert.rejects(loadRegionalSubnetInventory(x.request,region,{parse:x.parse,publicIPv4:true}),/Invalid subnet/);
  }
});

test('association prefers exact explicit relation, then a verified native main, not a foreign match or stale guess',()=>{
  const s={id:subnet,vpc,owner:'ec2'};
  const main={id:'rtb-main',vpc,mainAssociation:{},associations:[]};
  const explicit={id:'rtb-explicit',vpc,associations:[{subnet}]};
  assert.deepEqual(subnetRouteTable([main,explicit],s),{id:explicit.id,kind:'explicit'});
  assert.deepEqual(subnetRouteTable([main],s),{id:main.id,kind:'main'});
  assert.equal(subnetRouteTable([{...main,vpc:foreign}],s),null);
  assert.throws(()=>subnetRouteTable([{...explicit,vpc:foreign}],s),/Unconfirmed/);
  assert.throws(()=>subnetRouteTable([explicit,explicit],s),/Unconfirmed/);
  assert.throws(()=>subnetRouteTable([main],{...s,owner:'cloudformation'}),/Unconfirmed/);
});
