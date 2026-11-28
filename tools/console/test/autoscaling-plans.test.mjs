import assert from 'node:assert/strict';
import test from 'node:test';
import {capacityBounds, dimensions, newPlan, scalingPlanClient} from '../src/autoscaling-plans.mjs';
import {serviceAvailable, serviceFromHash} from '../src/console-navigation.mjs';

const service = 'autoscaling-plans';
const operations = ['CreateScalingPlan','DescribeScalingPlans','DescribeScalingPlanResources','UpdateScalingPlan','DeleteScalingPlan'];
const capabilities = {services:{[service]: {console:true, targetBilling:'PROVISIONED', forecastData:false, predictiveScaling:false,
  targets:Object.values(dimensions), operations}, dynamodb:{operations:['DescribeTable']}}};
const input = {name:'local', tag:'environment', tagValue:'dev', table:'orders', dimension:'read', minimum:'5', maximum:'8'};
const instruction = newPlan(input).ScalingInstructions[0];
const existing = {ScalingPlanName:'local', ScalingPlanVersion:1, CreationTime:10, StatusCode:'Active', ScalingInstructions:[instruction]};
const signal = new AbortController().signal;

test('route requires the shipped plan operations, both dimensions and real readback', () => {
  assert.equal(serviceFromHash('#/autoscaling-plans'), service);
  assert.equal(serviceAvailable(service, capabilities), true);
  for (const alter of [c => {c.consoleScope={instanceId:'i-1'}}, c => {c.services.dynamodb.operations=[]},
    c => {c.services[service].targetBilling='PAY_PER_REQUEST'}, c => {c.services[service].targets.pop()},
    c => {c.services[service].console=false}, ...operations.map(op => c => {c.services[service].operations=operations.filter(v => v!==op)})]) {
    const copy=structuredClone(capabilities); alter(copy); assert.equal(serviceAvailable(service,copy),false);
  }
});
test('create uses a named existing table and bounded integer capacity', () => {
  assert.equal(instruction.ResourceId,'table/orders');
  assert.equal(instruction.ScalableDimension,dimensions.read);
  assert.equal(instruction.DisableDynamicScaling,true);
  assert.deepEqual(instruction.TargetTrackingConfigurations,[]);
  assert.equal(newPlan({...input,dimension:'write'}).ScalingInstructions[0].ScalableDimension,dimensions.write);
  for (const patch of [{name:'a/b'},{table:'path/orders'},{dimension:'compute'},{tagValue:''},{minimum:'-1'},{maximum:'40001'},{minimum:'9'},{minimum:'1e2'}]) {
    assert.equal(newPlan({...input,...patch}),null);
  }
  assert.deepEqual(capacityBounds('1','40000'),{MinCapacity:1,MaxCapacity:40000});
});
test('DynamoDB readback uses its own JSON protocol and verifies both selected region and table', async () => {
  const table={TableName:'orders',TableArn:'arn:aws:dynamodb:us-west-2:000000000000:table/orders',ProvisionedThroughput:{ReadCapacityUnits:5,WriteCapacityUnits:9}};
  const api=scalingPlanClient(async (url, options) => {
    assert.equal(url,'/'); assert.equal(options.headers['Content-Type'],'application/x-amz-json-1.0');
    assert.equal(options.headers['X-Amz-Target'],'DynamoDB_20120810.DescribeTable');
    assert.equal(options.headers['X-Simulator-Region'],'us-west-2'); assert.deepEqual(JSON.parse(options.body),{TableName:'orders'});
    return Response.json({Table:table});
  },'us-west-2');
  assert.deepEqual(await api.capacity('orders',signal),table.ProvisionedThroughput);
  table.TableArn=table.TableArn.replace('us-west-2','us-east-1');
  await assert.rejects(api.capacity('orders',signal),/Unexpected native/);
});
test('update preserves stored policy and site-specific fields with unchanged target identity',async () => {
  const original={...instruction,DisableDynamicScaling:false,ScalingPolicyUpdateBehavior:'KeepExternalPolicies',
    TargetTrackingConfigurations:[{TargetValue:70,PredefinedScalingMetricSpecification:{PredefinedScalingMetricType:'DynamoDBReadCapacityUtilization'}}]};
  let sent;
  const api=scalingPlanClient(async (url,options) => {
    assert.equal(options.headers['Content-Type'],'application/x-amz-json-1.1');
    assert.equal(options.headers['X-Amz-Target'],'AnyScaleScalingPlannerFrontendService.UpdateScalingPlan');
    sent=JSON.parse(options.body);return Response.json({});
  },'us-east-1');
  await api.update({...existing,ScalingInstructions:[original]},'2','3',signal);
  assert.deepEqual(sent,{ScalingPlanName:'local',ScalingPlanVersion:1,ScalingInstructions:[{...original,MinCapacity:2,MaxCapacity:3}]});
  await assert.rejects(api.update({...existing,ScalingInstructions:[original,original]},'2','3',signal));
});
test('a canceled or cross-plan detail response cannot be published', async () => {
  let called=0;
  const controller=new AbortController();
  const api=scalingPlanClient(async () => {
    if (++called===2) {controller.abort();return Response.json({ScalingPlanResources:[]});}
    return Response.json({ScalingPlans:[existing]});
  },'us-east-1');
  await assert.rejects(api.detail('other',signal),/Unexpected native/);
  called=0;
  await assert.rejects(api.detail('local',controller.signal),{name:'AbortError'});
});
