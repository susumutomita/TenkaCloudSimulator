const assert=require('node:assert/strict');
const {readFileSync}=require('node:fs');
const {createHash, randomUUID}=require('node:crypto');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const opts=JSON.parse(readFileSync(0,'utf8'));
const {port,cookie,vpc,stackVpc,stackSubnet,asset,principal,phase,legacyRoute,peerRoute,nativeMain,peerMain}=opts;
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
const origin='http://127.0.0.1:'+port;
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function until(fn,label){
  for(const end=Date.now()+16000;Date.now()<end;){const value=fn();if(value)return value;await sleep(15);}
  assert.fail(label);
}
const button=(node,text)=>[...(node?.querySelectorAll('button')??[])].find(b=>b.textContent.trim()===text);
const ruleTitles=['Authorize security group ingress','Edit inbound rules','Edit outbound rules','Edit subnet associations','Attach to instance'];

(async()=>{
  const [html,bundle]=await Promise.all(['/','/app.js'].map(path=>fetch(origin+'/_simulator/console'+path,
    {headers:{Cookie:cookie},redirect:'error'}).then(r=>{assert.equal(r.status,200);return r.text();})));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset,'must use exact served asset');
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',runScripts:'outside-only',
    pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document,calls=[];
  const allowed=new Set(['DescribeVpcs','DescribeSubnets','CreateSubnet','DeleteSubnet','DescribeSecurityGroups',
    'CreateSecurityGroup','DeleteSecurityGroup','DescribeNetworkAcls','CreateNetworkAcl','DeleteNetworkAcl','DescribeRouteTables','CreateRouteTable','DeleteRouteTable']);
  let target, holdNextGroup, writeRefused, staleSignal, continueWrite, continueStale, holdNextRoute, continueRoute;
  try{
    Object.assign(w,{TextEncoder,TextDecoder,matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
      ResizeObserver:class{observe(){}unobserve(){}disconnect(){}},IntersectionObserver:class{observe(){}unobserve(){}disconnect(){}},scrollTo(){}});
    Object.defineProperty(w.crypto,'randomUUID',{value:randomUUID});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.fetch=async(path,init={})=>{
      assert.ok(path==='/'||path==='/_simulator/v1/capabilities','unexpected application route');
      const headers=new Headers(init.headers);headers.set('Cookie',cookie);
      const body=init.body?.toString(), form=new URLSearchParams(body), action=form.get('Action');
      if(path==='/'){
        assert.ok(allowed.has(action),'unexpected operation '+action);
        assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
        assert.equal(headers.get('X-Simulator-Principal'),principal);
        if(action==='DescribeSecurityGroups'||action==='DescribeNetworkAcls'){
          assert.equal(form.get('Filter.1.Name'),'vpc-id');
          assert.equal(form.get('Filter.1.Value.1'),target);
          if(action==='DescribeNetworkAcls') assert.equal(target,vpc,'CFN pane must not query ACLs');
        }
        if(action.startsWith('Create')) assert.equal(form.get('VpcId'),vpc,'must create under selected native VPC');
        if(action==='CreateSubnet') assert.equal(form.get('CidrBlock'),'10.74.1.0/24');
        if(action==='CreateNetworkAcl') assert.match(form.get('ClientToken'),/^[a-f0-9-]{36}$/);
        if(action==='DescribeRouteTables')assert.deepEqual(Object.fromEntries(form),{Action:action,Version:'2016-11-15',MaxResults:'100'});
        if(action==='CreateRouteTable'){
          assert.match(form.get('ClientToken'),/^[a-f0-9]{32}$/);
          assert.equal(form.get('TagSpecification.1.ResourceType'),'route-table');
          assert.equal(form.get('TagSpecification.1.Tag.1.Value'),'GUI route <設定>');
        }
        if(action==='DeleteRouteTable')assert.equal(form.get('RouteTableId'),opts.saved.table);

      }
      const bridge=init.signal&&new AbortController();
      if(bridge){
        if(init.signal.aborted)bridge.abort();else init.signal.addEventListener('abort',()=>bridge.abort(),{once:true});
      }
      const response=await fetch(origin+path,{...init,headers,body,redirect:'error',signal:bridge?.signal});
      if(action) calls.push([action,response.status,form.get('Filter.1.Value.1')]);
      if(action==='CreateSecurityGroup'&&response.ok)holdNextGroup='write';
      if(action==='CreateRouteTable'&&response.ok)holdNextRoute=true;
      if(action==='DescribeRouteTables'&&holdNextRoute){
        holdNextRoute=false;
        await new Promise(release=>{continueRoute=release;});
      }

      if(action==='DescribeSecurityGroups'&&target===vpc&&holdNextGroup){
        const mode=holdNextGroup;holdNextGroup=null;
        await new Promise(release=>{
          if(mode==='write')continueWrite=release;
          else{staleSignal=init.signal;continueStale=release;}
        });
      }
      return response;
    };
    w.eval(bundle);
    const display=text=>d.body.textContent.includes(text);
    const ctrl=(title,node=d)=>button(node,title)||[...(node?.querySelectorAll('button')??[])].find(b=>b.getAttribute('aria-label')===title);
    const click=async(title,node=d)=>(await until(()=>{const b=ctrl(title,node);return b&&!b.disabled&&b;},'unavailable control '+title)).click();
    const radio=async id=>until(()=>d.querySelector('input[type="radio"][aria-label="Select '+id+'"]'),'missing VPC');
    const select=async id=>{target=id;(await radio(id)).click();};
    const active=label=>d.querySelector('button[aria-label="Close '+label+' dialog"]')?.closest('[role="dialog"]');
    const fill=async(label,index,text)=>{
      const inputs=await until(()=>active(label)?.querySelectorAll('input[type="text"]')?.[index],'missing '+label+' input');
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(inputs,text);
      inputs.dispatchEvent(new w.InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:text}));
      inputs.dispatchEvent(new w.Event('change',{bubbles:true}));
      await sleep(45);
    };
    const submit=async(label,title)=>click(title,await until(()=>active(label),'missing active '+label+' modal'));
    async function legacy(){
      await select(stackVpc);
      await until(()=>display(stackSubnet)&&display('CFN readback is still here'),'missing legacy subnet/group');
      assert.ok(display('CloudFormation owns this VPC.'));
      await until(()=>display(legacyRoute),'missing CloudFormation route row');
      assert.ok(!display(peerRoute)&&!display(peerMain)&&!display(nativeMain));

      assert.ok(display('This panel does not read or change its network ACLs.'));
      for(const name of ['Add subnet','Create security group','Create network ACL','Create route table','Delete subnet','Delete security group','Delete network ACL','Delete route table','Associate subnet','Disassociate subnet'])assert.ok(!button(d,name),name+' is not a CFN action');
    }
    await legacy();
    await select(vpc);
    await until(()=>display('Independent native group')||phase==='delete'&&display('Detached GUI group'),'native SG not listed');
    assert.ok(!display(legacyRoute)&&!display(peerRoute)&&!display(peerMain));
    for(const title of ruleTitles)assert.ok(!button(d,title),title+' must not be offered');
    // Explicit association is a route-table action, not a subnet/SG/ACL
    // edit. Only verified tables in this VPC, including its main, offer it.
    const scopedTables=[nativeMain,...(phase==='delete'?[opts.saved.table]:[])].sort();
    await until(()=>scopedTables.every(id=>d.querySelector('button[aria-label="Associate subnet: '+id+'"]')),
      'saved native rows missing verified route association controls');
    const associations=[...d.querySelectorAll('button[aria-label^="Associate subnet: "]')];
    assert.deepEqual(associations.map(b=>b.getAttribute('aria-label')).sort(),scopedTables.map(id=>'Associate subnet: '+id));
    const protectedMain=ctrl('Delete route table: '+nativeMain);
    assert.equal(protectedMain.getAttribute('aria-disabled'),'true');
    if(phase==='create'){
      await click('Add subnet');
      await fill('subnet',0,'10.75.1.0/24');
      assert.ok(ctrl('Create subnet',active('subnet')).disabled);
      await fill('subnet',0,'10.74.1.11/24');
      await submit('subnet','Create subnet');
      const subnet=await until(()=>/Saved subnet: (subnet-[a-f0-9]{17})/.exec(d.body.textContent)?.[1],'subnet success missing');
      await click('Create security group');
      await fill('security group',0,'GUI group');await fill('security group',1,'Detached GUI group');
      await submit('security group','Create security group');
      await until(()=>continueWrite&&ctrl('Close details')?.disabled,'missing aggregate write lease');
      assert.ok(!display('Saved security group:'),'success published before real native readback');
      assert.ok(ctrl('Create network ACL').disabled);
      assert.ok(ctrl('Add subnet').closest('[inert]'));
      assert.ok(ctrl('Services').disabled,'host navigation block did not include held write');
      (await radio(stackVpc)).click();
      assert.ok(!display(stackSubnet),'VPC switched during a write');
      continueWrite();
      const group=await until(()=>/Saved security group: (sg-[a-f0-9]{17})/.exec(d.body.textContent)?.[1],'group success missing');
      await click('Create network ACL');
      await fill('network ACL',0,'GUI ACL <local>');await submit('network ACL','Create network ACL');
      const acl=await until(()=>/Saved network ACL: (acl-[a-f0-9]{17})/.exec(d.body.textContent)?.[1],'ACL success missing');
      assert.ok(display('GUI ACL <local>')&&display(subnet)&&display(group));
      await click('Create route table');
      await fill('route table',0,'界'.repeat(86));
      assert.ok(ctrl('Create route table',active('route table')).disabled);
      await fill('route table',0,'GUI route <設定>');
      await submit('route table','Create route table');
      await until(()=>continueRoute&&ctrl('Close details')?.disabled,'missing route-table write lease');
      assert.ok(!display('Saved route table:'));
      assert.ok(ctrl('Add subnet').closest('[inert]')&&ctrl('Create network ACL').disabled&&ctrl('Services').disabled);
      continueRoute();
      const table=await until(()=>/Saved route table: (rtb-[a-f0-9]{17})/.exec(d.body.textContent)?.[1],'route readback missing');
      assert.ok(display('GUI route <設定>')&&!display(legacyRoute)&&!display(peerRoute)&&!display(peerMain));

      holdNextGroup='stale';
      await click('Refresh security groups');
      await until(()=>continueStale,'missing held read after refresh');
      await legacy();
      assert.equal(staleSignal.aborted,true);
      continueStale();
      await sleep(120);
      assert.ok(!display('Detached GUI group'),'late old-scope response leaked into CFN pane');
      writeRefused=true;
      console.log(JSON.stringify({saved:{subnet,group,acl,table},holdProof:true,lateReadAborted:staleSignal.aborted,heldRouteReadback:!!continueRoute}));
    }else{
      const {subnet,group,acl,table}=opts.saved;
      const mouse=(node,type)=>node.dispatchEvent(new w.MouseEvent(type,{bubbles:true,cancelable:true,button:0}));
      async function chooseGroup(id,description){
        if(display(description)&&display(id))return;
        const picker=await until(()=>[...d.querySelectorAll('button[aria-haspopup="listbox"]')].find(b=>{
          const labels=(b.getAttribute('aria-labelledby')??'').split(' ').map(id=>d.getElementById(id)?.textContent.trim());
          return labels.includes('Saved security group');
        }),'missing group selector');
        // Cloudscape Select opens on mouse-down and commits on option mouse-up.
        mouse(picker,'mousedown');
        const option=await until(()=>[...(d.getElementById(picker.getAttribute('aria-controls'))?.querySelectorAll('[role="option"]')??[])]
          .find(row=>row.textContent.includes(id)),'missing scoped saved SG option');
        mouse(option,'mouseup');
        await until(()=>display(description)&&display(id),'saved SG selection was not applied');
      }
      // Force a real selection change on every run, regardless of generated-ID order.
      await chooseGroup(opts.sibling,'Independent native group');
      await chooseGroup(group,'Detached GUI group');
      await click('Delete security group');
      await fill('security group',0,'delete');await submit('security group','Delete security group');
      await until(()=>display('Removed security group: '+group),'group removal missing');
      assert.ok(display(acl)&&display(subnet)&&display('Independent native group'));
      await click('Delete network ACL');
      await fill('network ACL',0,'acl-incorrect');
      assert.ok(ctrl('Delete network ACL',active('network ACL')).disabled);
      await fill('network ACL',0,acl);await submit('network ACL','Delete network ACL');
      await until(()=>display('Deleted network ACL: '+acl),'ACL removal missing');
      assert.ok(display(subnet)&&display('Independent native group'));
      await click('Delete route table: '+table);
      await fill('route table',0,peerRoute);
      assert.ok(ctrl('Delete route table',active('route table')).disabled);
      await fill('route table',0,table);await submit('route table','Delete route table');
      await until(()=>display('Deleted route table: '+table)&&!ctrl('Delete route table: '+table)&&ctrl('Delete route table: '+nativeMain),
        'custom route table removal or preserved main was not confirmed');
      await click('Delete subnet');await fill('subnet',0,'delete');await submit('subnet','Delete subnet');
      await until(()=>display('Removed subnet: '+subnet)&&display('No subnets are recorded for this VPC.'),'subnet removal missing');
      await legacy();
      console.log(JSON.stringify({removed:true}));
    }
    const mutations=calls.filter(([a])=>a.startsWith(phase==='create'?'Create':'Delete'));
    assert.deepEqual(mutations.map(([a,status])=>[a,status]),phase==='create'
      ?[['CreateSubnet',200],['CreateSecurityGroup',200],['CreateNetworkAcl',200],['CreateRouteTable',200]]
      :[['DeleteSecurityGroup',200],['DeleteNetworkAcl',200],['DeleteRouteTable',200],['DeleteSubnet',200]]);
    if(phase==='create')assert.ok(writeRefused);
  } catch(error){console.error('Observed safe Console diagnostics:',calls,d.body.textContent.slice(-1300));throw error;}
  finally {continueWrite?.();continueStale?.();continueRoute?.();w.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
