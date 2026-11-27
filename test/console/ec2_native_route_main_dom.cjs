'use strict';
const assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {port,cookie,viewer,vpc,main,custom,implicit,explicit,association,asset,source}=JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
assert.equal(JSON.parse(readFileSync(resolve(process.env.JSDOM_MODULE,'package.json'))).version,'30.0.1');
const origin='http://127.0.0.1:'+port,root='/_simulator/console/';
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const disabled=button=>button.disabled||button.getAttribute('aria-disabled')==='true';
async function until(find,message){
  for(const end=Date.now()+10000;Date.now()<end;){const found=find();if(found)return found;await pause(20);}
  assert.fail(message);
}
async function served(path){const r=await fetch(origin+root+path,{headers:{Cookie:cookie},redirect:'error'});assert.equal(r.status,200);return r.text();}

async function run(){
  const html=await served('');
  let bundle;
  if(source){
    // Development proof only. Build actual console sources in memory with the
    // pinned dependencies; the served-asset receipt below remains distinct.
    const dir=resolve(__dirname,'../../tools/console');
    const {build}=require(resolve(dir,'node_modules/esbuild'));
    const built=await build({absWorkingDir:dir,entryPoints:['src/main.jsx'],bundle:true,write:false,minify:true,
      platform:'browser',format:'iife',target:['es2022'],loader:{'.css':'empty'},define:{'process.env.NODE_ENV':'"production"'}});
    assert.equal(built.outputFiles.length,1);
    bundle=built.outputFiles[0].text;
  }else{
    bundle=await served('app.js');assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  }
  const repl='ReplaceRouteTableAssociation', writes=[],errors=[];
  function boot(session,hold){
    const vc=new VirtualConsole();vc.on('jsdomError',error=>errors.push(error.message));
    const dom=new JSDOM(html,{url:origin+root+'#/vpc',runScripts:'outside-only',pretendToBeVisual:true,virtualConsole:vc});
    const w=dom.window,d=w.document;
    let release, waiting=false, held=false;
    Object.assign(w,{TextEncoder,TextDecoder,
      ResizeObserver:class{observe(){} unobserve(){} disconnect(){}},
      IntersectionObserver:class{observe(){} unobserve(){} disconnect(){}},
      matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.localStorage.setItem('tenkacloud-simulator-locale','en');
    w.fetch=async(path,options={})=>{
      assert.ok(path==='/'||path==='/_simulator/v1/capabilities');
      const headers=new Headers(options.headers),body=options.body?.toString(),fields=Object.fromEntries(new URLSearchParams(body));
      for(const name of ['cookie','authorization','x-simulator-token'])assert.ok(!headers.has(name));
      headers.set('Cookie',session);
      const response=await fetch(origin+path,{...options,headers,body,signal:undefined,redirect:'error'});
      if(path==='/'){
        assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
        assert.equal(fields.Version,'2016-11-15');
        if(fields.Action===repl){
          assert.ok(hold);assert.equal(response.status,200);assert.ok(!waiting);
          writes.push(fields);waiting=true;
        }else{
          assert.ok(fields.Action.startsWith('Describe'),'unexpected native write '+fields.Action);
          if(waiting&&!held&&fields.Action==='DescribeRouteTables'){
            assert.equal(response.status,200);assert.equal(fields.MaxResults,'100');
            held=true;await new Promise(resolve=>{release=resolve;});
          }
        }
      }
      return response;
    };
    w.eval(bundle);
    const button=(label,scope=d)=>[...scope.querySelectorAll('button')].find(b=>b.textContent.trim()===label);
    const rowButton=(name,id)=>d.querySelector('button[aria-label="'+name+': '+id+'"]');
    const row=id=>rowButton('Delete route table',id)?.closest('tr');
    const modal=()=>d.querySelector('button[aria-label="Close route table dialog"]')?.closest('[role=dialog]');
    const column=(id,title)=>{
      const cell=row(id),head=[...cell.closest('table').querySelectorAll('thead th')].findIndex(th=>th.textContent.trim()===title);
      assert.ok(head>=0,'missing '+title);return cell.cells[head].textContent.trim();
    };
    async function choose(select,id,excluded){
      select.dispatchEvent(new w.MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));
      const list=await until(()=>d.getElementById(select.getAttribute('aria-controls'))?.querySelector('[role=option]')?.parentElement,'missing choices');
      assert.ok(![...list.querySelectorAll('[data-value]')].some(el=>excluded.includes(el.dataset.value)));
      const item=list.querySelector('[data-value="'+id+'"]')?.closest('[role=option]');
      assert.ok(item,'missing exact verified choice');item.dispatchEvent(new w.MouseEvent('mouseup',{bubbles:true,cancelable:true,button:0}));
    }
    async function open(){
      const selector='input[type=radio],input[type=checkbox]';
      let select;
      try {
        select=await until(()=>[...d.querySelectorAll('tr')].find(r=>r.textContent.includes(vpc)&&r.querySelector(selector))?.
          querySelector(selector),'missing real VPC row');
      } catch(error) {
        error.message+=': '+d.body.textContent.slice(-700)+' | '+errors.join(', ');throw error;
      }
      select.click();await until(()=>row(main)&&row(custom),'missing main/custom route tables');
    }
    return {dom,d,button,row,rowButton,modal,column,choose,open,held:()=>!!release,release:()=>release?.()};
  }

  const ui=boot(cookie,true);
  try{
    await ui.open();
    assert.ok(ui.d.body.textContent.includes('select a resource to use the available subnet, route table or gateway controls.'));
    assert.ok(!ui.d.body.textContent.includes('Native route and gateway attachment edits are unavailable.'));
    const effective='Subnets using the main table implicitly',explicitColumn='Explicit subnet associations';
    await until(()=>ui.column(main,effective)===implicit,'no confirmed implicit subnet');
    assert.ok(ui.row(main).textContent.includes(' · Main'));
    for(const id of [main,custom])assert.match(ui.column(id,'IPv4 routes'),/10\.190\.0\.0\/16 → local · Read-only/);
    const deleteMain=ui.rowButton('Delete route table',main);
    assert.ok(disabled(deleteMain));deleteMain.click();assert.ok(!ui.modal());
    assert.ok(disabled(ui.rowButton('Remove IPv4 route',main))&&disabled(ui.rowButton('Remove IPv4 route',custom)));
    assert.ok(!disabled(ui.rowButton('Add IPv4 route',main)),'local consumed static-route capacity');
    assert.ok(disabled(ui.rowButton('Disassociate subnet',main)));
    assert.equal(ui.column(custom,explicitColumn),explicit);assert.equal(ui.column(custom,effective),'—');
    ui.rowButton('Change subnet route table',custom).click();
    const selectors=await until(()=>{const s=ui.modal()?.querySelectorAll('button[aria-haspopup=listbox]');return s?.length===2&&s;},'missing replacement selectors');
    await ui.choose(selectors[0],association,[implicit]);await ui.choose(selectors[1],main,[custom]);
    (await until(()=>{const b=ui.button('Change subnet route table',ui.modal());return b&&!disabled(b)&&b;},'cannot submit verified action')).click();
    await until(ui.held,'missing real post-write regional acknowledgement');
    assert.ok(!ui.d.body.textContent.includes('Changed the saved subnet association:'));
    ui.release();
    await until(()=>ui.d.body.textContent.includes('Changed the saved subnet association:')&&ui.column(main,effective)===implicit,
      'verified replacement or fallback missing');
    assert.equal(ui.column(main,explicitColumn),explicit);
    assert.equal(ui.column(custom,explicitColumn),'None');
    assert.ok(!disabled(ui.rowButton('Delete route table',custom)),'local prevented deletion of a now-detached custom table');
    assert.deepEqual(writes,[{Action:repl,Version:'2016-11-15',AssociationId:association,RouteTableId:main}]);
  }finally{ui.release();ui.dom.window.close();}

  const limited=boot(viewer,false);
  try{
    await limited.open();
    await until(()=>limited.column(main,'Subnets using the main table implicitly').includes('ec2:DescribeSubnets is denied.'),
      'subnet policy refusal became an empty success');
    assert.equal(limited.column(main,'Explicit subnet associations'),explicit);
    assert.ok(!limited.column(main,'Subnets using the main table implicitly').includes(implicit));
    assert.equal(writes.length,1);
  }finally{limited.dom.window.close();}
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({mode:source?'in-memory source':'exact served asset',oneAcknowledgedWrite:true,implicitNotWire:true}));
}
run().catch(error=>{console.error(error);process.exitCode=1;});
