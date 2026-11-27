import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {createVpcWithSubnet, vpcCreateIssues, vpcSubnetAvailable, vpcSubnetParameters} from '../src/ec2-vpc-subnet.mjs';

const require = createRequire(import.meta.url);
const consoleDir = fileURLToPath(new URL('..', import.meta.url));
const {outputFiles} = await build({
  absWorkingDir: consoleDir, stdin: {contents: "export {default} from './src/ec2-vpc-create.jsx';", resolveDir: consoleDir},
  bundle: true, write: false, platform: 'node', format: 'cjs', loader: {'.css': 'empty'},
  external: ['react', 'react-dom', 'react/jsx-runtime'], define: {'process.env.NODE_ENV': '"production"'},
});
const module = {exports:{}};
Function('require', 'module', 'exports', outputFiles[0].text)(require, module, module.exports);
const {createElement} = require('react'), {renderToStaticMarkup} = require('react-dom/server');
const form = {mode:'vpc-subnet', stackName:'sandbox-network', name:'VPC "<&>', cidr:'10.20.0.0/16', subnetCIDR:'10.20.1.0/24'};
const region = 'us-east-1';
const id = 'arn:aws:cloudformation:' + region + ':000000000000:stack/' + form.stackName + '/' + 'c'.repeat(32);
const vpcId = 'vpc-' + 'a'.repeat(17), subnetId = 'subnet-' + 'b'.repeat(17);
const xmlns = 'http://cloudformation.amazonaws.com/doc/2010-05-15/';
const xml = (localName, content) => ({localName, namespaceURI:xmlns, children:typeof content === 'string' ? [] : content, textContent:content});
const doc = (action, fields) => ({documentElement:xml(action+'Response',[xml(action+'Result',fields)])});
const create = doc('CreateStack',[xml('StackId',id)]);
const stack = ({stackId=id,name=form.stackName,status='CREATE_COMPLETE',vpc=vpcId,subnet=subnetId}={}) =>
  doc('DescribeStacks',[xml('Stacks',[xml('member',[
    xml('StackId',stackId),xml('StackName',name),xml('StackStatus',status),xml('Outputs',[
      xml('member',[xml('OutputKey','VpcId'),xml('OutputValue',vpc)]),
      xml('member',[xml('OutputKey','SubnetId'),xml('OutputValue',subnet)]),
    ]),
  ])])]);

test('actual React VPC settings show editable two-resource fields only when opted in and supported', () => {
  const render = (enabled, values, disabled=false) => renderToStaticMarkup(createElement(module.exports.default,
    {pairedAvailable:enabled,values,busy:disabled,change(){}}));
  const only = render(true,{...form,mode:'vpc-only'});
  assert.match(only,/VPC only/); assert.match(only,/VPC and more/);
  assert.match(only,/role="radiogroup"/);
  assert.match(only,/value="vpc-only"[^>]*checked|checked[^>]*value="vpc-only"/);
  const headings=['VPC settings', 'Resources to create', 'Name tag - optional', 'IPv4 CIDR block', 'IPv6 CIDR block', 'Tenancy', 'Tags'];
  const positions=headings.map(heading=>only.indexOf(heading));
  assert.ok(positions.every((pos,i)=>pos>=0 && (i===0 || pos>positions[i-1])));
  assert.doesNotMatch(only,/Subnet IPv4 CIDR|CloudFormation stack name/);
  const legacy = render(false,form);
  assert.doesNotMatch(legacy,/VPC and more|Subnet IPv4 CIDR|CloudFormation stack name/);
  const draft = render(true,form);
  assert.match(draft,/only a VPC and one subnet/); assert.match(draft,/Subnet IPv4 CIDR/); assert.match(draft,/CloudFormation stack name/);
  assert.match(draft,/value="10\.20\.1\.0\/24"/); assert.match(draft,/value="sandbox-network"/);
  assert.match(render(true,form,true),/disabled=""/);
});

test('native form has no stack inputs; draft problems block invalid requests and explain the exact field', () => {
  assert.deepEqual(vpcCreateIssues({cidr:'10.0.0.0/16'},false),{});
  assert.deepEqual(vpcCreateIssues({cidr:'10.0.0.0/15',name:'界'.repeat(86)},false),{cidr:'cidr',name:'name'});
  assert.deepEqual(vpcCreateIssues({cidr:'10.0.0.0/16',stackName:'',subnetCIDR:'10.1.0.0/24'},true),
    {stack:'stack',subnet:'containment'});
  assert.deepEqual(vpcCreateIssues({cidr:'10.0.0.0/16',stackName:'valid',subnetCIDR:'not a subnet'},true),{subnet:'subnet'});
  assert.deepEqual(vpcCreateIssues({cidr:'10.0.0.7/16',stackName:'valid',subnetCIDR:'10.0.1.12/24',name:'界'.repeat(85)},true),{});
});

test('only a verified operator using the administrator identity can submit the paired workflow', async () => {
  const cfn = {operations:['CreateStack','DescribeStacks']};
  const caps = {identitySelection:true,services:{cloudformation:cfn}};
  assert.equal(vpcSubnetAvailable(caps,true,'simulator-admin'),true);
  const denied = [
    [{...caps,identitySelection:false},true,'learner'],
    [{...caps,identitySelection:false},true,'simulator-admin'],
    [caps,false,'simulator-admin'],
    [caps,true,'switched-exercise'],
    [{...caps,consoleScope:{instanceId:'i-example'}},true,'simulator-admin'],
    [{...caps,services:{cloudformation:true}},true,'simulator-admin'],
    [{...caps,services:{cloudformation:{operations:['CreateStack']}}},true,'simulator-admin'],
  ];
  for (const input of denied) {
    const allowed=vpcSubnetAvailable(...input);
    assert.equal(allowed,false);
    const html=renderToStaticMarkup(createElement(module.exports.default,{pairedAvailable:allowed,values:form,change(){}}));
    assert.doesNotMatch(html,/VPC and more|Subnet IPv4 CIDR|CloudFormation stack name/);
    await assert.rejects(createVpcWithSubnet(async()=>assert.fail('No participant CreateStack call'),
      region,form,{allowed}),/authenticated operator/);
  }
});

test('the template and route create exactly two resources and never provide credentials or an existing ID', () => {
  const query = vpcSubnetParameters({...form,roleArn:'ignored',vpcId:'ignored',templateBody:'ignored'});
  assert.deepEqual([...query.keys()],['Action','Version','StackName','TemplateBody']);
  const template = JSON.parse(query.get('TemplateBody'));
  assert.deepEqual(template.Resources,{
    Vpc:{Type:'AWS::EC2::VPC',Properties:{CidrBlock:form.cidr,Tags:[{Key:'Name',Value:form.name}]}},
    Subnet:{Type:'AWS::EC2::Subnet',Properties:{VpcId:{Ref:'Vpc'},CidrBlock:form.subnetCIDR}},
  });
  assert.deepEqual(template.Outputs,{VpcId:{Value:{Ref:'Vpc'}},SubnetId:{Value:{Ref:'Subnet'}}});
  for (const name of [undefined,'','1invalid','bad/escape','a'.repeat(129)])
    assert.throws(()=>vpcSubnetParameters({...form,stackName:name}),/: stack/);
  assert.throws(()=>vpcSubnetParameters({...form,subnetCIDR:''}),/: cidr/);
  for (const cidr of ['10.20.0.0/29','10.20.256.0/24','10.020.2.0/24','::/24'])
    assert.throws(()=>vpcSubnetParameters({...form,subnetCIDR:cidr}),/: cidr/);
  assert.throws(()=>vpcSubnetParameters({...form,subnetCIDR:'10.21.0.0/24'}),/: containment/);
  assert.throws(()=>vpcSubnetParameters({...form,subnetCIDR:'10.20.0.0/16',cidr:'10.20.0.0/24'}),/: containment/);
  assert.ok(vpcSubnetParameters({...form,cidr:'192.168.0.0/16',subnetCIDR:'192.168.1.1/24'}));

});

test('uses one authenticated regional CloudFormation POST and exact ARN readback before success', async () => {
  const calls=[], signal = new AbortController().signal;
  const transport = async (path, opts) => {
    calls.push([path, opts]); return {ok:true,text:async()=>calls.length === 1 ? create : stack()};
  };
  assert.deepEqual(await createVpcWithSubnet(transport,region,form,{allowed:true,signal,parse:x=>x}),{stackId:id,vpcId,subnetId});
  assert.deepEqual(calls.map(([,opts])=>opts.body.get('Action')),['CreateStack','DescribeStacks']);
  assert.equal(calls[1][1].body.get('StackName'),id);
  for (const [path, opts] of calls) {
    assert.equal(path,'/'); assert.equal(opts.signal,signal); assert.equal(opts.redirect,'error');
    assert.deepEqual(opts.headers,{'Content-Type':'application/x-www-form-urlencoded',
      'X-Simulator-Service':'cloudformation','X-Simulator-Region':region});
  }
});

test('a refusal, wrong stack, unconfirmed output or abort is not reported as success and does not retry', async () => {
  for (const result of [stack({name:'another'}),stack({status:'CREATE_IN_PROGRESS'}),stack({subnet:vpcId}),stack({stackId:id+'a'})]) {
    let count=0;
    await assert.rejects(createVpcWithSubnet(async()=>({ok:true,text:async()=>++count===1?create:result}),region,form,{allowed:true,parse:x=>x}),/did not confirm/);
    assert.equal(count,2);
  }
  let count=0;
  await assert.rejects(createVpcWithSubnet(async()=>{count++;return {ok:false,status:403};},region,form,{allowed:true}),/403/);
  assert.equal(count,1);
  const controller=new AbortController();
  await assert.rejects(createVpcWithSubnet(async()=>({ok:true,text:async()=>{controller.abort();return create;}}),region,form,{allowed:true,signal:controller.signal,parse:x=>x}),
    {name:'AbortError'});
});
