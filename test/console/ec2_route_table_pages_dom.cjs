const assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const {readFileSync}=require('node:fs');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {port,cookie,vpc,saved,stack,asset}=JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
const origin='http://127.0.0.1:'+port;
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(find,message) {
  for(let end=Date.now()+12000;Date.now()<end;) {
    const found=find();
    if(found)return found;
    await delay(20);
  }
  assert.fail(message);
}

function boot(html,bundle,locale) {
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',
    pretendToBeVisual:true,runScripts:'outside-only',virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document;
  const receipt={calls:[],wait:null,mode:null};
  Object.assign(w,{TextEncoder,TextDecoder,matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
    ResizeObserver:class {observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver:class {observe(){} unobserve(){} disconnect(){}},scrollTo(){}});
  w.localStorage.setItem('tenkacloud-simulator-locale',locale);
  w.HTMLElement.prototype.scrollIntoView=function(){};
  w.fetch=async(path,init={})=>{
    assert.ok(path==='/'||path==='/_simulator/v1/capabilities');
    const headers=new Headers(init.headers); headers.set('Cookie',cookie);
    const body=init.body?.toString(),form=new URLSearchParams(body),action=form.get('Action');
    const page=action==='DescribeRouteTables',continuation=page&&form.has('NextToken');
    const event={action,continuation,filter:form.get('Filter.1.Value.1')};
    if(page||action==='DeleteRouteTable') {
      assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
      assert.equal(form.get('Version'),'2016-11-15');
      if(page) assert.equal(form.get('MaxResults'),'100');
      receipt.calls.push(event);
    }
    const send=()=>fetch(origin+path,{...init,headers,body,signal:undefined,redirect:'error'});
    const wait=async()=>{
      receipt.mode=null;
      await new Promise(resolve=>{receipt.wait={release:resolve,signal:init.signal};});
    };
    if(continuation&&receipt.mode==='before') await wait();
    const response=await send();
    event.status=response.status;
    if(continuation&&receipt.mode==='after') await wait();
    return response;
  };
  w.eval(bundle);
  const button=(text,parent=d)=>[...parent.querySelectorAll('button')].find(b=>b.textContent.trim()===text);
  const text=label=>d.body.textContent.includes(label);
  const select=async id=>{
    const radio=await until(()=>[...d.querySelectorAll('tr')].find(tr=>tr.textContent.includes(id))?.querySelector('input[type=radio]'),
      'target VPC is not selectable');
    radio.click();
  };
  const fill=(input,value)=>{
    Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(input,value);
    input.dispatchEvent(new w.InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:value}));
    input.dispatchEvent(new w.Event('change',{bubbles:true}));
  };
  return {dom,w,d,receipt,button,text,select,fill};
}

(async()=>{
  const [html,bundle]=await Promise.all(['/','/app.js'].map(tail=>
    fetch(origin+'/_simulator/console'+tail,{headers:{Cookie:cookie},redirect:'error'}).then(async response=>{
      assert.equal(response.status,200);
      return response.text();
    })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  const en=boot(html,bundle,'en');
  let committedPeer;
  try {
    const {d,receipt,button,text,select,fill}=en;
    receipt.mode='after';
    await select(vpc);
    const old=await until(()=>receipt.wait,'native continuation did not arrive');
    assert.equal(receipt.calls.length,2);
    assert.ok(receipt.calls[1].continuation&&receipt.calls[1].status===200);
    assert.ok(!text(saved[0]),'first page was published while tail was held');
    assert.ok(button('Create route table').disabled,'mutation enabled on a prefix');

    // The CFN pane uses the same real regional paginated read. Late native
    // rows must not appear there, even with a transport that ignores abort.
    receipt.wait=null;
    await select(stack.Vpc);
    await until(()=>text(stack.Table),'the CFN table was not read from the complete regional sequence');
    assert.equal(old.signal.aborted,true);
    old.release();
    await delay(60);
    assert.ok(!text(saved[0]));
    const filter=await until(()=>d.querySelector('input[placeholder="subnet-…"]'),'CFN filter missing');
    fill(filter,stack.Subnet);
    await until(()=>receipt.calls.some(a=>a.filter===stack.Subnet&&a.status===200)&&text(stack.Subnet),'CFN filter did not match');

    await select(vpc);
    const deletion=()=>d.querySelector('button[aria-label="Delete route table: '+saved[0]+'"]');
    (await until(()=>deletion()&&!deletion().disabled&&deletion(),'complete native read did not authorize normal deletion')).click();
    const dialog=()=>d.querySelector('button[aria-label="Close route table dialog"]')?.closest('[role=dialog]');
    fill(await until(()=>dialog()?.querySelector('input'),'missing exact ID confirmation'),saved[0]);
    const submit=await until(()=>dialog()&&button('Delete route table',dialog())&&!button('Delete route table',dialog()).disabled&&
      button('Delete route table',dialog()),'exact table delete did not become available');
    receipt.mode='before';
    submit.click();
    const post=await until(()=>receipt.wait,'fresh regional deletion readback did not reach next page');
    assert.ok(!text('Deleted route table:'),'write appeared confirmed from first page only');
    assert.equal(receipt.calls.filter(a=>a.action==='DeleteRouteTable'&&a.status===200).length,1);
    // Use the same ordinary authenticated local API to create a different
    // table between two valid readback requests. The server, not this
    // fixture, must invalidate the authentic continuation.
    const peer=await fetch(origin+'/',{method:'POST',redirect:'error',
      headers:{Cookie:cookie,'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':'us-east-1'},
      body:new URLSearchParams({Action:'CreateRouteTable',Version:'2016-11-15',VpcId:vpc})});
    assert.equal(peer.status,200);
    const xml=new en.w.DOMParser().parseFromString(await peer.text(),'application/xml');
    committedPeer=xml.querySelector('routeTableId').textContent;
    post.release();
    await until(()=>text('Could not confirm the saved route table state.')&&receipt.calls.some(a=>a.continuation&&a.status===400),
      'a moved snapshot was not reported as unconfirmed');
    assert.ok(!text('Deleted route table:')&&!text(saved[1]));
    button('Refresh route tables').click();
    await until(()=>text(saved[1])&&!text(saved[0]),'fresh pages did not show the actual saved deletion');
    assert.equal(receipt.calls.filter(a=>a.action==='DeleteRouteTable').length,1,'refresh repeated a mutation');
  } finally {
    en.dom.window.close();
  }
  const ja=boot(html,bundle,'ja');
  try {
    await ja.select(vpc);
    await until(()=>ja.text(saved[1])&&ja.text('保存済みの設定のみです。')&&
      ja.receipt.calls.some(a=>a.continuation&&a.status===200),'Japanese complete inventory not published');
    assert.ok(!ja.text(saved[0])&&ja.button('ルートテーブルを作成'));
  } finally {
    ja.dom.window.close();
  }
  console.log(JSON.stringify({removed:saved[0],peer:committedPeer,completeJSDOM:true,lateScopeRetired:true,staleWriteUnconfirmed:true}));
})().catch(error=>{console.error(error);process.exitCode=1;});
