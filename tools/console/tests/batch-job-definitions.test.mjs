import test from 'node:test';
import assert from 'node:assert/strict';
import {batchDefinitionARN,batchDefinitionInput,batchDefinitionOperations,isBatchJobDefinition} from '../src/batch-job-definitions.mjs';

const service={model:'batch',signingName:'batch',protocol:'rest-json',account:'000000000000'};
const operation=(name,uri,members,fields={})=>({name,uri,method:'POST',members:members.map(([name,type])=>({name,type})),...fields});
const create=operation('RegisterJobDefinition','/v1/registerjobdefinition',
 [['jobDefinitionName','string'],['type','string'],['platformCapabilities','list'],['containerProperties','structure']]);
const describe=operation('DescribeJobDefinitions','/v1/describejobdefinitions',[['jobDefinitions','list']],
 {names:'jobDefinitions',items:'jobDefinitions'});
const update=operation('DeregisterJobDefinition','/v1/deregisterjobdefinition',[['jobDefinition','string']],{lookup:'jobDefinition'});
const resource={kind:'jobDefinition',arnWire:'jobDefinitionArn',operations:{create,describe,update}};
const arn='arn:aws:batch:us-east-1:000000000000:job-definition/unchanged_name:7';
const item={jobDefinitionArn:arn,jobDefinitionName:'unchanged_name',revision:7,status:'INACTIVE'};
const values={jobDefinitionName:'unchanged_name',image:'example.invalid/metadata:demo',vcpus:'2',memoryMiB:'1024'};

test('Batch-only console consumes these exact advertised native operations',()=>{
 assert.equal(isBatchJobDefinition(service,resource),true);
 assert.deepEqual(batchDefinitionOperations(service,resource),{create,describe,deregister:update});
 for(const [current,res] of [[{...service,model:'glue'},resource],[{...service,protocol:'json'},resource],
  [{...service,signingName:'other'},resource],[service,{...resource,kind:'schedulingPolicy'}]]){
  assert.equal(isBatchJobDefinition(current,res),false);
  assert.deepEqual(batchDefinitionOperations(current,res),{});
 }
 const ops=(change)=>batchDefinitionOperations(service,{...resource,operations:{...resource.operations,...change}});
 for(const corrupt of [undefined,{...update,method:'DELETE'},{...update,uri:'/v1/deletejobdefinition'},
  {...update,uri:'https://example.invalid/v1/deregisterjobdefinition'},{...update,lookup:'jobDefinitionName'},
  {...update,members:[{name:'jobDefinition',type:'string',location:'uri'}]}]){
  assert.deepEqual(ops({update:corrupt}),{create,describe,deregister:null});
 }
 assert.equal(ops({describe:{...describe,names:'jobDefinitionName'}}).describe,null);
 assert.equal(ops({create:{...create,members:[]}}).create,null);
});

test('exact saved regional revision ARNs allow inactive reading but never fall back to a name',()=>{
 assert.equal(batchDefinitionARN(service,resource,item,'us-east-1'),arn);
 for(const bad of [
  {...item,jobDefinitionArn:arn.replace(':7',':8')},{...item,jobDefinitionArn:'unchanged_name:7'},
  {...item,jobDefinitionArn:arn.replace('us-east-1','us-west-2')},{...item,jobDefinitionArn:arn.replace('000000000000','123456789012')},
  {...item,jobDefinitionArn:arn.replace('arn:aws:batch:','arn:aws:ecs:')},{...item,jobDefinitionName:'other'},
  {...item,revision:'7'},{...item,revision:0},{...item,revision:1.7},{...item,revision:2147483648},null,
 ])assert.equal(batchDefinitionARN(service,resource,bad,'us-east-1'),'');
 assert.equal(batchDefinitionARN(service,{...resource,arnWire:'name'},item,'us-east-1'),'');
 assert.equal(batchDefinitionARN(service,resource,item,undefined),'');
});

test('four values project to inert EC2 metadata without a command or other runtime fields',()=>{
 assert.deepEqual(batchDefinitionInput({...values,image:' '+values.image+' ',command:['unused'],tags:{unused:'no'}}),{
  invalid:[],input:{jobDefinitionName:values.jobDefinitionName,type:'container',platformCapabilities:['EC2'],
   containerProperties:{image:values.image,resourceRequirements:[{type:'VCPU',value:'2'},{type:'MEMORY',value:'1024'}]}}});
 assert.deepEqual(batchDefinitionInput({}),{invalid:['jobDefinitionName','image','vcpus','memoryMiB'],input:null});
 for(const [key,bad] of [
  ['jobDefinitionName','<not-a-name>'],['jobDefinitionName','a'.repeat(129)],
  ['image','https://example.invalid/metadata'],['image','example.invalid:21/metadata'],['image','image@sha256:abcd'],
  ['image','a'.repeat(256)],['image','<not-an-image>'],['vcpus','1e1'],['vcpus','01'],['vcpus','0'],
  ['vcpus','2147483648'],['memoryMiB','3'],['memoryMiB','4.0'],['memoryMiB','+4'],['memoryMiB','2147483648'],
 ])assert.deepEqual(batchDefinitionInput({...values,[key]:bad}),{invalid:[key],input:null});
 for(const [vcpus,memoryMiB] of [['1','4'],['2147483647','2147483647']])
  assert.ok(batchDefinitionInput({...values,vcpus,memoryMiB}).input);
});
