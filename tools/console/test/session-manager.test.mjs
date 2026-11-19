import assert from 'node:assert/strict';
import test from 'node:test';
import {readFileSync} from 'node:fs';
import {serviceAvailable,servicesForProvider,serviceFromHash,hashForService} from '../src/console-navigation.mjs';
import {canStart,connectionState,validReadiness} from '../src/session-manager-state.mjs';
const services={ec2:{console:true,operations:['DescribeInstances','DescribeVpcs']},ssm:{operations:[]},s3:{},lambda:{}};
const scope={instanceId:'i-12345678',region:'us-east-1'};
const instanceCaps={services,consoleScope:scope};
test('an instance console session lists only Console Home, EC2 and Session Manager',()=>{
 const ids=['home','ec2','vpc','s3','ssm','ssm-session','lambda','iam'].filter(id=>serviceAvailable(id,instanceCaps));
 assert.deepEqual(ids,['home','ec2','ssm-session']);
 assert.deepEqual(servicesForProvider('aws',instanceCaps).map(s=>s.id).sort(),['ec2','ssm-session']);
 assert.equal(serviceFromHash('#/session-manager'),'ssm-session');assert.equal(hashForService('ssm-session'),'/session-manager');
});
test('a parameter console session and an unscoped session never show Session Manager',()=>{
 assert.equal(serviceAvailable('ssm-session',{services,consoleScope:{parameterName:'/p',region:'us-east-1'}}),false);
 assert.equal(serviceAvailable('ssm-session',{services}),false);
 assert.equal(serviceAvailable('ssm',{services,consoleScope:{parameterName:'/p',region:'us-east-1'}}),true);
 assert.equal(serviceAvailable('ec2',{services,consoleScope:{parameterName:'/p',region:'us-east-1'}}),false);
 assert.equal(serviceAvailable('ssm-session',{services:{ec2:services.ec2},consoleScope:scope}),false);
});
test('Start is enabled only after the server reports the relay connectable',()=>{
 assert.equal(connectionState(null),'checking');assert.equal(connectionState('unavailable'),'unavailable');
 assert.equal(connectionState({Connectable:false}),'preparing');assert.equal(connectionState({Connectable:true}),'ready');
 assert.equal(connectionState({Connectable:'yes'}),'unavailable');
 for(const state of ['checking','preparing','unavailable'])assert.equal(canStart(state,'idle'),false);
 assert.equal(canStart('ready','idle'),true);assert.equal(canStart('ready','open'),false);assert.equal(canStart('ready','disconnected'),false);
});
test('readiness for another instance or region is ignored',()=>{
 assert.equal(validReadiness({Connectable:true,Target:'i-12345678',Region:'us-east-1'},scope),true);
 assert.equal(validReadiness({Connectable:true,Target:'i-87654321',Region:'us-east-1'},scope),false);
 assert.equal(validReadiness({Connectable:true,Target:'i-12345678',Region:'us-west-2'},scope),false);
 assert.equal(validReadiness({Connectable:1},scope),false);
});
test('the page uses the existing terminal client and keeps output inert and reconnect explicit',()=>{
 const source=readFileSync(new URL('../src/session-manager.jsx',import.meta.url),'utf8');
 assert.match(source,/ssm-terminal-client\.mjs/);assert.match(source,/connection\.resume\(\)/);
 assert.doesNotMatch(source,/dangerouslySetInnerHTML|innerHTML/);
 assert.match(source,/disabled=\{!canStart\(state,phase\)/);
});
