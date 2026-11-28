import assert from 'node:assert/strict';
import test from 'node:test';
import {actions, branchCreated, branchUpdated, matchesSavedRule, notificationClient,
  notificationRulesAvailable, ruleDraft, ruleInput, service} from '../src/codestar-notifications.mjs';

const region = 'us-east-1';
const arn = 'arn:aws:codestar-notifications:us-east-1:000000000000:notificationrule/' + 'a'.repeat(40);
const source = 'arn:aws:codecommit:us-east-1:000000000000:practice';
const topic = 'arn:aws:sns:us-east-1:000000000000:practice';
const shape = {Arn:arn, Name:'Rules', Resource:source, CreatedBy:'creator', DetailType:'BASIC', Status:'ENABLED',
  Targets:[{TargetType:'SNS',TargetAddress:topic,TargetStatus:'ACTIVE'}],
  EventTypes:[{EventTypeId:branchCreated},{EventTypeId:branchUpdated}]};

test('ordinary authenticated capability requires the full native set', () => {
  const config = {console:true,protocol:'REST JSON',operations:actions};
  assert.equal(notificationRulesAvailable({services:{[service]:config}}),true);
  assert.equal(notificationRulesAvailable({consoleScope:{instanceId:'bound'},services:{[service]:config}}),false);
  assert.equal(notificationRulesAvailable({services:{[service]:{...config,operations:actions.slice(1)}}}),false);
});

test('draft preserves actual source, events and targets and checks regional forms', () => {
  const draft=ruleDraft(shape);
  const input=ruleInput(draft,region);
  assert.equal(input.Resource,source);
  assert.deepEqual(input.Targets,[{TargetType:'SNS',TargetAddress:topic}]);
  assert.equal(ruleInput(draft,'eu-west-1'),null);
  assert.equal(ruleInput({...draft,topics:topic+'\n'+topic},region),null);
  assert.equal(ruleInput({...draft,created:false,updated:false},region),null);
  assert.equal(ruleInput({...draft,topics:topic+'.fifo'},region),null);
  assert.deepEqual(ruleInput({...draft,topics:''},region,arn).Targets,[]);
  assert.ok(!Object.hasOwn(ruleInput(draft,region,arn),'Resource'));
  assert.equal(matchesSavedRule(shape,input),true);
  assert.equal(matchesSavedRule({...shape,EventTypes:[{EventTypeId:branchCreated}]},input),false);
  assert.equal(matchesSavedRule({...shape,Status:'DISABLED'},input),false);
});

test('five actual regional REST paths and return identities are retained', async () => {
  const calls=[];
  const records=[{NotificationRules:[{Arn:arn,Id:'a'.repeat(40)}],NextToken:'next'},shape,{Arn:arn},{},{Arn:arn}];
  const api=notificationClient(async (path,options) => {
    calls.push([path,options]);
    return new Response(JSON.stringify(records.shift()));
  },region);
  const signal=new AbortController().signal;
  assert.equal((await api.list('previous',signal)).NextToken,'next');
  assert.equal((await api.detail(arn,signal)).Resource,source);
  const input=ruleInput(ruleDraft(shape),region);
  assert.equal(await api.write('CreateNotificationRule',{...input,ClientRequestToken:'retained'},signal),arn);
  assert.equal(await api.write('UpdateNotificationRule',{Arn:arn,Status:'DISABLED'},signal),arn);
  assert.equal(await api.write('DeleteNotificationRule',{Arn:arn},signal),arn);
  assert.deepEqual(calls.map(([p])=>p),['/listNotificationRules','/describeNotificationRule','/createNotificationRule','/updateNotificationRule','/deleteNotificationRule']);
  for (const [,options] of calls) {
    assert.equal(options.method,'POST');
    assert.deepEqual(options.headers,{'Content-Type':'application/json','X-Simulator-Service':service,'X-Simulator-Region':region});
    assert.equal(options.signal,signal);
  }
  assert.deepEqual(JSON.parse(calls[0][1].body),{MaxResults:20,NextToken:'previous'});
  assert.equal(JSON.parse(calls[2][1].body).ClientRequestToken,'retained');
});

test('canceled or malformed reads and wrong-identity writes cannot become acknowledged', async () => {
  const unrelated=arn.replaceAll('aaaaaaaa','bbbbbbbb');
  const signal=new AbortController().signal;
  const fixed=value=>notificationClient(async()=>new Response(JSON.stringify(value)),region);
  await assert.rejects(fixed({...shape,Arn:unrelated}).detail(arn,signal));
  await assert.rejects(fixed({NotificationRules:[{Arn:arn,Id:'other'}]}).list('',signal));
  await assert.rejects(fixed({Arn:unrelated}).write('DeleteNotificationRule',{Arn:arn},signal));
  await assert.rejects(fixed({Arn:arn}).write('UpdateNotificationRule',{Arn:arn},signal));
  await assert.rejects(fixed(shape).detail(arn,AbortSignal.abort()),{name:'AbortError'});
});
