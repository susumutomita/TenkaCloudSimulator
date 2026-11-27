'use strict';
const assert=require('node:assert/strict');
const {spawn}=require('node:child_process');
const {mkdtemp,mkdir,readFile,rm}=require('node:fs/promises');
const {join,resolve}=require('node:path');
const {tmpdir}=require('node:os');
const {setTimeout:sleep}=require('node:timers/promises');
const {chromium}=require(process.env.PLAYWRIGHT_MODULE);
const extract=(xml,key)=>new RegExp('<'+key+'>([^<]+)</'+key+'>').exec(xml)?.[1];

// No browser/network product response is mocked. The two seed VPCs, native
// Subnet, native route and cleanup all hit the candidate’s local EC2 state.
async function run(){
  const work=await mkdtemp(join(tmpdir(),'tcsim-subnet-screen-'));
  const tokenPath=join(work,'token');
  const proc=spawn(resolve(process.env.SIMULATOR_BINARY),['--listen','127.0.0.1:0','--state',join(work,'state.db'),'--token-file',tokenPath],
    {cwd:work,env:{PATH:process.env.PATH,CI:'1'},stdio:['ignore','pipe','pipe']});
  const stopped=new Promise((res,rej)=>{proc.once('error',rej);proc.once('exit',(code,signal)=>res({code,signal}));});
  let log='',browser,context;
  for(const stream of [proc.stdout,proc.stderr])stream.on('data',chunk=>{log=(log+chunk).slice(-2048);});
  try{
    let port='';
    for(let i=0;!port&&i<100;i++){port=/http:\/\/127\.0\.0\.1:(\d+)/.exec(log)?.[1];if(!port)await sleep(80);}
    assert.ok(port,'candidate did not listen');
    const origin='http://127.0.0.1:'+port, token=(await readFile(tokenPath,'utf8')).trim();
    browser=await chromium.launch({headless:true});
    context=await browser.newContext({baseURL:origin,viewport:{width:1440,height:1000}});
    assert.equal((await context.request.post('/_simulator/login',{form:{token},headers:{Origin:origin},maxRedirects:0})).status(),303);
    const call=async(action,fields={},service='ec2')=>{
      const response=await context.request.post('/',{form:{Action:action,Version:service==='ec2'?'2016-11-15':'2010-05-15',...fields},
        headers:{Origin:origin,'X-Simulator-Service':service,'X-Simulator-Region':'us-east-1'}});
      const body=await response.text();
      assert.equal(response.status(),200,action+': '+body.slice(0,220));
      return body;
    };
    const nativeVpc=async(cidr,name)=>extract(await call('CreateVpc',{CidrBlock:cidr,'TagSpecification.1.ResourceType':'vpc',
      'TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name}),'vpcId');
    const a=await nativeVpc('10.80.0.0/16','Network alpha'),b=await nativeVpc('10.81.0.0/16','Network beta');
    assert.ok(a&&b&&a!==b);
    const template=JSON.stringify({Resources:{
      Vpc:{Type:'AWS::EC2::VPC',Properties:{CidrBlock:'10.82.0.0/16'}},
      Subnet:{Type:'AWS::EC2::Subnet',Properties:{VpcId:{Ref:'Vpc'},CidrBlock:'10.82.1.0/24',Tags:[{Key:'Name',Value:'Saved stack Name'}]}},
    }});
    await call('CreateStack',{StackName:'browser-stack-vpc',TemplateBody:template},'cloudformation');
    const stackID=extract(await call('DescribeSubnets'),'subnetId');
    assert.ok(stackID,'stack subnet was not saved');
    const page=await context.newPage();page.setDefaultTimeout(8000);
    const writes=[],errors=[];
    page.on('pageerror',error=>errors.push(String(error)));
    page.on('request',request=>{
      if(request.method()==='POST'&&request.url()===origin+'/'){
        const payload=new URLSearchParams(request.postData()??'');
        if(payload.get('Action')==='CreateSubnet')writes.push(Object.fromEntries(payload));
      }
    });
    const screenshots=process.env.SCREENSHOTS_DIR&&resolve(process.env.SCREENSHOTS_DIR);
    if(screenshots)await mkdir(screenshots,{recursive:true});
    await page.goto('/_simulator/console/#/vpc');
    await page.getByRole('navigation',{name:'Service navigation'}).getByRole('link',{name:'Subnets',exact:true}).click();
    await page.getByRole('heading',{name:'Subnets',level:1}).waitFor();
    await page.getByText('Saved stack Name',{exact:true}).waitFor();
    await page.getByRole('searchbox',{name:'Search subnets'}).fill('not-a-real-subnet');
    await page.getByText('No saved subnets match your filters.',{exact:true}).waitFor();
    await page.getByRole('searchbox',{name:'Search subnets'}).fill('Saved stack');
    await page.getByText('Saved stack Name',{exact:true}).waitFor();
    await page.getByRole('link',{name:'Create subnet',exact:true}).click();
    const form=page.getByTestId('subnet-create-form');
    await form.waitFor();
    const choose=async(label,option)=>{
      await form.getByRole('button',{name:label}).click();
      const item=page.getByRole('option',{name:option});
      await item.click();
    };
    const cidr=form.getByRole('textbox',{name:'IPv4 subnet CIDR block'});
    const create=form.getByRole('button',{name:'Create subnet',exact:true});
    assert.equal(await form.getByRole('textbox',{name:'Subnet name - optional'}).isDisabled(),true);
    assert.equal(await create.isDisabled(),true);
    await choose(/VPC ID/,new RegExp('Network beta'));
    await form.getByText('10.81.0.0/16',{exact:true}).waitFor();
    await cidr.fill('10.80.1.5/24');
    await form.getByText('Enter an IPv4 CIDR within this VPC.',{exact:true}).waitFor();
    assert.equal(await create.isDisabled(),true);
    assert.equal(writes.length,0);
    await choose(/VPC ID/,new RegExp('Network alpha'));
    await form.getByText('10.80.0.0/16',{exact:true}).waitFor();
    await choose(/Availability Zone/,/^us-east-1b$/);
    if(screenshots)await page.screenshot({path:join(screenshots,'subnet-create-desktop.png'),fullPage:true});
    await create.click();
    await form.waitFor({state:'hidden'});
    await page.getByRole('heading',{name:'Subnet details',exact:true}).waitFor();
    const actual=new URL(page.url()).hash.match(/\/subnets\/(subnet-[a-f0-9]+)/)?.[1];
    assert.ok(actual&&actual!==stackID);
    assert.deepEqual(writes,[{Action:'CreateSubnet',Version:'2016-11-15',VpcId:a,CidrBlock:'10.80.1.0/24',AvailabilityZone:'us-east-1b'}]);
    await page.getByText('Main route table (implicit)',{exact:true}).waitFor();
    const xml=await call('DescribeSubnets');
    assert.ok(xml.includes('<subnetId>'+actual+'</subnetId>')&&xml.includes('<cidrBlock>10.80.1.0/24</cidrBlock>'));
    await page.getByRole('link',{name:'Back to subnets',exact:true}).click();
    await page.getByRole('link',{name:actual,exact:true}).waitFor();
    assert.equal(await page.getByText('Saved stack Name',{exact:true}).count(),0,'scoped list filter leaked another VPC');
    await page.getByRole('link',{name:actual,exact:true}).click();
    const rtb=extract(await call('CreateRouteTable',{VpcId:a,ClientToken:'subnet-ui-browser-route'}),'routeTableId');
    assert.ok(rtb);
    const assoc=extract(await call('AssociateRouteTable',{RouteTableId:rtb,SubnetId:actual}),'associationId');
    assert.ok(assoc);
    await page.reload();
    await page.getByText('Explicit subnet association',{exact:true}).waitFor();
    await page.getByText(rtb,{exact:true}).waitFor();
    await call('DisassociateRouteTable',{AssociationId:assoc});
    await call('DeleteRouteTable',{RouteTableId:rtb});
    await call('DeleteSubnet',{SubnetId:actual});
    await call('DeleteVpc',{VpcId:a});await call('DeleteVpc',{VpcId:b});
    await call('DeleteStack',{StackName:'browser-stack-vpc'},'cloudformation');
    assert.equal(extract(await call('DescribeSubnets'),'subnetId'),undefined);
    await page.evaluate(()=>localStorage.setItem('tenkacloud-simulator-locale','ja'));
    await page.setViewportSize({width:390,height:844});
    await page.goto('/_simulator/console/#/vpc/subnets/create?region=us-east-1');
    await page.reload();
    await page.getByRole('heading',{name:'サブネットを作成',level:1}).waitFor();
    await page.getByText('EC2 管理の VPC がありません。先に VPC を作成してください。',{exact:true}).waitFor();
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
    if(screenshots)await page.screenshot({path:join(screenshots,'subnet-create-mobile-ja.png'),fullPage:true});
    await page.getByRole('link',{name:'キャンセル',exact:true}).click();
    await form.waitFor({state:'hidden'});
    assert.deepEqual(errors,[]);
    console.log('PASS Chromium+native loopback: regional Subnets/search/filter entry, form constraints, one saved subnet, native main/explicit route, cleanup, and Japanese mobile.');
  }finally{
    await context?.close();await browser?.close();
    proc.kill('SIGTERM');
    const timer=setTimeout(()=>proc.kill('SIGKILL'),10000);
    try{assert.deepEqual(await stopped,{code:0,signal:null});}finally{clearTimeout(timer);await rm(work,{recursive:true,force:true});}
  }
}
run().catch(error=>{console.error(error);process.exitCode=1;});
