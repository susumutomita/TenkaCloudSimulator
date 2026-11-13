import assert from 'node:assert/strict';
import test from 'node:test';
import {serviceAvailable,servicesForProvider,serviceFromHash} from '../src/console-navigation.mjs';
const caps={services:{ec2:{console:true,operations:['DescribeInstances','DescribeVpcs']}}};
test('EC2 and VPC require the exact readonly capability, never mere truthiness',()=>{
 for(const id of ['ec2','vpc']){
  assert.equal(serviceAvailable(id,caps),true);
  for(const value of [null,true,{}, {console:true}, {console:true,operations:['DescribeInstances']}])assert.equal(serviceAvailable(id,{services:{ec2:value}}),false);
  assert.equal(serviceAvailable(id,{...caps,consoleScope:{parameterName:'bound'}}),false);
 }
});
test('discover and deep-link the independent EC2 and VPC views',()=>{
 assert.equal(serviceFromHash('#/ec2'),'ec2');assert.equal(serviceFromHash('#/vpc'),'vpc');
 assert.deepEqual(servicesForProvider('aws',caps,'インスタンス').map(s=>s.id),['ec2']);
 assert.deepEqual(servicesForProvider('aws',caps,'ネットワーク').map(s=>s.id),['vpc']);
});
