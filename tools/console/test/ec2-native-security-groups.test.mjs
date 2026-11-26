import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createVpcNativeSecurityGroup, deleteVpcNativeSecurityGroup, loadVpcNativeSecurityGroups,
  nativeSGFailure, nativeSGName, nativeSGTarget, parseVpcNativeSecurityGroups, validNativeSGDescription,
  vpcNativeSecurityGroupsAvailable,
} from '../src/ec2-native-security-groups.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const selected = {id:'vpc-'+ 'a'.repeat(17),managedBy:'ec2'};
const other = {id:'vpc-'+ 'b'.repeat(17),managedBy:'ec2'};
const region = 'us-east-1', id = 'sg-'+'c'.repeat(17);
const arn = `arn:aws:ec2:${region}:000000000000:security-group/${id}`;
const mode = 'detached-config-only';
const draft = {name:'Fixture web  ',description:'Web & service'};
const normalized = {id,name:'Fixture web',description:draft.description,vpc:selected.id,mode,tags:[]};
const capability = {console:true, securityGroupReadback:'DescribeSecurityGroups',
  operations:['DescribeSecurityGroups','CreateSecurityGroup','DeleteSecurityGroup'],
  detachedConfigurationOnly:['native security groups','native network ACLs'],
  describeSelectors:{DescribeSecurityGroups:['GroupId.N','vpc-id']}};
function elem(localName, content=[],namespaceURI=ns) {
  const children = Array.isArray(content) ? content : [];
  return {nodeType:1,namespaceURI,localName,children,childNodes:children,
    textContent:Array.isArray(content)?content.map(c=>c.textContent).join(''):content};
}
const tag = (key,value) => elem('item',[elem('key',key),elem('value',value)]);
const row = ({groupID=id, name='Fixture web', description=draft.description, vpc=selected.id,
  owner='000000000000', ownership=mode, tags=[], rules=[], extra=[]}={}) => elem('item',[
  elem('ownerId',owner),elem('groupId',groupID),elem('groupName',name),elem('groupDescription',description),
  elem('vpcId',vpc),elem('ipPermissions',rules),elem('tagSet',tags),
  ...(ownership===null?[]:[elem('simulatorSecurityGroupMode',ownership)]),...extra,
]);
const doc = (action,fields=[],request='local-request') => ({documentElement:elem(action+'Response',[elem('requestId',request),...fields])});
const describe = rows => doc('DescribeSecurityGroups',[elem('securityGroupInfo',rows)]);
const created = (opts={}) => doc('CreateSecurityGroup',[
  elem('groupId',opts.id??id),elem('securityGroupArn',opts.arn??arn),elem('return',opts.ok??'true'),...(opts.extra??[]),
]);
const removed = (groupID=id,ok='true') => doc('DeleteSecurityGroup',[elem('groupId',groupID),elem('return',ok)]);
const requestWith = (responses,seen=[]) => async (path,opts)=>{
  seen.push({path,...opts});
  assert.ok(responses.length,'unexpected request (including retries)');
  const reply = responses.shift();
  return {ok:true,status:200,text:async()=>reply};
};
const parse = x=>x;

test('authenticated ordinary VPC lifecycle is explicitly advertised, otherwise no native controls',()=>{
  const can = (ec2,auth=true,other={}) => vpcNativeSecurityGroupsAvailable({services:{ec2},...other},auth);
  assert.equal(can(capability),true);
  assert.equal(can(capability,false),false);
  assert.equal(can(capability,true,{consoleScope:{instanceId:'i-fixture'}}),false);
  for (const patch of [
    {console:false},{securityGroupReadback:true},{operations:'all'},
    {operations:capability.operations.slice(0,2)},{detachedConfigurationOnly:['native network ACLs']},
    {describeSelectors:{DescribeSecurityGroups:['GroupId.N']}},
  ]) assert.equal(can({...capability,...patch}),false);
  assert.equal(nativeSGTarget(selected),true);
  assert.equal(nativeSGTarget({...selected,managedBy:'cloudformation'}),false);
  assert.equal(nativeSGTarget({id:'vpc-ignored',managedBy:'ec2'}),false);
});

test('input normalization mirrors bounded native Query name and description semantics',()=>{
  assert.equal(nativeSGName('Fixture web  '),'Fixture web');
  assert.equal(nativeSGName('a & [] + *'),'a & [] + *');
  for(const value of [undefined,42,' ',' sg–','sg-name','SG-name','Default   ','非ASCII','g\n','a'.repeat(256)]) {
    assert.equal(nativeSGName(value),'');
  }
  assert.equal(validNativeSGDescription(draft.description),true);
  assert.equal(validNativeSGDescription('x'.repeat(255)),true);
  for(const value of [null,'','x'.repeat(256),'a<unsafe>','日']) assert.equal(validNativeSGDescription(value),false);
});

test('saved readback requires exact selected VPC, local owner, detached metadata, and no effective rules',()=>{
  assert.deepEqual(parseVpcNativeSecurityGroups(describe([]),selected),[]);
  assert.deepEqual(parseVpcNativeSecurityGroups(describe([row()]),selected),[normalized]);
  assert.deepEqual(parseVpcNativeSecurityGroups(describe([row({tags:[tag('Key','学習'),tag('Empty','')]})]),selected)[0].tags,
    [{key:'Key',value:'学習'},{key:'Empty',value:''}]);
  for (const data of [
    {...describe([row()]),doctype:{}},doc('DescribeSecurityGroups'),doc('DescribeSecurityGroups',[elem('securityGroupInfo')],''),
    doc('DescribeSecurityGroups',[elem('securityGroupInfo'),elem('nextToken','cursor')]),
    describe([row({vpc:other.id})]),describe([row({owner:'111122223333'})]),
    describe([row({ownership:null})]),describe([row({ownership:'active'})]),
    describe([row({extra:[elem('ipPermissionsEgress')]})]),describe([row({rules:[elem('item')]})]),
    describe([row({name:'default'})]),describe([row({name:'space '})]),
    describe([row(),row()]),describe([row(),row({groupID:'sg-'+'d'.repeat(17),name:'fixture Web'})]),
    describe([row({tags:[tag('A','1'),tag('A','2')]})]),describe([row({tags:[tag('aws:x','1')]})]),
    describe([row({tags:[tag('Key','日'.repeat(86))]})]),describe([elem('unexpected')]),
  ]) assert.throws(()=>parseVpcNativeSecurityGroups(data,selected),/Invalid or incomplete/);
});

test('create uses the real action and requires matching scoped saved metadata before reporting success',async()=>{
  const seen=[], signal=new AbortController().signal;
  const actual=await createVpcNativeSecurityGroup(requestWith([created(),describe([row()])],seen),region,selected,draft,{parse,signal});
  assert.deepEqual(actual,{created:normalized,rows:[normalized]});
  assert.deepEqual(seen.map(o=>[o.path,o.method,o.redirect,o.signal,Object.fromEntries(o.body)]),[
    ['/','POST','error',signal,{Action:'CreateSecurityGroup',Version:'2016-11-15',VpcId:selected.id,GroupName:'Fixture web',GroupDescription:draft.description}],
    ['/','POST','error',signal,{Action:'DescribeSecurityGroups',Version:'2016-11-15','Filter.1.Name':'vpc-id','Filter.1.Value.1':selected.id}],
  ]);
  for (const req of seen) assert.deepEqual(req.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region});
  for (const result of [[],
    [row({description:'different'})],[row({tags:[tag('unasked','value')]})],[row({vpc:other.id})]]) {
    await assert.rejects(createVpcNativeSecurityGroup(requestWith([created(),describe(result)]),region,selected,draft,{parse}),/Invalid or incomplete/);
  }
  await assert.rejects(createVpcNativeSecurityGroup(requestWith([created({arn:arn.replace(region,'ap-northeast-1')})]),region,selected,draft,{parse}),/Invalid or incomplete/);
  for(const vpc of [other.id,{...selected,managedBy:'cloudformation'}]) {
    await assert.rejects(createVpcNativeSecurityGroup(async()=>assert.fail('unverified scope must not write'),region,vpc,draft,{parse}),/Invalid or incomplete/);
  }
});

test('delete binds the saved group, requires explicit positive receipt and confirms it has disappeared',async()=>{
  const seen=[];
  assert.deepEqual(await deleteVpcNativeSecurityGroup(requestWith([removed(),describe([])],seen),region,selected,normalized,{parse}),
    {removed:normalized,rows:[]});
  assert.deepEqual(Object.fromEntries(seen[0].body),{Action:'DeleteSecurityGroup',Version:'2016-11-15',GroupId:id});
  for(const receipt of [removed('sg-'+'d'.repeat(17)),removed(id,'false')]) {
    await assert.rejects(deleteVpcNativeSecurityGroup(requestWith([receipt]),region,selected,normalized,{parse}),/Invalid or incomplete/);
  }
  await assert.rejects(deleteVpcNativeSecurityGroup(requestWith([removed(),describe([row()])]),region,selected,normalized,{parse}),/Invalid or incomplete/);
  for(const group of [{...normalized,vpc:other.id},{...normalized,mode:'active'},{...normalized,id:'sg-invalid'}]) {
    await assert.rejects(deleteVpcNativeSecurityGroup(async()=>assert.fail('wrong owner/scope must not write'),region,selected,group,{parse}),/Invalid or incomplete/);
  }
});

test('abort discards stale responses and a blocked or ambiguous mutation is never retried',async()=>{
  const stop = new AbortController(), seen=[];
  await assert.rejects(loadVpcNativeSecurityGroups(async()=>{
    seen.push('read');
    return {ok:true,text:async()=>{stop.abort();return describe([row()]);}};
  },region,selected,{signal:stop.signal,parse:()=>assert.fail('stale reply must not be parsed')}),{name:'AbortError'});
  await assert.rejects(loadVpcNativeSecurityGroups(async()=>assert.fail('aborted scope must not issue request'),region,selected,{signal:stop.signal}),{name:'AbortError'});
  assert.deepEqual(seen,['read']);
  let count=0;
  await assert.rejects(createVpcNativeSecurityGroup(async()=>{count++;throw Error('UnauthorizedOperation: denied');},region,selected,draft,{parse}),
    error=>nativeSGFailure(error)==='denied');
  assert.equal(count,1);
  assert.equal(nativeSGFailure(Error('InvalidGroup.Duplicate: conflict')),'duplicate');
  assert.equal(nativeSGFailure(Error('DependencyViolation: dependency')),'dependency');
  assert.equal(nativeSGFailure(Error('unknown completion')),'unconfirmed');
});
