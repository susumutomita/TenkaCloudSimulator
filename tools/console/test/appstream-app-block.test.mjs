import assert from 'node:assert/strict';
import test from 'node:test';
import {appStreamAppBlockInput,appStreamAppBlockDetailInput,isAppStreamAppBlock} from '../src/appstream-app-block.mjs';

const base={name:'fixture-block',packaging:'APPSTREAM2',importMode:'new',source:'s3://example-bucket'};
const script={uri:'s3://example-bucket/assets/setup script.ps1',executable:'PowerShell.exe',args:'-File startup.ps1',timeout:'30'};

test('workspaces-app packaging can record a destination bucket without builder/setup',()=>{
 assert.deepEqual(appStreamAppBlockInput(base),{errors:{},input:{Name:'fixture-block',PackagingType:'APPSTREAM2',SourceS3Location:{S3Bucket:'example-bucket'}}});
 assert.equal(appStreamAppBlockInput({...base,source:'s3://example-bucket/object'}).input,null);
});

test('existing package preserves S3 object key and only the requested post script',()=>{
 const delivered=appStreamAppBlockInput({...base,importMode:'existing',source:'s3://example-bucket/assets/test.vhd',
  withPostSetup:true,postSetup:script,setup:{...script,executable:'unused'},tags:'Stage=test\nTeam=sim'});
 assert.deepEqual(delivered,{errors:{},input:{
  Name:'fixture-block',PackagingType:'APPSTREAM2',SourceS3Location:{S3Bucket:'example-bucket',S3Key:'assets/test.vhd'},
  PostSetupScriptDetails:{ScriptS3Location:{S3Bucket:'example-bucket',S3Key:'assets/setup script.ps1'},ExecutablePath:'PowerShell.exe',ExecutableParameters:'-File startup.ps1',TimeoutInSeconds:30},
  Tags:{Stage:'test',Team:'sim'},
 }});
 assert.equal(appStreamAppBlockInput({...base,importMode:'existing'}).input,null);
});

test('custom packaging needs a VHD object and setup script and never emits a stale post script',()=>{
 const custom={...base,packaging:'CUSTOM',source:'s3://example-bucket/test.vhd',setup:script,withPostSetup:true,postSetup:script};
 const {input}=appStreamAppBlockInput(custom);
 assert.ok(input.SetupScriptDetails);
 assert.equal(input.PostSetupScriptDetails,undefined);
 assert.equal(appStreamAppBlockInput({...custom,source:'s3://example-bucket'}).input,null);
 assert.equal(appStreamAppBlockInput({...custom,setup:undefined}).input,null);
 assert.equal(appStreamAppBlockInput({...custom,setup:{...script,timeout:'0'}}).input,null);
});

test('invalid form never yields a payload or a fetchable URL',()=>{
 for(const source of ['https://example.test/bucket','file:///tmp/fake','s3://','s3://UPPER'])assert.equal(appStreamAppBlockInput({...base,source}).input,null);
 assert.equal(appStreamAppBlockInput({...base,name:'a b',tags:'Team=x\nTeam=y'}).input,null);
 assert.equal(appStreamAppBlockInput({...base,packaging:'unknown'}).input,null);
});

test('only the AppStream app-block view reads a current row by exact ARN',()=>{
 const op={name:'DescribeAppBlocks',names:'Arns'};
 const resource={kind:'appBlock',operations:{list:op}};
 const arn='arn:aws:appstream:us-east-1:000000000000:app-block/fixture-block';
 assert.equal(isAppStreamAppBlock({model:'appstream'},resource),true);
 assert.equal(isAppStreamAppBlock({model:'appstream'},{kind:'stack'}),false);
 assert.equal(isAppStreamAppBlock({model:'workspaces'},resource),false);
 assert.deepEqual(appStreamAppBlockDetailInput(resource,{Name:'fixture-block',Arn:arn}),{Arns:[arn]});
 assert.equal(appStreamAppBlockDetailInput(resource,{Name:'fixture-block'}),null);
});
