const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, cookie, vpc, stackVpc, stackSubnet} = JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port < 65536);
const origin = 'http://127.0.0.1:' + port;
const sleep = ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(test,note){
  for(let limit=Date.now()+20000;Date.now()<limit;){const s=test();if(s)return s; await sleep(15);}
  assert.fail(note);
}
const button=(node,title)=>[...(node?.querySelectorAll('button')??[])].find(b=>b.textContent.trim()===title);
(async()=>{
  const [html, bundle]=await Promise.all(['/', '/app.js'].map(path=>fetch(origin+'/_simulator/console'+path,{headers:{Cookie:cookie},redirect:'error'}).then(r=>{
    assert.equal(r.status,200); return r.text();
  })));
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',runScripts:'outside-only',pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document,calls=[];
  try {
    Object.assign(w,{TextEncoder,TextDecoder,matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
      ResizeObserver:class{observe(){}unobserve(){}disconnect(){}},IntersectionObserver:class{observe(){}unobserve(){}disconnect(){}},scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.fetch=async(path,init={})=>{
      assert.ok(path==='/'||path==='/_simulator/v1/capabilities','unexpected application route');
      const headers=new Headers(init.headers); headers.set('Cookie',cookie);
      const body=init.body?.toString(), form=new URLSearchParams(body), action=form.get('Action');
      if(path==='/') {
        assert.ok(['DescribeVpcs','DescribeSubnets','CreateSubnet','DeleteSubnet','DescribeSecurityGroups','DescribeRouteTables'].includes(action),'unexpected action '+action);
        assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
        if (action==='DescribeSecurityGroups') {
          assert.equal(form.get('Filter.1.Name'),'vpc-id');
          assert.equal(form.get('Filter.1.Value.1'),stackVpc);
        }
        if (action==='CreateSubnet') {assert.equal(form.get('VpcId'),vpc); assert.equal(form.get('CidrBlock'),'10.74.1.0/24');}
      }
      const bridge=init.signal&&new AbortController();
      if(bridge) {
        if(init.signal.aborted)bridge.abort();else init.signal.addEventListener('abort',()=>bridge.abort(),{once:true});
      }
      const r=await fetch(origin+path,{...init,headers,body,redirect:'error',signal:bridge?.signal});
      if(action) calls.push([action,r.status,headers.get('X-Simulator-Principal')]);
      return r;
    };
    w.eval(bundle);
    const display=s=>d.body.textContent.includes(s);
    const select=async id=>(await until(()=>d.querySelector('input[type="radio"][aria-label="Select '+id+'"]'),'VPC list missing id')).click();
    await select(stackVpc);
    await until(()=>display(stackSubnet),'CloudFormation readback absent');
    assert.ok(display('CloudFormation owns this VPC.'));
    await until(()=>display('CFN readback is still here'),'existing CloudFormation SG pane lost its readback with a native SG present');
    assert.equal(button(d,'Add subnet'),undefined);
    await select(vpc);
    const add=await until(()=>{const b=button(d,'Add subnet');return b&&!b.disabled&&b;},'native subnet action absent');
    assert.ok(!display(stackSubnet));
    assert.ok(display('This pane displays CloudFormation-managed groups only.'));
    assert.ok(!display('Detached group'));
    add.click();
    const dialog=()=>d.querySelector('button[aria-label="Close subnet dialog"]')?.closest('[role="dialog"]');
    const fill=async text=>{
      const input=await until(()=>dialog()?.querySelector('input[type="text"]'),'subnet form input absent');
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(input,text);
      input.dispatchEvent(new w.InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:text}));
      input.dispatchEvent(new w.Event('change',{bubbles:true}));
      await sleep(40);
    };
    const submit=async title=>(await until(()=>{const b=button(dialog(),title);return b&&!b.disabled&&b;},'subnet submit unavailable')).click();
    await fill('10.75.1.0/24');
    assert.ok(button(dialog(),'Create subnet').disabled);
    await fill('10.74.1.11/24');
    await submit('Create subnet');
    await until(()=>display('Saved subnet:'),'native create was not confirmed');
    const saved= /Saved subnet: (subnet-[0-9a-f]{17})/.exec(d.body.textContent)?.[1];
    assert.ok(saved);
    assert.ok(display('10.74.1.0/24'));
    const found=calls.findIndex(x=>x[0]==='CreateSubnet');
    assert.deepEqual(calls.slice(found,found+2).map(x=>x.slice(0,2)),[['CreateSubnet',200],['DescribeSubnets',200]]);
    assert.ok(calls.every(x=>x[2]==='subnet-builder'));
    (await until(()=>button(d,'Delete subnet'),'delete action missing')).click();
    await fill('delete');
    await submit('Delete subnet');
    await until(()=>display('Removed subnet: '+saved)&&display('No subnets are recorded for this VPC.'),'native delete was not confirmed');
    assert.equal(calls.filter(x=>x[0]==='CreateSubnet').length,1);
    assert.equal(calls.filter(x=>x[0]==='DeleteSubnet').length,1);
    await select(stackVpc);
    await until(()=>display(stackSubnet),'stack readback was lost');
    assert.equal(button(d,'Add subnet'),undefined);
    assert.equal(button(d,'Delete subnet'),undefined);
    console.log('PASS real embedded VPC Console in JSDOM: selected existing native VPC add/read/remove under selected participant policy; existing CFN subnet remains read-only.');
  } catch(error) {console.error('Observed safe Console diagnostics:', calls, d.body.textContent.slice(-1100)); throw error;} finally {w.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
