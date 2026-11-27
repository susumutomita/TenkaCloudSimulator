'use strict';
// Opt-in, real Chromium + served app.js + disposable local simulator.
// No AWS endpoints, imported browser profile, transport fixtures, or guest.
const assert = require('node:assert/strict');
const {spawn} = require('node:child_process');
const {mkdtemp, mkdir, readFile, rm} = require('node:fs/promises');
const {tmpdir} = require('node:os');
const {join, resolve} = require('node:path');
const {setTimeout: sleep} = require('node:timers/promises');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE);

const ec2ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const screenshots = process.env.SCREENSHOTS_DIR && resolve(process.env.SCREENSHOTS_DIR);
const box = async locator => {
  const result = await locator.boundingBox();
  assert.ok(result, 'expected visible tile');
  return result;
};
async function until(find, label) {
  for (let i=0; i<100; i++) {
    const result=await find();
    if (result) return result;
    await sleep(80);
  }
  assert.fail(label);
}

async function run() {
  const dir = await mkdtemp(join(tmpdir(), 'tcsim-vpc-browser-'));
  const state = join(dir,'state.db'), tokenFile=join(dir,'operator-token');
  const simulator=spawn(resolve(process.env.SIMULATOR_BINARY),
    ['--listen','127.0.0.1:0','--state',state,'--token-file',tokenFile],
    {cwd:dir,env:{PATH:process.env.PATH,CI:'1'},stdio:['ignore','pipe','pipe']});
  const exit=new Promise((res,rej)=>{
    simulator.once('error',rej);
    simulator.once('exit',(code,signal)=>res({code,signal}));
  });
  let log='';
  for (const stream of [simulator.stdout,simulator.stderr]) stream.on('data',chunk=>{log=(log+chunk).slice(-2048);});
  let browser, context;
  try {
    const port=await until(()=>log.match(/http:\/\/127\.0\.0\.1:(\d+)/)?.[1],'local simulator never listened');
    const origin='http://127.0.0.1:'+port;
    const token=(await readFile(tokenFile,'utf8')).trim();
    browser=await chromium.launch({headless:true});
    context=await browser.newContext({baseURL:origin,viewport:{width:1440,height:1040}});
    const login=await context.request.post('/_simulator/login',{form:{token},headers:{Origin:origin},maxRedirects:0});
    assert.equal(login.status(),303);
    const page = await context.newPage();
    const calls=[], pageErrors=[];
    page.on('request',request=>{
      if (request.url().startsWith(origin+'/') && request.method()==='POST') {
        const action=new URLSearchParams(request.postData()??'').get('Action');
        if (action) calls.push(action);
      }
    });
    page.on('pageerror',error=>pageErrors.push(String(error)));
    const actionList=()=>calls.filter(action=>['CreateVpc','CreateSubnet','CreateSecurityGroup','CreateStack'].includes(action));
    const button=name=>page.getByRole('button',{name,exact:true});
    const field=name=>page.getByRole('textbox',{name,exact:true});
    const form=page.getByTestId('vpc-create-form');
    const create=()=>form.getByRole('button',{name:'Create VPC',exact:true});
    const only=page.locator('input[type=radio][value=vpc-only]');
    const more=page.locator('input[type=radio][value=vpc-subnet]');
    const vpcCIDR=field('IPv4 CIDR');
    const tileOnly=page.locator('[data-value=vpc-only]'),tileMore=page.locator('[data-value=vpc-subnet]');
    const readEC2=async (action,extra={})=>{
      const response=await context.request.post('/',{form:{Action:action,Version:'2016-11-15',...extra},
        headers:{Origin:origin,'X-Simulator-Service':'ec2','X-Simulator-Region':'us-east-1'}});
      assert.equal(response.status(),200,action);
      const body=await response.text();
      return page.evaluate(({body,ns})=>{
        const xml=new DOMParser().parseFromString(body,'application/xml');
        if(xml.documentElement.namespaceURI!==ns) throw Error('unexpected EC2 namespace');
        return [...xml.getElementsByTagNameNS(ns,'item')].map(el=>Object.fromEntries(
          [...el.children].filter(child=>!child.children.length).map(child=>[child.localName,child.textContent])));
      },{body,ns:ec2ns});
    };
    const stackQuery=async (action, name='')=>{
      const response=await context.request.post('/',{form:{Action:action,Version:'2010-05-15',...(name?{StackName:name}:{})},
        headers:{Origin:origin,'X-Simulator-Service':'cloudformation','X-Simulator-Region':'us-east-1'}});
      assert.equal(response.status(),200,action);
      return response.text();
    };

    await page.goto('/_simulator/console/#/vpc');
    await button('Create VPC').click();
    await form.waitFor();
    assert.match(page.url(),/view=create/);
    const links = page.getByRole('navigation',{name:'Breadcrumbs'});
    await links.getByRole('link',{name:'Your VPCs',exact:true}).waitFor();
    assert.equal(await only.isChecked(),true);
    assert.equal(await vpcCIDR.inputValue(),'10.0.0.0/16');
    assert.equal(await create().isEnabled(),true);
    assert.equal(await field('CloudFormation stack name').count(),0);
    assert.equal(await page.getByRole('radio',{name:/IPAM-allocated/}).isDisabled(),true);
    assert.equal(await page.getByRole('radio',{name:/Amazon-provided IPv6/}).isDisabled(),true);
    const [left,right]=await Promise.all([box(tileOnly),box(tileMore)]);
    assert.ok(right.x>left.x && Math.abs(right.y-left.y)<4,'expected two desktop columns');
    if (screenshots) {await mkdir(screenshots,{recursive:true});await page.screenshot({path:join(screenshots,'vpc-create-desktop.png'),fullPage:true});}
    await vpcCIDR.fill('');
    assert.equal(await create().isDisabled(),true);
    await form.getByText('Enter an IPv4 CIDR.',{exact:true}).waitFor();
    await vpcCIDR.fill('10.42.0.0/16');
    await only.focus();
    await only.press('ArrowRight');
    await until(()=>more.isChecked(),'native radio ArrowRight did not select VPC and more');
    await field('CloudFormation stack name').fill('private-network');
    await field('Subnet IPv4 CIDR').fill('10.90.1.0/24');
    assert.equal(await create().isDisabled(),true);
    await form.getByText('The subnet must fit inside the new VPC’s IPv4 CIDR.',{exact:true}).waitFor();
    assert.deepEqual(actionList(),[]);
    await page.goBack();
    await form.waitFor({state:'hidden'});
    await button('Create VPC').click();
    await links.getByRole('link',{name:'Your VPCs',exact:true}).click();
    await form.waitFor({state:'hidden'});
    await button('Create VPC').click();
    await button('Cancel').click();
    await form.waitFor({state:'hidden'});
    assert.doesNotMatch(page.url(),/view=create/);

    await button('Create VPC').click();
    await field('Name tag - optional').fill('browser-vpc-only');
    await vpcCIDR.fill('10.42.0.7/16');
    await form.getByText('browser-vpc-only',{exact:true}).waitFor();
    await create().click();
    await form.waitFor({state:'hidden'});
    await page.getByText(/You successfully created vpc-/).waitFor();
    const native=(await readEC2('DescribeVpcs')).find(row=>row.simulatorManagedBy==='ec2');
    assert.ok(native,'native VPC not in real HTTP inventory');
    assert.equal(native.cidrBlock,'10.42.0.0/16','server did not normalize host bits');
    assert.deepEqual(actionList(),['CreateVpc']);
    await button('Add subnet').click();
    const subnetDialog=page.getByRole('dialog',{name:'Add subnet'});
    await subnetDialog.getByRole('textbox',{name:'Subnet IPv4 CIDR'}).fill('10.42.1.0/24');
    await subnetDialog.getByRole('button',{name:'Create subnet',exact:true}).click();
    await subnetDialog.waitFor({state:'hidden'});
    await button('Create security group').click();
    const groupDialog=page.getByRole('dialog',{name:'Create security group'});
    await groupDialog.getByRole('textbox',{name:'Group name'}).fill('browser-group');
    await groupDialog.getByRole('textbox',{name:'Group description'}).fill('Private practice group');
    await groupDialog.getByRole('button',{name:'Create security group',exact:true}).click();
    await groupDialog.waitFor({state:'hidden'});
    const [subnets,groups]=await Promise.all([readEC2('DescribeSubnets'),readEC2('DescribeSecurityGroups')]);
    const savedSubnet=subnets.find(row=>row.vpcId===native.vpcId && row.cidrBlock==='10.42.1.0/24');
    const savedGroup=groups.find(row=>row.vpcId===native.vpcId && row.groupName==='browser-group');
    assert.ok(savedSubnet && savedGroup);
    assert.deepEqual(actionList(),['CreateVpc','CreateSubnet','CreateSecurityGroup']);

    await button('Create VPC').click();
    await tileMore.click();
    await field('CloudFormation stack name').fill('browser-stack-network');
    await vpcCIDR.fill('10.43.0.0/16');
    await field('Subnet IPv4 CIDR').fill('10.43.2.0/24');
    await create().click();
    await form.waitFor({state:'hidden'});
    await page.getByText(/CloudFormation stack and saved network verified/).waitFor();
    const vpcs=await readEC2('DescribeVpcs'), grouped=(await readEC2('DescribeSubnets'));
    const owned=vpcs.find(row=>row.simulatorManagedBy==='cloudformation');
    assert.ok(owned);
    assert.equal(owned.cidrBlock,'10.43.0.0/16');
    assert.ok(grouped.some(row=>row.vpcId===owned.vpcId && row.cidrBlock==='10.43.2.0/24'));
    assert.deepEqual(actionList(),['CreateVpc','CreateSubnet','CreateSecurityGroup','CreateStack']);
    assert.equal(vpcs.filter(row=>row.simulatorManagedBy).length,2);
    assert.equal((await readEC2('DescribeInstances')).filter(row=>row.instanceId).length,0,'the VPC wizard must not claim or create an instance');
    await stackQuery('DeleteStack','browser-stack-network');
    assert.ok(!(await readEC2('DescribeVpcs')).some(row=>row.vpcId===owned.vpcId));
    assert.ok((await readEC2('DescribeVpcs')).some(row=>row.vpcId===native.vpcId));
    await readEC2('DeleteSecurityGroup',{GroupId:savedGroup.groupId});
    await readEC2('DeleteSubnet',{SubnetId:savedSubnet.subnetId});
    await readEC2('DeleteVpc',{VpcId:native.vpcId});
    assert.ok(!(await readEC2('DescribeVpcs')).some(row=>row.vpcId));
    assert.ok(!(await readEC2('DescribeSubnets')).some(row=>row.subnetId));
    assert.ok(!(await readEC2('DescribeSecurityGroups')).some(row=>row.groupId));

    await page.setViewportSize({width:390,height:844});
    await page.evaluate(()=>localStorage.setItem('tenkacloud-simulator-locale','ja'));
    await page.goto('/_simulator/console/#/vpc?region=us-east-1&view=create');
    await page.reload();
    await form.waitFor();
    await form.getByRole('heading',{name:'VPC の設定'}).waitFor();
    await form.getByRole('heading',{name:'タグ'}).waitFor();
    await page.getByRole('link',{name:'お使いのVPC',exact:true}).waitFor();
    assert.equal(await only.isChecked(),true);
    await until(async()=>{const [top,bottom]=await Promise.all([box(tileOnly),box(tileMore)]);return bottom.y>top.y;},'mobile tiles must stack');
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'mobile viewport horizontally overflows');
    await tileMore.click();
    assert.ok(await field('サブネット IPv4 CIDR').isVisible());
    if (screenshots) await page.screenshot({path:join(screenshots,'vpc-create-mobile-ja.png'),fullPage:true});
    assert.deepEqual(pageErrors,[]);
    console.log('PASS Chromium: bilingual desktop/mobile create form, keyboard and navigation, actual native VPC/subnet/SG, separate private stack, refusal-before-write and cleanup. No guest launch.');
  } finally {
    await context?.close();
    await browser?.close();
    simulator.kill('SIGTERM');
    const watchdog=setTimeout(()=>simulator.kill('SIGKILL'),12000);
    try {assert.deepEqual(await exit,{code:0,signal:null});} finally {clearTimeout(watchdog);await rm(dir,{recursive:true,force:true});}
  }
}
run().catch(error=>{console.error(error);process.exitCode=1;});
