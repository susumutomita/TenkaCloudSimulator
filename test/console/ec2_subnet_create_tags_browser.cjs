'use strict';
const assert=require('node:assert/strict');
const {spawn}=require('node:child_process');
const {createHash}=require('node:crypto');
const {mkdtemp,readFile,rm}=require('node:fs/promises');
const {join,resolve}=require('node:path');
const {tmpdir}=require('node:os');
const {setTimeout:sleep}=require('node:timers/promises');
const {chromium}=require(process.env.PLAYWRIGHT_MODULE);

const region='us-east-1',root='/_simulator/console/',q='?region='+region;
const extract=(xml,element)=>new RegExp('<'+element+'>([^<]+)</'+element+'>').exec(xml)?.[1];
const name='Training 東京 & <local>', identity='subnet-create-without-tags', stack='tag-lab-stack';

// The initial fixtures and all observed writes use the real local API and the
// embedded bundle without changing, intercepting or injecting product responses.
async function run(){
  const dir=await mkdtemp(join(tmpdir(),'subnet-tag-ui-'));
  const tokenPath=join(dir,'test-token');
  const sim=spawn(resolve(process.env.SIMULATOR_BINARY),['--listen','127.0.0.1:0','--state',join(dir,'state.sqlite'),'--token-file',tokenPath],
    {cwd:dir,env:{PATH:process.env.PATH,CI:'1'},stdio:['ignore','pipe','pipe']});
  const exit=new Promise((done,fail)=>{sim.once('error',fail);sim.once('exit',(code,signal)=>done({code,signal}));});
  let log='',active='startup',browser,session;
  for(const stream of [sim.stdout,sim.stderr])stream.on('data',chunk=>{log=(log+chunk).slice(-2048);});
  try{
    let port='';
    for(let i=0;!port&&i<100;i++){port=/http:\/\/127\.0\.0\.1:(\d+)/.exec(log)?.[1];if(!port)await sleep(80);}
    assert.ok(port,'candidate did not listen');
    const origin='http://127.0.0.1:'+port, token=(await readFile(tokenPath,'utf8')).trim();
    browser=await chromium.launch({headless:true,args:['--disable-background-networking','--disk-cache-size=1']});
    session=await browser.newContext({baseURL:origin,viewport:{width:1380,height:950}});
    const anonymous=await session.request.get('/_simulator/v1/capabilities');
    assert.equal(anonymous.status(),401);
    assert.equal((await session.request.post('/_simulator/login',{form:{token},headers:{Origin:origin},maxRedirects:0})).status(),303);
    const capabilities=await session.request.get('/_simulator/v1/capabilities');
    assert.equal(capabilities.headers()['x-simulator-authentication'],'required');
    const ec2=(await capabilities.json()).services.ec2;
    assert.equal(ec2.nativeSubnetTagsOnCreate,true);
    assert.equal(ec2.operations.includes('CreateTags'),false,'generic tagging unexpectedly advertised');
    const bundle=await session.request.get(root+'app.js');
    assert.equal(bundle.status(),200);
    const hash=createHash('sha256').update(await bundle.body()).digest('hex');
    assert.equal(hash,require('../../tools/console/bundle-digests.json')['app.js']);
    const api=async (action,params={},service='ec2')=>{
      const reply=await session.request.post('/',{form:{Action:action,Version:service==='ec2'?'2016-11-15':'2010-05-15',...params},
        headers:{Origin:origin,'X-Simulator-Service':service,'X-Simulator-Region':region}});
      const xml=await reply.text();
      assert.equal(reply.status(),200,action+': '+xml.slice(0,200));
      return xml;
    };
    const vpc=extract(await api('CreateVpc',{CidrBlock:'10.95.0.0/16',
      'TagSpecification.1.ResourceType':'vpc','TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':'Tag workshop'}),'vpcId');
    assert.match(vpc,/^vpc-[0-9a-f]{17}$/);
    const template=JSON.stringify({Resources:{
      V:{Type:'AWS::EC2::VPC',Properties:{CidrBlock:'10.96.0.0/16',Tags:[{Key:'Name',Value:'Stack-owned fixture'}]}},
      S:{Type:'AWS::EC2::Subnet',Properties:{VpcId:{Ref:'V'},CidrBlock:'10.96.1.0/24',Tags:[{Key:'Name',Value:'Read-only stack subnet'}]}},
    }});
    await api('CreateStack',{StackName:stack,TemplateBody:template},'cloudformation');
    const existing=await api('DescribeSubnets');
    const stackSubnet=extract(existing,'subnetId');assert.ok(stackSubnet);
    const arn='arn:aws:ec2:'+region+':000000000000:';
    const policy={Version:'2012-10-17',Statement:[
      {Effect:'Allow',Action:['ec2:DescribeVpcs','ec2:DescribeSubnets','ec2:DescribeRouteTables'],Resource:'*'},
      {Effect:'Allow',Action:'ec2:CreateSubnet',Resource:[arn+'vpc/'+vpc,arn+'subnet/*']},
    ]};
    const setPolicy=await session.request.put('/_simulator/v1/iam/principals/'+identity+'/policy',
      {data:policy,headers:{Origin:origin,'Content-Type':'application/json'}});
    assert.ok(setPolicy.ok(),'test identity policy could not be configured');

    const page=await session.newPage();page.setDefaultTimeout(8000);
    const writes=[],faults=[];
    page.on('pageerror',error=>faults.push(String(error)));
    page.on('request',req=>{
      if(req.method()==='POST'&&req.url()===origin+'/'){
        const params=new URLSearchParams(req.postData()??'');
        if(params.get('Action')&&!params.get('Action').startsWith('Describe'))writes.push({
          principal:req.headers()['x-simulator-principal'],region:req.headers()['x-simulator-region'],params:Object.fromEntries(params)});
      }
    });
    const form=page.getByTestId('subnet-create-form');
    const btn=(text,scope=form)=>scope.getByRole('button',{name:text,exact:typeof text==='string'});
    const box=text=>form.getByRole('textbox',{name:text,exact:true});
    const label=box('Subnet name - optional'), cidr=box('IPv4 subnet CIDR block');
    const details=page.getByRole('heading',{name:'Subnet details',exact:true});
    async function selectNative(){
      await form.waitFor();
      await btn(/^VPC ID/).click();
      const off=page.getByRole('option',{name:/Stack-owned fixture/});
      await off.waitFor();assert.equal(await off.isDisabled(),true);
      await page.getByRole('option',{name:/Tag workshop/}).click();
      await form.getByText('10.95.0.0/16',{exact:true}).waitFor();
    }
    const currentSubnet=()=>/^#\/vpc\/subnets\/(subnet-[a-f0-9]+)\?/.exec(new URL(page.url()).hash)?.[1];
    const observedRows=()=>page.getByRole('table',{name:'Saved subnet tags',exact:true});
    const inventoryTable=page.getByRole('table',{name:'Subnets',exact:true});

    active='tag form';
    await page.goto(root+'#/vpc/subnets/create'+q);
    await form.waitFor();
    assert.equal(await label.isDisabled(),true);
    assert.equal(await btn('Add new tag').isDisabled(),true);
    await selectNative();
    assert.equal(await label.isEditable(),true);
    await label.fill(name);
    await cidr.fill('10.95.1.7/24');
    await btn('Add new tag').click();
    await box('Tag key 1').fill('Stage');
    await btn('Add new tag').click();
    await box('Tag key 2').fill('Stage');
    await form.getByText('Tag keys must be unique.',{exact:true}).first().waitFor();
    assert.equal(await btn('Create subnet').isDisabled(),true);
    assert.equal(writes.length,0);
    await box('Tag key 2').fill('Purpose');
    await box('Tag value 2').fill('local <practice>');
    await btn('Add new tag').click(); // untouched blank row must not make a wire member
    const complete=page.waitForResponse(response=>response.request().postData()?.includes('Action=CreateSubnet')&&response.status()===200);
    await btn('Create subnet').click();
    await complete;
    await details.waitFor();
    const saved=currentSubnet();assert.ok(saved);
    const savedTags=observedRows();
    await savedTags.getByRole('cell',{name:name,exact:true}).waitFor();
    await savedTags.getByRole('cell',{name:'(empty)',exact:true}).waitFor();
    await savedTags.getByRole('cell',{name:'local <practice>',exact:true}).waitFor();
    assert.equal(await savedTags.getByRole('row').count(),4,'header + three exact saved tags');
    await page.reload();
    await savedTags.getByRole('cell',{name:name,exact:true}).waitFor();
    await page.getByRole('link',{name:'Back to subnets',exact:true}).click();
    await inventoryTable.getByRole('cell',{name,exact:true}).waitFor();

    active='same features, no CreateTags IAM';
    await page.getByRole('button',{name:'Account menu',exact:true}).click();
    await page.getByText('Choose identity',{exact:true}).click();
    const choose=page.getByRole('dialog',{name:'Choose identity',exact:true});
    await choose.getByRole('textbox',{name:'Principal name',exact:true}).fill(identity);
    await btn('Choose identity',choose).click();
    await choose.waitFor({state:'hidden'});
    await page.getByRole('link',{name:'Create subnet',exact:true}).click();
    await form.waitFor();
    if(await label.isDisabled())await selectNative();
    await label.fill('Should not be saved');
    await cidr.fill('10.95.2.0/24');
    const refused=page.waitForResponse(response=>response.request().postData()?.includes('Action=CreateSubnet')&&response.status()===403);
    await btn('Create subnet').click();
    await refused;
    await form.getByText('The current identity is not allowed to perform this subnet operation.',{exact:true}).waitFor();
    assert.equal(writes.length,2,'the denied tagged create was not resent without tags');
    let inventory=await api('DescribeSubnets');
    assert.ok(inventory.includes('<subnetId>'+saved+'</subnetId>'));
    assert.ok(!inventory.includes('10.95.2.0/24')&&!inventory.includes('Should not be saved'));
    await label.fill('');
    await btn('Create subnet').click();
    await details.waitFor();
    const untagged=currentSubnet();assert.ok(untagged&&untagged!==saved);
    assert.equal(await observedRows().count(),0);
    assert.deepEqual(writes.map(call=>[call.principal,call.region,call.params.Action]),[
      ['simulator-admin',region,'CreateSubnet'],[identity,region,'CreateSubnet'],[identity,region,'CreateSubnet'],
    ]);
    assert.deepEqual(writes[0].params,{
      Action:'CreateSubnet',Version:'2016-11-15',VpcId:vpc,CidrBlock:'10.95.1.0/24',AvailabilityZone:region+'a',
      'TagSpecification.1.ResourceType':'subnet','TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name,
      'TagSpecification.1.Tag.2.Key':'Stage','TagSpecification.1.Tag.2.Value':'',
      'TagSpecification.1.Tag.3.Key':'Purpose','TagSpecification.1.Tag.3.Value':'local <practice>',
    });
    assert.equal(writes[1].params['TagSpecification.1.Tag.1.Value'],'Should not be saved');
    assert.deepEqual(writes[2].params,{
      Action:'CreateSubnet',Version:'2016-11-15',VpcId:vpc,CidrBlock:'10.95.2.0/24',AvailabilityZone:region+'a',
    });

    active='cleanup';
    for(const id of [untagged,saved])await api('DeleteSubnet',{SubnetId:id});
    inventory=await api('DescribeSubnets');
    assert.equal(inventory.replace(/<requestId>[^<]+<\/requestId>/,''),
      existing.replace(/<requestId>[^<]+<\/requestId>/,''),'stack-owned subnet returned unchanged');
    await api('DeleteVpc',{VpcId:vpc});await api('DeleteStack',{StackName:stack},'cloudformation');
    assert.equal(extract(await api('DescribeSubnets'),'subnetId'),undefined);
    assert.deepEqual(faults,[]);
    console.log(JSON.stringify({passed:'Chromium + real native CreateSubnet, readback and IAM',sha:hash,
      tagged:1,denied:1,untagged:1,stackPreserved:true,cleanup:true,unexpectedPageErrors:0}));
  }catch(error){error.message=active+': '+error.message;throw error;}
  finally{
    await session?.close();await browser?.close();sim.kill('SIGTERM');
    const timer=setTimeout(()=>sim.kill('SIGKILL'),10000);
    try{assert.deepEqual(await exit,{code:0,signal:null});}finally{clearTimeout(timer);await rm(dir,{recursive:true,force:true});}
  }
}
run().catch(error=>{console.error(error);process.exitCode=1;});
