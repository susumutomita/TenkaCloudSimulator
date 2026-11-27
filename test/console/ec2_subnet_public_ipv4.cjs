'use strict';
const assert=require('node:assert/strict');
const {readFileSync}=require('node:fs');
const {createHash}=require('node:crypto');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {port,cookie,enabled,locale,principal,asset,vpc,subnet,neighbor,stackVpc,stackSubnet}=JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
const origin='http://127.0.0.1:'+port;
const action='ModifySubnetAttribute';
const strings=locale==='ja'?{
  edit:'サブネットの設定を編集', close:'サブネットのダイアログを閉じる', save:'変更を保存',
  notify:'サブネットの設定を保存しました: ', refresh:'サブネットを更新',
  failure:'サブネットの状態を確認できませんでした。', on:'有効', off:'無効',
}:{
  edit:'Edit subnet settings', close:'Close subnet dialog', save:'Save changes',
  notify:'Saved subnet settings: ', refresh:'Refresh subnets',
  failure:'Could not confirm subnet state.', on:'Enabled', off:'Disabled',
};
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(condition,why){
  for(const deadline=Date.now()+10000;Date.now()<deadline;){
    const found=condition(); if(found)return found;
    await wait(20);
  }
  assert.fail(why);
}

(async()=>{
  const [html,bundle]=await Promise.all(['','app.js'].map(path=>fetch(origin+'/_simulator/console/'+path,
    {headers:{Cookie:cookie},redirect:'error'}).then(r=>{assert.equal(r.status,200);return r.text();})));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',runScripts:'outside-only',
    pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document;
  const mutations=[];
  let readback=false, release;
  try {
    Object.assign(w,{TextEncoder,TextDecoder,
      matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
      ResizeObserver:class {observe(){} unobserve(){} disconnect(){}},
      IntersectionObserver:class {observe(){} unobserve(){} disconnect(){}},scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.localStorage.setItem('tenkacloud-simulator-locale',locale);
    w.fetch=async(path,init={})=>{
      assert.ok(path==='/'||path==='/_simulator/v1/capabilities');
      const headers=new Headers(init.headers);headers.set('Cookie',cookie);
      const body=init.body?.toString(), values=Object.fromEntries(new URLSearchParams(body));
      const response=await fetch(origin+path,{...init,headers,body,signal:undefined,redirect:'error'});
      if(values.Action===action){
        assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
        assert.equal(headers.get('X-Simulator-Principal'),principal);
        assert.equal(response.status,200);
        mutations.push(values);
        readback=true;
      } else if(values.Action==='DescribeSubnets'&&readback){
        assert.equal(response.status,200);
        readback=false;
        if(enabled){
          await new Promise(resolve=>{release=resolve;});
        } else {
          // Simulate a stale read of the same row, not a different write or DB.
          const doc=new w.DOMParser().parseFromString(await response.text(),'application/xml');
          const row=[...doc.querySelectorAll('subnetSet > item')].find(item=>
            item.querySelector('subnetId')?.textContent===subnet);
          const persisted=row?.querySelector('mapPublicIpOnLaunch');
          assert.equal(persisted?.textContent,'false');
          persisted.textContent='true';
          return new Response(new w.XMLSerializer().serializeToString(doc),{status:200,headers:{'Content-Type':'text/xml'}});
        }
      }
      return response;
    };
    w.eval(bundle);
    const text=s=>d.body.textContent.includes(s);
    const button=(label,parent=d)=>[...parent.querySelectorAll('button')].find(b=>b.textContent.trim()===label);
    const edit=id=>d.querySelector('button[aria-label="'+strings.edit+': '+id+'"]');
    const subnetRow=id=>edit(id)?.closest('tr');
    const dialog=()=>d.querySelector('button[aria-label="'+strings.close+'"]')?.closest('[role=dialog]');
    const select=async id=>{
      const input=await until(()=>[...d.querySelectorAll('tr')].find(r=>
        r.textContent.includes(id)&&r.querySelector('input[type=radio]'))?.querySelector('input[type=radio]'),
      'VPC missing from actual list');
      input.click();
    };
    await select(vpc);
    await until(()=>subnetRow(subnet)&&subnetRow(neighbor),'native subnet rows missing');
    assert.ok([...subnetRow(subnet).cells].some(cell=>cell.textContent.trim()===(enabled?strings.off:strings.on)));
    (await until(()=>{const b=edit(subnet);return b&&!b.disabled&&b;},'settings unavailable')).click();
    const input=await until(()=>dialog()?.querySelector('input[type=checkbox]'),'settings checkbox missing');
    assert.equal(input.checked,!enabled);
    assert.ok(button(strings.save,dialog()).disabled,'unchanged settings can be submitted');
    input.click();
    const save=await until(()=>{const b=button(strings.save,dialog());return b&&!b.disabled&&b;},'changed settings not ready');
    save.click();
    if(enabled){
      await until(()=>release,'no post-write readback');
      assert.ok(!text(strings.notify),'success appeared before readback');
      assert.ok(button('Close details').disabled,'parent view was not fenced while saving');
      release();
      await until(()=>text(strings.notify+subnet)&&subnetRow(subnet),'verified value did not publish');
    } else {
      await until(()=>text(strings.failure),'a readback that contradicted the write was accepted');
      assert.ok(!text(strings.notify));
      button(strings.refresh).click();
      await until(()=>subnetRow(subnet)&&!text(strings.failure),'refresh did not recover real durable state');
    }
    assert.ok([...subnetRow(subnet).cells].some(cell=>cell.textContent.trim()===(enabled?strings.on:strings.off)));
    assert.ok([...subnetRow(neighbor).cells].some(cell=>cell.textContent.trim()===strings.off));
    await select(stackVpc);
    await until(()=>text(stackSubnet),'CFN read view missing');
    assert.ok(!d.querySelector('button[aria-label^="'+strings.edit+': "]'),'CFN-owned subnet offered a native write');
    assert.deepEqual(mutations,[{Action:action,Version:'2016-11-15',SubnetId:subnet,'MapPublicIpOnLaunch.Value':String(enabled)}],
      'refresh or view change replayed a write');
  } finally {
    release?.();
    dom.window.close();
  }
})().catch(err=>{console.error(err);process.exitCode=1;});
