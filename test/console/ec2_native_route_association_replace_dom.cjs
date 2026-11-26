const assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const {readFileSync}=require('node:fs');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {port,cookie,vpc,subnets,tables,ids,legacy,asset}=JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
const origin='http://127.0.0.1:'+port;
const action='ReplaceRouteTableAssociation';
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));

async function until(find,error) {
  for (const end=Date.now()+9000;Date.now()<end;) {
    const found=find();
    if(found)return found;
    await pause(20);
  }
  assert.fail(error);
}

function local(path,options={}) {
  return fetch(origin+path,{...options,redirect:'error',headers:{Cookie:cookie,...options.headers}});
}

function boot(html,bundle,locale) {
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',runScripts:'outside-only',
    pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document;
  const proof={commands:[],pages:[],hold:false,omit:null,release:null};
  Object.assign(w,{TextEncoder,TextDecoder,
    matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
    ResizeObserver:class {observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver:class {observe(){} unobserve(){} disconnect(){}},scrollTo(){}});
  w.HTMLElement.prototype.scrollIntoView=function(){};
  w.localStorage.setItem('tenkacloud-simulator-locale',locale);
  const xml=text=>new w.DOMParser().parseFromString(text,'application/xml');
  w.fetch=async(path,init={})=>{
    assert.ok(path==='/'||path==='/_simulator/v1/capabilities');
    const headers=new Headers(init.headers);
    headers.set('Cookie',cookie);
    const body=init.body?.toString(), fields=Object.fromEntries(new URLSearchParams(body));
    const response=await fetch(origin+path,{...init,headers,body,signal:undefined,redirect:'error'});
    if(fields.Action===action) {
      assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
      assert.equal(response.status,200);
      const doc=xml(await response.clone().text());
      const next=doc.querySelector('newAssociationId').textContent;
      proof.commands.push({fields,next});
    }
    if(fields.Action==='DescribeRouteTables') {
      assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
      assert.equal(response.status,200);
      proof.pages.push(fields);
      if(proof.hold) {
        proof.hold=false;
        await new Promise(resolve=>{proof.release=resolve;});
      }
      if(proof.omit) {
        const doc=xml(await response.text());
        const retained=[...doc.querySelectorAll('associationSet item')].find(row=>
          row.querySelector('routeTableAssociationId')?.textContent===proof.omit);
        assert.ok(retained,'expected an untouched sibling in the real stored response');
        proof.omit=null;
        retained.remove();
        return new Response(new w.XMLSerializer().serializeToString(doc),
          {status:200,headers:{'Content-Type':'text/xml'}});
      }
    }
    return response;
  };
  w.eval(bundle);

  const rowButton=(label,table)=>d.querySelector('button[aria-label="'+label+': '+table+'"]');
  const routeRow=(label,table)=>rowButton(label,table)?.closest('tr');
  const button=(label,parent=d)=>[...parent.querySelectorAll('button')].find(b=>b.textContent.trim()===label);
  const dialog=()=>d.querySelector('button[aria-label="'+
    (locale==='ja'?'ルートテーブルのダイアログを閉じる':'Close route table dialog')+'"]')?.closest('[role=dialog]');
  async function selectVPC(id) {
    const input=await until(()=>[...d.querySelectorAll('tr')].find(row=>
      row.textContent.includes(id)&&row.querySelector('input[type=radio],input[type=checkbox]'))?.
      querySelector('input[type=radio],input[type=checkbox]'),'missing selectable VPC');
    input.click();
  }
  async function choose(select,id,excluded=[]) {
    select.dispatchEvent(new w.MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));
    const popup=await until(()=>d.getElementById(select.getAttribute('aria-controls'))?.querySelector('[role=option]')?.parentElement,
      'select list did not open');
    for(const wrong of excluded)assert.ok(!popup.querySelector('[data-value="'+wrong+'"]'),'foreign or same target was offered');
    const choice=popup.querySelector('[data-value="'+id+'"]')?.closest('[role=option]');
    assert.ok(choice,'saved choice unavailable');
    choice.dispatchEvent(new w.MouseEvent('mouseup',{bubbles:true,cancelable:true,button:0}));
  }
  async function prepare(label,source,association,destination) {
    const control=await until(()=>{const b=rowButton(label,source);return b&&!b.disabled&&b;},
      'missing explicit native replacement in '+locale);
    control.click();
    const selects=await until(()=>{const s=dialog()?.querySelectorAll('button[aria-haspopup="listbox"]');return s?.length===2&&s;},
      'missing two explicit selectors');
    assert.ok(button(label,dialog()).disabled,'blank request can be sent');
    await choose(selects[0],association);
    assert.ok(button(label,dialog()).disabled,'missing destination can be sent');
    await choose(selects[1],destination,[source,legacy.Table]);
    return until(()=>{const b=button(label,dialog());return b&&!b.disabled&&b;},'valid saved choices did not enable replacement');
  }
  return {dom,d,proof,button,routeRow,selectVPC,prepare,xml};
}

async function assertReference(saved,before) {
  const response=await local('/_simulator/docs/openapi.json');
  assert.equal(response.status,200);
  const doc=await response.json();
  const ref=doc.paths['/'].post.requestBody.content['application/x-www-form-urlencoded'].examples;
  assert.ok(!ref[action],'the reference should not expose a writable preset');
  assert.ok(ref.DescribeRouteTables.description.includes(action));
  const preset=ref.DescribeRouteTables.value;
  assert.deepEqual(preset,{Action:'DescribeRouteTables',Version:'2016-11-15',MaxResults:100});
  const observation=await local('/',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded',
    'X-Simulator-Service':'ec2','X-Simulator-Region':'us-east-1'},body:new URLSearchParams(preset)});
  assert.equal(observation.status,200);
  const view=saved.xml(await observation.text());
  const [source,target]=tables.map(id=>[...view.querySelectorAll('routeTableSet > item')].find(
    r=>r.querySelector('routeTableId').textContent===id));
  const pair=(table,subnet)=>[...table.querySelectorAll('associationSet item')].find(
    r=>r.querySelector('subnetId').textContent===subnet)?.querySelector('routeTableAssociationId').textContent;
  assert.equal(pair(source,subnets[0]),undefined);
  assert.equal(pair(source,subnets[1]),ids[1]);
  assert.equal(pair(target,subnets[0]),before);
  assert.equal(pair(target,subnets[2]),ids[2]);
}

(async()=>{
  const [html,bundle]=await Promise.all(['','app.js'].map(asset=>local('/_simulator/console/'+asset).then(async r=>{
    assert.equal(r.status,200);
    return r.text();
  })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  const en=boot(html,bundle,'en');
  let confirmed;
  try {
    await en.selectVPC(vpc);
    const label='Change subnet route table';
    const submit=await en.prepare(label,tables[0],ids[0],tables[1]);
    assert.equal(en.proof.commands.length,0);
    en.proof.hold=true;
    submit.click();
    await until(()=>en.proof.release,'missing complete regional readback');
    assert.ok(!en.d.body.textContent.includes('Changed the saved subnet association:'));
    assert.equal(en.proof.pages.at(-1).MaxResults,'100');
    assert.deepEqual(en.proof.commands[0].fields,{Action:action,Version:'2016-11-15',AssociationId:ids[0],RouteTableId:tables[1]});
    confirmed=en.proof.commands[0].next;
    en.proof.release();
    await until(()=>en.d.body.textContent.includes('Changed the saved subnet association: '+confirmed),'no correct scoped success');
    assert.ok(!en.routeRow(label,tables[0]).textContent.includes(subnets[0]));
    assert.ok(en.routeRow(label,tables[1]).textContent.includes(subnets[0]));
    await assertReference(en,confirmed);
  } finally {
    en.proof.release?.();
    en.dom.window.close();
  }

  const jp=boot(html,bundle,'ja');
  let movedBack;
  try {
    await jp.selectVPC(vpc);
    const label='サブネットのルートテーブルを変更';
    const submit=await jp.prepare(label,tables[1],confirmed,tables[0]);
    jp.proof.omit=ids[2];
    submit.click();
    await until(()=>jp.d.body.textContent.includes('ルートテーブルの保存状態を確認できませんでした。'),
      'incomplete readback showed success');
    assert.ok(!jp.d.body.textContent.includes('サブネットの関連付けを変更しました:'));
    movedBack=jp.proof.commands[0].next;
    jp.button('ルートテーブルを更新').click();
    await until(()=>jp.routeRow(label,tables[0])?.textContent.includes(subnets[0])&&
      jp.routeRow(label,tables[1])?.textContent.includes(subnets[2]),'refresh lost the committed association or sibling');
    assert.equal(jp.proof.commands.length,1,'refresh repeated the mutation');
    await jp.selectVPC(legacy.Vpc);
    await until(()=>jp.d.body.textContent.includes(legacy.Table)&&jp.d.querySelector('input[placeholder="subnet-…"]'),
      'legacy read disappeared');
    assert.ok(!jp.d.querySelector('button[aria-label^="'+label+': "]'),'CloudFormation view offered native mutations');
  } finally {
    jp.dom.window.close();
  }
  console.log(JSON.stringify({confirmed,movedBack}));
})().catch(err=>{console.error(err);process.exitCode=1;});
