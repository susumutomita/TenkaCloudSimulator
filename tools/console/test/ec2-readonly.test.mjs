import assert from 'node:assert/strict';
import test from 'node:test';
import {serviceAvailable,servicesForProvider,serviceFromHash,hashForService,vpcLifecycleAvailable,subnetLifecycleAvailable,instanceLifecycleAvailable} from '../src/console-navigation.mjs';
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
test('VPC create and delete controls need both advertised operations and no handoff scope',()=>{
 const lifecycle={services:{ec2:{console:true,operations:['DescribeInstances','DescribeVpcs','CreateVpc','DeleteVpc']}}};
 assert.equal(vpcLifecycleAvailable(lifecycle),true);
 assert.equal(serviceAvailable('vpc',lifecycle),true);
 for(const value of [caps,null,{},{services:{ec2:{operations:['CreateVpc']}}},{services:{ec2:{operations:'CreateVpc,DeleteVpc'}}},{...lifecycle,consoleScope:{instanceId:'i-1'}}])assert.equal(vpcLifecycleAvailable(value),false);
});
test('Subnets is a page of VPC: routed and gated by DescribeSubnets, never listed as a service',()=>{
 const listed={services:{ec2:{console:true,operations:['DescribeInstances','DescribeVpcs','DescribeSubnets']}}};
 assert.equal(serviceFromHash('#/subnets'),'subnets');assert.equal(hashForService('subnets'),'/subnets');
 assert.equal(serviceAvailable('subnets',listed),true);
 assert.equal(serviceAvailable('subnets',caps),false);
 assert.equal(serviceAvailable('subnets',{...listed,consoleScope:{instanceId:'i-1'}}),false);
 assert.deepEqual(servicesForProvider('aws',listed).map(s=>s.id),['ec2','vpc']);
 assert.deepEqual(servicesForProvider('aws',listed,'サブネット').map(s=>s.id),['vpc']);
 assert.deepEqual(servicesForProvider('aws',listed,'subnet').map(s=>s.id),['vpc']);
});
test('subnet and instance controls need every advertised operation and no handoff scope',()=>{
 const operations=['DescribeInstances','DescribeVpcs','DescribeSubnets','CreateVpc','DeleteVpc','CreateSubnet','DeleteSubnet','RunInstances','TerminateInstances'];
 const full={services:{ec2:{console:true,operations}}};
 assert.equal(subnetLifecycleAvailable(full),true);assert.equal(instanceLifecycleAvailable(full),true);
 for(const missing of ['DescribeSubnets','CreateSubnet','DeleteSubnet'])assert.equal(subnetLifecycleAvailable({services:{ec2:{console:true,operations:operations.filter(op=>op!==missing)}}}),false);
 for(const missing of ['DescribeVpcs','DescribeSubnets','RunInstances','TerminateInstances'])assert.equal(instanceLifecycleAvailable({services:{ec2:{console:true,operations:operations.filter(op=>op!==missing)}}}),false);
 for(const value of [caps,null,{},{services:{ec2:{operations:'RunInstances,TerminateInstances'}}},{...full,consoleScope:{instanceId:'i-1'}}]){
  assert.equal(subnetLifecycleAvailable(value),false);assert.equal(instanceLifecycleAvailable(value),false);
 }
 // A handoff bound to one instance still reads EC2, without launch controls.
 assert.equal(serviceAvailable('ec2',{...full,consoleScope:{instanceId:'i-1'}}),true);
});
