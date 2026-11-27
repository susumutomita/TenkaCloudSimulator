'use strict';
const assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {port,cookie,viewer,vpc,main,custom,withStatic,implicit,explicit,asset}=JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
assert.equal(JSON.parse(readFileSync(resolve(process.env.JSDOM_MODULE,'package.json'))).version,'30.0.1');
const origin='http://127.0.0.1:'+port,path='/_simulator/console/';
const errors=[];
async function wait(read,why){
  for(const end=Date.now()+10000;Date.now()<end;){const found=read();if(found)return found;await new Promise(r=>setTimeout(r,20));}
  assert.fail(why);
}
async function served(tail){const r=await fetch(origin+path+tail,{headers:{Cookie:cookie},redirect:'error'});assert.equal(r.status,200);return r.text();}
async function inspect(html,bundle,session,denied){
  const log=new VirtualConsole();log.on('jsdomError',error=>errors.push(error.message));
  const dom=new JSDOM(html,{url:origin+path+'#/vpc',runScripts:'outside-only',pretendToBeVisual:true,virtualConsole:log});
  const w=dom.window,d=w.document;
  const calls=[];
  try{
    Object.assign(w,{TextEncoder,TextDecoder,ResizeObserver:class{observe(){} unobserve(){} disconnect(){}},
      IntersectionObserver:class{observe(){} unobserve(){} disconnect(){}},
      matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.localStorage.setItem('tenkacloud-simulator-locale','en');
    w.fetch=async(url,options={})=>{
      assert.ok(url==='/'||url==='/_simulator/v1/capabilities');
      const headers=new Headers(options.headers),body=options.body?.toString();
      for(const name of ['cookie','authorization','x-simulator-token'])assert.ok(!headers.has(name));
      let action='capabilities';
      if(url==='/'){
        const fields=new URLSearchParams(body);
        action=fields.get('Action');
        assert.equal(headers.get('X-Simulator-Region'),'us-east-1');
        assert.equal(fields.get('Version'),'2016-11-15');
        assert.match(action,/^Describe\w+$/,'the maintenance Console probe is read-only');
      }
      headers.set('Cookie',session);
      const response=await fetch(origin+url,{...options,headers,body,signal:undefined,redirect:'error'});
      calls.push([action,response.status]);
      return response;
    };
    w.eval(bundle);
    const selector='input[type=radio][aria-label="Select '+vpc+'"]';
    (await wait(()=>d.querySelector(selector),'saved legacy VPC selection missing')).click();
    await wait(()=>d.querySelector(selector)?.checked,'exact legacy VPC selection did not settle');
    const row=id=>d.querySelector('button[aria-label="Delete route table: '+id+'"]')?.closest('tr');
    await wait(()=>row(main)&&row(custom)&&row(withStatic),'main or old custom route tables omitted');
    const effective='Subnets using the main table implicitly';
    function col(id,title){
      const tr=row(id),i=[...tr.closest('table').querySelectorAll('thead th')].findIndex(th=>th.textContent.trim()===title);
      assert.ok(i>=0);return tr.cells[i].textContent.trim();
    }
    await wait(()=>denied?col(main,effective).includes('ec2:DescribeSubnets is denied.'):
      col(main,effective)===implicit,'implicit fallback without exact subnet evidence');
    assert.ok(row(main).textContent.includes(' · Main'));
    const protect=row(main).querySelector('button[aria-label^="Delete route table:"]');
    assert.ok(protect.disabled||protect.getAttribute('aria-disabled')==='true');
    for(const id of [main,custom,withStatic])assert.match(col(id,'IPv4 routes'),/10\.190\.0\.0\/16 → local · Read-only/);
    assert.match(col(withStatic,'IPv4 routes'),/0\.0\.0\.0\/0 → igw-.+198\.51\.100\.0\/24 → igw-/);
    assert.equal(col(custom,'Explicit subnet associations'),explicit);
    assert.equal(col(custom,effective),'—');
    if(denied)assert.ok(!col(main,effective).includes(implicit));
  }catch(error){console.error('Synthetic read-only DOM diagnostics:',calls,d.body.textContent.slice(-1400));throw error;}
  finally{w.close();}
}
(async()=>{
  const [html,bundle]=await Promise.all([served(''),served('app.js')]);
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  await inspect(html,bundle,cookie,false);
  await inspect(html,bundle,viewer,true);
  assert.deepEqual(errors,[]);
  console.log('PASS exact served Cloudscape in jsdom: upgraded main and two preserved custom rows; real scoped IAM does not invent implicit subnets. Read-only.');
})().catch(error=>{console.error(error);process.exitCode=1;});
