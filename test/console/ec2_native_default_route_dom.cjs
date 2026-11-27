const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, cookie, vpc, table, gateway, asset, preset} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port < 65536);
const origin = 'http://127.0.0.1:' + port;
const sleep = ms => new Promise(resolve => setTimeout(resolve,ms));
const disabled = button => button.disabled || button.getAttribute('aria-disabled') === 'true';
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
  const receipt={calls:[],holdExact:false,wrongTable:false,omitSavedSibling:false,release:null};
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
      if (receipt.omitSavedSibling) {
        receipt.omitSavedSibling=false;
        const state=new w.DOMParser().parseFromString(await response.text(),'application/xml');
        const routes=[...state.querySelectorAll('routeSet item')];
        assert.ok(routes.some(item=>item.querySelector('gatewayId')?.textContent==='local'));
        const route=routes.find(item=>item.querySelector('destinationCidrBlock')?.textContent==='198.51.100.0/24');
        assert.ok(route);
        route.remove();
        return new Response(new w.XMLSerializer().serializeToString(state),{status:200,headers:{'Content-Type':'text/xml'}});
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
    const add='Add IPv4 route',remove='Remove IPv4 route';
    (await until(()=>rowButton(add)&&!rowButton(add).disabled&&rowButton(add),'missing available native route control')).click();
    const submit=await until(()=>dialog()?.textContent.includes(gateway)&&!btn(add,dialog())?.disabled&&btn(add,dialog()),
      'missing verified attached gateway in route dialog');
    assert.equal(dialog().querySelector('input[placeholder="198.51.100.0/24"]').value,'0.0.0.0/0');
    assert.equal(receipt.calls.filter(c=>c.action==='CreateRoute').length,0);
    receipt.holdExact=true;
    submit.click();
    await until(()=>receipt.release,'no exact-table readback for route creation');
    assert.ok(!d.body.textContent.includes('Saved IPv4 route in:'));
    assert.deepEqual(receipt.calls.find(c=>c.action==='CreateRoute').form,
      {Action:'CreateRoute',Version:'2016-11-15',RouteTableId:table,DestinationCidrBlock:'0.0.0.0/0',GatewayId:gateway});
    receipt.release();
    await until(()=>d.body.textContent.includes('Saved IPv4 route in: '+table)&&rowButton(remove)&&!rowButton(remove).disabled,
      'creation was not published after its exact durable readback');
    assert.ok(disabled(rowButton('Delete route table')));
    assert.ok(rowButton(remove).closest('tr').textContent.includes(gateway));
    const {MaxResults:pageSize,...oneTable}=preset;
    assert.equal(pageSize,100);
    const ref=await fetch(origin+'/',{method:'POST',redirect:'error',headers:{Cookie:cookie,'X-Simulator-Service':'ec2',
      'X-Simulator-Region':'us-east-1','Content-Type':'application/x-www-form-urlencoded'},
      body:new URLSearchParams({...oneTable,'RouteTableId.1':table})});
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
    assert.ok(!d.body.textContent.includes('Removed IPv4 route from:'));
    assert.deepEqual(receipt.calls.find(c=>c.action==='DeleteRoute').form,
      {Action:'DeleteRoute',Version:'2016-11-15',RouteTableId:table,DestinationCidrBlock:'0.0.0.0/0'});
    btn('Refresh route tables').click();
    await until(()=>rowButton(add)&&!rowButton(add).disabled,'fresh inventory did not show the saved deletion');
    assert.ok(disabled(rowButton(remove)),'refresh did not clear stale route');
    assert.equal(receipt.calls.filter(c=>c.action==='DeleteRoute').length,1,'refresh replayed deletion');

    rowButton(add).click();
    (await until(()=>dialog()?.textContent.includes(gateway)&&!btn(add,dialog())?.disabled&&btn(add,dialog()),
      'could not verify gateway on second default route creation')).click();
    await until(()=>d.body.textContent.includes('Saved IPv4 route in: '+table)&&!rowButton(remove).disabled,'missing second default create');
    rowButton(add).click();
    const prefix=await until(()=>dialog()?.querySelector('input[placeholder="198.51.100.0/24"]'),'missing CIDR input');
    formChange(prefix,'198.51.100.93/24');
    await until(()=>dialog()?.textContent.includes('Saved as: 198.51.100.0/24')&&btn(add,dialog())&&!btn(add,dialog()).disabled,
      'missing canonical outside-VPC preview');
    btn(add,dialog()).click();
    await until(()=>rowButton(remove)?.closest('tr').textContent.includes('198.51.100.0/24')&&!rowButton(remove).disabled,
      'outside-VPC route was not saved alongside /0');
    rowButton(remove).click();
    const select=await until(()=>dialog()?.querySelector('button[aria-haspopup="listbox"]'),'missing saved route selector');
    select.dispatchEvent(new w.MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));
    const defaultOption=await until(()=>d.getElementById(select.getAttribute('aria-controls'))?.
      querySelector('[role="option"] [data-value="0.0.0.0/0"]')?.closest('[role="option"]'),
      'missing saved default route option');
    defaultOption.dispatchEvent(new w.MouseEvent('mouseup',{bubbles:true,cancelable:true,button:0}));
    const confirm=await until(()=>dialog()?.querySelector('input'),'missing confirmation after choosing one saved route');
    formChange(confirm,table);
    await until(()=>!btn(remove,dialog())?.disabled,'matching table ID did not permit selected route removal');
    receipt.omitSavedSibling=true;
    btn(remove,dialog()).click();
    await until(()=>d.body.textContent.includes('Could not confirm the saved route table state.'),'accepted an exact row with a missing sibling');
    assert.ok(!d.body.textContent.includes('Removed IPv4 route from:'));
    assert.equal(receipt.calls.filter(c=>c.action==='DeleteRoute').at(-1).form.DestinationCidrBlock,'0.0.0.0/0');
    btn('Refresh route tables').click();
    await until(()=>rowButton(remove)&&!rowButton(remove).disabled&&rowButton(remove).closest('tr').textContent.includes('198.51.100.0/24'),
      'fresh snapshot lost the untouched non-default sibling');
    assert.ok(!rowButton(remove).closest('tr').textContent.includes('0.0.0.0/0'));
    rowButton(remove).click();
    const again=await until(()=>dialog()?.querySelector('input'),'missing non-default route confirmation');
    formChange(again,table);
    await until(()=>!btn(remove,dialog())?.disabled,'matching ID did not permit final non-default removal');
    btn(remove,dialog()).click();
    await until(()=>d.body.textContent.includes('Removed IPv4 route from: '+table)&&disabled(rowButton(remove)),
      'verified non-default removal was not shown');
    assert.equal(receipt.calls.filter(c=>c.action==='DeleteRoute').at(-1).form.DestinationCidrBlock,'198.51.100.0/24');
  } finally {
    dom.window.close();
  }
  const jp=await boot(html,bundle,'ja');
  try {
    (await selectVPC(jp.d,'Japanese')).click();
    await until(()=>jp.d.querySelector('button[aria-label="IPv4 ルートを追加: '+table+'"]'),'missing Japanese route action');
    assert.ok(jp.d.body.textContent.includes('通信は転送されません'));
  } finally {
    jp.dom.window.close();
  }
  console.log('PASS signed embedded Cloudscape/JSDOM: IPv4 default and non-default routes, held exact readback, wrong table and missing sibling refusal, refresh without replay, exact detach-safe removal, Japanese controls.');
})().catch(error=>{console.error(error);process.exitCode=1;});
