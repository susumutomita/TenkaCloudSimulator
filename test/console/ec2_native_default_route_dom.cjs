const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, cookie, vpc, table, gateway, asset, preset} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port < 65536);
const origin = 'http://127.0.0.1:' + port;
const sleep = ms => new Promise(resolve => setTimeout(resolve,ms));
async function until(find, message) {
  for (const end=Date.now()+9000;Date.now()<end;) {
    const match=find();
    if(match)return match;
    await sleep(20);
  }
  assert.fail(message);
}

async function boot(html, bundle, locale) {
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',runScripts:'outside-only',
    pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document;
  const receipt={calls:[],holdExact:false,wrongTable:false,release:null};
  Object.assign(w,{TextEncoder,TextDecoder,
    matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
    ResizeObserver:class {observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver:class {observe(){} unobserve(){} disconnect(){}},
    scrollTo(){}});
  w.localStorage.setItem('tenkacloud-simulator-locale',locale);
  w.HTMLElement.prototype.scrollIntoView=function(){};
  w.fetch=async(path, init={})=>{
    assert.ok(path==='/'||path==='/_simulator/v1/capabilities');
    const headers=new Headers(init.headers);
    headers.set('Cookie',cookie);
    const body=init.body?.toString(),form=new URLSearchParams(body),action=form.get('Action');
    const response=await fetch(origin+path,{...init,headers,body,signal:undefined,redirect:'error'});
    if (['DescribeRouteTables','DescribeInternetGateways','CreateRoute','DeleteRoute'].includes(action)) {
      assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
      assert.equal(form.get('Version'),'2016-11-15');
      receipt.calls.push({action,form:Object.fromEntries(form),status:response.status});
    }
    if (action==='DescribeRouteTables'&&form.get('RouteTableId.1')===table) {
      if (receipt.holdExact) {
        receipt.holdExact=false;
        await new Promise(resolve=>{receipt.release=resolve;});
      }
      if (receipt.wrongTable) {
        receipt.wrongTable=false;
        const raw=await response.text();
        assert.ok(raw.includes(table));
        // Real deletion already returned; this local faulty transport swaps
        // the readback identity. A successful empty route set is not enough.
        return new Response(raw.replaceAll(table,'rtb-1029384756abcdef0'),{status:200,headers:{'Content-Type':'text/xml'}});
      }
    }
    return response;
  };
  w.eval(bundle);
  return {dom,w,d,receipt};
}

(async()=>{
  const [html,bundle]=await Promise.all(['/','/app.js'].map(tail=>
    fetch(origin+'/_simulator/console'+tail,{headers:{Cookie:cookie},redirect:'error'}).then(async response=>{
      assert.equal(response.status,200);
      return response.text();
    })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  const selectVPC=(d,language)=>until(()=>{
    const idCell=[...d.querySelectorAll('tr')].find(row=>row.textContent.includes(vpc)&&!row.textContent.includes(gateway));
    return idCell?.querySelector('input[type=radio],input[type=checkbox]');
  },'missing selectable '+language+' VPC row');
  const {dom,w,d,receipt}=await boot(html,bundle,'en');
  try {
    const btn=(label,parent=d)=>[...parent.querySelectorAll('button')].find(b=>b.textContent.trim()===label);
    const rowButton=label=>d.querySelector('button[aria-label="'+label+': '+table+'"]');
    const dialog=()=>d.querySelector('button[aria-label="Close route table dialog"]')?.closest('[role=dialog]');
    const formChange=(input,text)=>{
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(input,text);
      input.dispatchEvent(new w.InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:text}));
      input.dispatchEvent(new w.Event('change',{bubbles:true}));
    };
    (await selectVPC(d,'English')).click();
    const add='Add IPv4 default route',remove='Remove IPv4 default route';
    (await until(()=>rowButton(add)&&!rowButton(add).disabled&&rowButton(add),'missing available native route control')).click();
    const submit=await until(()=>dialog()?.textContent.includes(gateway)&&!btn(add,dialog())?.disabled&&btn(add,dialog()),
      'missing verified attached gateway in route dialog');
    assert.ok(dialog().textContent.includes('0.0.0.0/0'));
    assert.equal(receipt.calls.filter(c=>c.action==='CreateRoute').length,0);
    receipt.holdExact=true;
    submit.click();
    await until(()=>receipt.release,'no exact-table readback for route creation');
    assert.ok(!d.body.textContent.includes('Saved IPv4 default route in:'));
    assert.deepEqual(receipt.calls.find(c=>c.action==='CreateRoute').form,
      {Action:'CreateRoute',Version:'2016-11-15',RouteTableId:table,DestinationCidrBlock:'0.0.0.0/0',GatewayId:gateway});
    receipt.release();
    await until(()=>d.body.textContent.includes('Saved IPv4 default route in: '+table)&&rowButton(remove),
      'creation was not published after its exact durable readback');
    assert.ok(rowButton('Delete route table').disabled||rowButton('Delete route table').getAttribute('aria-disabled')==='true');
    assert.ok(rowButton(remove).closest('tr').textContent.includes(gateway));
    const ref=await fetch(origin+'/',{method:'POST',redirect:'error',headers:{Cookie:cookie,'X-Simulator-Service':'ec2',
      'X-Simulator-Region':'us-east-1','Content-Type':'application/x-www-form-urlencoded'},
      body:new URLSearchParams({...preset,'RouteTableId.1':table})});
    assert.equal(ref.status,200);
    const refXML=new w.DOMParser().parseFromString(await ref.text(),'application/xml');
    assert.equal(refXML.querySelector('routeTableId').textContent,table);
    assert.equal(refXML.querySelector('routeSet item gatewayId').textContent,gateway);
    assert.ok(receipt.calls.some(c=>c.action==='DescribeRouteTables'&&c.form['RouteTableId.1']===table&&c.status===200));
    rowButton(remove).click();
    const removal=await until(()=>dialog()?.querySelector('input'),'missing exact table ID confirmation');
    assert.ok(btn(remove,dialog()).disabled);
    formChange(removal,table);
    await until(()=>!btn(remove,dialog())?.disabled,'matching table ID did not permit removal');
    receipt.wrongTable=true;
    btn(remove,dialog()).click();
    await until(()=>d.body.textContent.includes('Could not confirm the saved route table state.'),'accepted a different table as deletion proof');
    assert.ok(!d.body.textContent.includes('Removed IPv4 default route from:'));
    assert.deepEqual(receipt.calls.find(c=>c.action==='DeleteRoute').form,
      {Action:'DeleteRoute',Version:'2016-11-15',RouteTableId:table,DestinationCidrBlock:'0.0.0.0/0'});
    btn('Refresh route tables').click();
    await until(()=>rowButton(add)&&!rowButton(add).disabled,'fresh inventory did not show the saved deletion');
    assert.ok(!rowButton(remove),'refresh did not clear stale route');
    assert.equal(receipt.calls.filter(c=>c.action==='DeleteRoute').length,1,'refresh replayed deletion');

    // The normal delete path also requires the exact table + canonical empty
    // route result, independently of the preceding faulty-response scenario.
    rowButton(add).click();
    (await until(()=>dialog()?.textContent.includes(gateway)&&!btn(add,dialog())?.disabled&&btn(add,dialog()),
      'could not verify gateway on second route creation')).click();
    await until(()=>d.body.textContent.includes('Saved IPv4 default route in: '+table)&&rowButton(remove),'missing second create');
    rowButton(remove).click();
    const again=await until(()=>dialog()?.querySelector('input'),'missing second confirmation');
    formChange(again,table);
    await until(()=>!btn(remove,dialog())?.disabled,'second matching ID did not permit removal');
    btn(remove,dialog()).click();
    await until(()=>d.body.textContent.includes('Removed IPv4 default route from: '+table)&&rowButton(add),'verified delete was not shown');
  } finally {
    dom.window.close();
  }
  const jp=await boot(html,bundle,'ja');
  try {
    (await selectVPC(jp.d,'Japanese')).click();
    await until(()=>jp.d.querySelector('button[aria-label="IPv4 デフォルトルートを追加: '+table+'"]'),'missing Japanese route action');
    assert.ok(jp.d.body.textContent.includes('通信は転送されません'));
  } finally {
    jp.dom.window.close();
  }
  console.log('PASS signed embedded Cloudscape/JSDOM: IPv4 route add, held exact readback, unconfirmed wrong table, refresh without replay, verified delete, Japanese route control.');
})().catch(error=>{console.error(error);process.exitCode=1;});
