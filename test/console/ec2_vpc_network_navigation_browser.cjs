'use strict';
const assert=require('node:assert/strict');
const {spawn}=require('node:child_process');
const {createHash}=require('node:crypto');
const {mkdtemp,mkdir,readFile,rm}=require('node:fs/promises');
const {join,resolve}=require('node:path');
const {tmpdir}=require('node:os');
const {setTimeout:sleep}=require('node:timers/promises');
const {chromium}=require(process.env.PLAYWRIGHT_MODULE);

const region='ap-northeast-1',root='/_simulator/console/',q='?region='+region;
const extract=(xml,key)=>new RegExp('<'+key+'>([^<]+)</'+key+'>').exec(xml)?.[1];
const networkUI=action=>!/^Describe/.test(action);
const egress='10.91.1.0/24';

// Exercise the exact committed assets served by a fresh local simulator.
async function run(){
  const work=await mkdtemp(join(tmpdir(),'tcsim-network-navigation-'));
  const tokenPath=join(work,'test-token');
  const proc=spawn(resolve(process.env.SIMULATOR_BINARY),['--listen','127.0.0.1:0','--state',join(work,'state.db'),'--token-file',tokenPath],
    {cwd:work,env:{PATH:process.env.PATH,CI:'1'},stdio:['ignore','pipe','pipe']});
  const stopped=new Promise((res,rej)=>{proc.once('error',rej);proc.once('exit',(code,signal)=>res({code,signal}));});
  let log='',browser,context,activeCase='start';
  for(const stream of [proc.stdout,proc.stderr])stream.on('data',chunk=>{log=(log+chunk).slice(-2048);});
  try{
    let port='';
    for(let i=0;!port&&i<100;i++){port=/http:\/\/127\.0\.0\.1:(\d+)/.exec(log)?.[1];if(!port)await sleep(80);}
    assert.ok(port,'candidate did not listen');
    const origin='http://127.0.0.1:'+port,token=(await readFile(tokenPath,'utf8')).trim();
    browser=await chromium.launch({headless:true,args:['--disable-background-networking','--disk-cache-size=1']});
    context=await browser.newContext({baseURL:origin,viewport:{width:1440,height:1000}});
    assert.equal((await context.request.post('/_simulator/login',{form:{token},headers:{Origin:origin},maxRedirects:0})).status(),303);
    const asset=await context.request.get(root+'app.js');
    assert.equal(asset.status(),200);
    const sha=createHash('sha256').update(await asset.body()).digest('hex');
    const expected=require('../../tools/console/bundle-digests.json');
    assert.equal(sha,expected['app.js']);
    const ec2=async(action,fields={},r=region)=>{
      const response=await context.request.post('/',{form:{Action:action,Version:'2016-11-15',...fields},
        headers:{Origin:origin,'X-Simulator-Service':'ec2','X-Simulator-Region':r}});
      const body=await response.text();
      assert.equal(response.status(),200,action+': '+body.slice(0,240));
      return body;
    };
    const nativeVpc=async(cidr,name)=>extract(await ec2('CreateVpc',{CidrBlock:cidr,'TagSpecification.1.ResourceType':'vpc',
      'TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name}),'vpcId');
    const vpc=await nativeVpc('10.91.0.0/16','Navigation alpha');
    const foreign=await nativeVpc('10.92.0.0/16','Navigation beta');
    const subnet=extract(await ec2('CreateSubnet',{VpcId:vpc,CidrBlock:egress,AvailabilityZone:region+'a'}),'subnetId');
    assert.ok(vpc&&foreign&&subnet);
    const page=await context.newPage();page.setDefaultTimeout(8000);
    const faults=[],calls=[];
    page.on('pageerror',error=>faults.push(String(error)));
    page.on('request',req=>{
      if(req.method()==='POST'&&req.url()===origin+'/'){
        const params=new URLSearchParams(req.postData()??'');
        if(params.has('Action')) calls.push({action:params.get('Action'),region:req.headers()['x-simulator-region'],params});
      }
    });
    const hash=()=>new URL(page.url()).hash;
    const waitRoute=path=>page.waitForURL(url=>url.hash.startsWith(path));
    const navigation=()=>page.getByRole('navigation',{name:'Service navigation'});
    const nav=name=>navigation().getByRole('link',{name,exact:true});
    const go=async(name,title=name)=>{await nav(name).click();await page.getByRole('heading',{name:title,level:1,exact:true}).waitFor();};
    const btn=(n,p=page)=>p.getByRole('button',{name:n,exact:typeof n==='string'});
    const text=(str,p=page)=>p.getByText(str,{exact:true});
    const link=(n,p=page)=>p.getByRole('link',{name:n,exact:true});
    const dlg=n=>page.getByRole('dialog',{name:n,exact:true});
    const tab=n=>page.getByRole('tab',{name:n,exact:true});
    const picker=async(label,choice,widget=page)=>{
      await btn(label,widget).click();
      await page.getByRole('option',{name:choice}).click();
    };
    const named=async (action,xmlField,name,lookup={})=>{
      let arr=[];
      for(let i=0;i<30;i++){
        const data=await ec2(action,lookup);
        arr=data.split('</item>').filter(row=>row.includes('>'+name+'<')&&extract(row,xmlField));
        if(arr.length===1)return extract(arr[0],xmlField);
        await sleep(35);
      }
      assert.fail('No unique saved '+xmlField+' for '+name);
    };
    const shot=async name=>{
      const dir=process.env.SCREENSHOTS_DIR;
      if(dir){await mkdir(dir,{recursive:true});await page.screenshot({path:join(dir,name+'.png'),fullPage:true});}
    };
    await page.goto(root+'#/vpc'+q);
    for(const name of ['VPCs','Subnets','Route tables','Internet gateways','Network ACLs','Security groups'])await nav(name).waitFor();

    activeCase='internet gateways';
    await go('Internet gateways');
    await btn('Create internet gateway').click();
    const createGateway=dlg('Create internet gateway');
    await createGateway.getByRole('textbox',{name:'Name (optional)',exact:true}).fill('navigation-gateway');
    await btn('Create',createGateway).click();
    await text('navigation-gateway').waitFor();
    const gateway=await named('DescribeInternetGateways','internetGatewayId','navigation-gateway');
    await btn(gateway).click();
    await waitRoute('#/vpc/internet-gateways/'+gateway);
    await btn('Attach to VPC').click();
    const attach=dlg('Attach to VPC');
    await picker(/EC2-managed VPC/,/Navigation alpha/,attach);
    await btn('Attach',attach).click();
    await page.getByText('Saved the VPC attachment:',{exact:false}).waitFor();
    assert.ok((await ec2('DescribeInternetGateways',{'InternetGatewayId.1':gateway})).includes('<vpcId>'+vpc+'</vpcId>'));
    const attachment=btn(new RegExp(vpc));
    await attachment.click();
    await waitRoute('#/vpc/'+vpc);
    await page.getByRole('heading',{name:'Resource details',exact:true}).waitFor();
    await page.goBack();
    await waitRoute('#/vpc/internet-gateways/'+gateway);
    await page.getByRole('heading',{name:'Gateway details',exact:true}).waitFor();
    await page.goForward();
    await waitRoute('#/vpc/'+vpc);

    activeCase='route tables and subnet back links';
    await go('Route tables');
    await btn('Create route table').click();
    const createTable=dlg('Create route table');
    await picker(/^VPC/,new RegExp(vpc),createTable);
    await createTable.getByRole('textbox',{name:'Name (optional)',exact:true}).fill('navigation-route');
    await btn('Create route table',createTable).click();
    await page.waitForURL(/\/route-tables\/rtb-/);
    const table=hash().split('/').at(-1).split('?')[0];
    await link(table,page.getByRole('table',{name:'Route tables',exact:true})).waitFor();
    assert.ok((await ec2('DescribeRouteTables',{'RouteTableId.1':table})).includes('>navigation-route<'));
    await tab('Routes').click();
    await btn('Add IPv4 route: '+table).click();
    const add=dlg('Add IPv4 route');
    await text(gateway,add).waitFor();
    await btn('Add IPv4 route',add).click();
    await page.getByRole('table').filter({hasText:'Destination'}).getByText('0.0.0.0/0',{exact:true}).waitFor();
    assert.ok((await ec2('DescribeRouteTables',{'RouteTableId.1':table})).includes('<gatewayId>'+gateway+'</gatewayId>'));
    await link(gateway).click();
    await waitRoute('#/vpc/internet-gateways/'+gateway);
    await page.getByRole('heading',{name:'Gateway details',exact:true}).waitFor();
    await page.goBack();
    await waitRoute('#/vpc/route-tables/'+table);
    await tab('Subnet associations').click();
    await btn('Associate subnet: '+table).click();
    const associationDlg=dlg('Associate subnet');
    await picker(/Subnet without an explicit association/,new RegExp(subnet),associationDlg);
    await btn('Associate subnet',associationDlg).click();
    await link(subnet).waitFor();
    const association=extract(await ec2('DescribeRouteTables',{'RouteTableId.1':table}),'routeTableAssociationId');
    assert.ok(association);
    await link(subnet).click();
    await waitRoute('#/vpc/subnets/'+subnet);
    await text('Explicit subnet association').waitFor();
    await link(table).click();
    await waitRoute('#/vpc/route-tables/'+table);
    await page.reload();
    await link(table,page.getByRole('table',{name:'Route tables',exact:true})).waitFor();
    await shot('route-table-desktop');

    activeCase='detached ACLs';
    await go('Network ACLs');
    await btn('Create network ACL').click();
    const createAcl=dlg('Create network ACL');
    await picker(/^VPC/,new RegExp(vpc),createAcl);
    await createAcl.getByRole('textbox',{name:'Name (optional)',exact:true}).fill('navigation-acl');
    await btn('Create network ACL',createAcl).click();
    await page.waitForURL(/\/network-acls\/acl-/);
    const acl=hash().split('/').at(-1).split('?')[0];
    await tab('Subnet associations').click();
    await text('The API confirms no associated subnets.').waitFor();
    assert.equal(await link(subnet).count(),0);
    const describeACL=(await ec2('DescribeNetworkAcls',{'NetworkAclId.1':acl}));
    assert.ok(describeACL.includes('<networkAclId>'+acl+'</networkAclId>')&&describeACL.includes('>navigation-acl<'));
    for(const not of ['Edit inbound rules','Edit outbound rules','Edit subnet associations']) assert.equal(await btn(not).count(),0);

    activeCase='security groups';
    await go('Security groups');
    await picker(/VPC filter/,/Navigation alpha/);
    await waitRoute('#/vpc/security-groups');
    await page.waitForURL(url=>url.hash.includes('&vpc='+vpc));
    await btn('Create security group').click();
    const createGroup=dlg('Create security group');
    await createGroup.getByRole('textbox',{name:'Group name',exact:true}).fill('navigation-group');
    await createGroup.getByRole('textbox',{name:'Group description',exact:true}).fill('Local detached group');
    await btn('Create security group',createGroup).click();
    await page.waitForURL(/\/security-groups\/sg-/);
    const group=hash().split('/').at(-1).split('?')[0];
    await btn('Add inbound rule').click();
    const addRule=dlg('Add inbound rule');
    await addRule.getByRole('textbox',{name:'From port',exact:true}).fill('443');
    await addRule.getByRole('textbox',{name:'To port',exact:true}).fill('443');
    await addRule.getByRole('textbox',{name:'Source IPv4 CIDR',exact:true}).fill(egress);
    await btn('Add inbound rule',addRule).click();
    await page.getByText('Saved security group rule:',{exact:false}).waitFor();
    assert.ok((await ec2('DescribeSecurityGroups',{'Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc})).includes('<cidrIp>'+egress+'</cidrIp>'));
    await page.reload();
    await text('Local detached group',page.locator('main')).first().waitFor();
    await btn('Remove inbound rule '+egress+' 443–443').waitFor();

    activeCase='mismatched, missing, malformed and unauthorized deep links';
    await page.goto(root+'#/vpc/security-groups/'+group+q+'&vpc='+foreign);
    await text('This security group was not found in the selected VPC.').waitFor();
    assert.equal(await btn('Remove inbound rule '+egress+' 443–443').count(),0);
    await page.goto(root+'#/vpc/subnets/'+subnet+q+'&vpc='+foreign);
    await text('This subnet was not found in the selected region.').waitFor();
    await page.goto(root+'#/vpc/route-tables/'+table+'?region=us-west-2&vpc='+vpc);
    await text('The selected route table is not in this inventory.').waitFor();
    assert.ok(calls.some(c=>c.region==='us-west-2'&&c.action==='DescribeRouteTables'));
    const beforeMalformed=calls.length;
    await page.goto(root+'#/vpc/route-tables/'+table+q+'&region=us-west-2');
    await text('This VPC view is unavailable in this console session.').waitFor();
    assert.equal(calls.slice(beforeMalformed).length,0);
    const anonymous=await browser.newContext({baseURL:origin});
    try{
      const anonymousPage=await anonymous.newPage(), observed=[];
      anonymousPage.on('request',req=>{if(req.method()==='POST'&&req.url()===origin+'/')observed.push(req);});
      await anonymousPage.goto(root+'#/vpc/network-acls/'+acl+q);
      await anonymousPage.waitForURL(url=>url.pathname==='/_simulator/login',{timeout:8000});
      assert.deepEqual(observed,[]);
    }finally{await anonymous.close();}

    activeCase='ordinary destructive controls against owned disposable resources';
    await page.goto(root+'#/vpc/security-groups/'+group+q+'&vpc='+vpc);
    await btn('Delete security group').click();
    const deleteGroup=dlg('Delete security group');
    await deleteGroup.getByRole('textbox',{name:'Type delete to confirm',exact:true}).fill('delete');
    await btn('Delete security group',deleteGroup).click();
    await page.waitForURL(url=>!url.hash.includes(group));
    assert.ok(!(await ec2('DescribeSecurityGroups',{'Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc})).includes(group));

    await page.goto(root+'#/vpc/network-acls/'+acl+q+'&vpc='+vpc);
    await btn('Delete network ACL').click();
    const deleteAcl=dlg('Delete network ACL');
    await deleteAcl.getByRole('textbox',{name:'Enter the network ACL ID to confirm deletion',exact:true}).fill(acl);
    await btn('Delete network ACL',deleteAcl).click();
    await page.waitForURL(url=>!url.hash.includes(acl));
    assert.ok(!(await ec2('DescribeNetworkAcls')).includes(acl));

    await ec2('DisassociateRouteTable',{AssociationId:association});
    await ec2('DeleteRoute',{RouteTableId:table,DestinationCidrBlock:'0.0.0.0/0'});
    await ec2('DeleteRouteTable',{RouteTableId:table});
    await page.goto(root+'#/vpc/internet-gateways/'+gateway+q);
    await btn('Detach from VPC').click();
    await btn('Detach',dlg('Detach from VPC')).click();
    await page.getByText('Removed the VPC attachment:',{exact:false}).waitFor();
    await btn('Delete internet gateway').click();
    const deleteGW=dlg('Delete internet gateway');
    await deleteGW.getByRole('textbox',{name:'Enter the gateway ID to confirm deletion',exact:true}).fill(gateway);
    await btn('Delete',deleteGW).click();
    await page.waitForURL(url=>!url.hash.includes(gateway));
    assert.ok(!(await ec2('DescribeInternetGateways')).includes(gateway));

    await ec2('DeleteSubnet',{SubnetId:subnet});
    await ec2('DeleteVpc',{VpcId:vpc});await ec2('DeleteVpc',{VpcId:foreign});
    assert.equal(extract(await ec2('DescribeVpcs'),'vpcId'),undefined);
    const writes=calls.filter(row=>networkUI(row.action));
    assert.deepEqual(writes.map(row=>row.action),[
      'CreateInternetGateway','AttachInternetGateway','CreateRouteTable','CreateRoute','AssociateRouteTable',
      'CreateNetworkAcl','CreateSecurityGroup','AuthorizeSecurityGroupIngress','DeleteSecurityGroup','DeleteNetworkAcl',
      'DetachInternetGateway','DeleteInternetGateway']);
    assert.ok(writes.every(row=>row.region===region),'a write crossed region');

    activeCase='Japanese narrow viewport';
    await page.evaluate(()=>localStorage.setItem('tenkacloud-simulator-locale','ja'));
    await page.setViewportSize({width:390,height:844});
    await page.goto(root+'#/vpc/network-acls'+q);
    await page.reload();
    await page.getByRole('heading',{name:'ネットワーク ACL',level:1,exact:true}).waitFor();
    await text('VPC の絞り込みに一致する関連付けのないネットワーク ACL はありません。').waitFor();
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
    await shot('network-acl-mobile-ja');
    assert.deepEqual(faults,[]);
    console.log(JSON.stringify({result:'PASS exact embedded Chromium against native SQLite/EC2',sha,
      savedWrites:writes.length,sidebar:true,deepLinks:true,regionAndVpcGuards:true,
      noUnauthenticatedEc2:true,cleanup:true,japaneseMobile:true,pageErrors:0}));
  }catch(error){error.message=activeCase+': '+error.message;throw error;}
  finally{
    await context?.close();await browser?.close();
    proc.kill('SIGTERM');
    const timer=setTimeout(()=>proc.kill('SIGKILL'),10000);
    try{assert.deepEqual(await stopped,{code:0,signal:null});}finally{clearTimeout(timer);await rm(work,{recursive:true,force:true});}
  }
}
run().catch(error=>{console.error(error);process.exitCode=1;});
