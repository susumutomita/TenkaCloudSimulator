const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {readFileSync}=require('node:fs');
const {createHash}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const assert=require('node:assert/strict');

const {endpoint,token,cookie,bucket,key,upload,asset,driver}=JSON.parse(readFileSync(0,'utf8'));
assert.equal(new URL(endpoint).hostname,'127.0.0.1');
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(find,message) {
  for (const end=Date.now()+8000;Date.now()<end;) {
    const value=find();if(value)return value;
    await sleep(20);
  }
  assert.fail(message);
}
function network(path,init={}) {
  const url=new URL(path,endpoint);
  assert.equal(url.origin,endpoint);
  const headers=new Headers(init.headers);
  headers.set('Cookie',cookie);
  return fetch(url,{...init,headers,signal:undefined,redirect:'error'});
}
const etag=bytes=>'"'+createHash('md5').update(bytes).digest('hex')+'"';

(async()=>{
  const [html,bundle]=await Promise.all(['','app.js'].map(tail=>network('/_simulator/console/'+tail).then(async r=>{
    assert.equal(r.status,200);
    return r.text();
  })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset);
  const dom=new JSDOM(html,{url:endpoint+'/_simulator/console/#/s3',runScripts:'outside-only',
    pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document;
  const receipts=[];
  let mode='hold',release,readbacks=0,replaced;
  try {
    Object.assign(w,{TextEncoder,TextDecoder,File,Blob,
      matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
      ResizeObserver:class {observe(){} unobserve(){} disconnect(){}},
      IntersectionObserver:class {observe(){} unobserve(){} disconnect(){}},scrollTo(){}});
    w.localStorage.setItem('tenkacloud-simulator-locale','en');
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.fetch=async(path,init={})=>{
      const response=await network(path,init);
      const url=new URL(path,endpoint);
      if (url.searchParams.get('uploadId')!==upload) return response;
      if (init.method==='PUT') {
        assert.equal(decodeURIComponent(url.pathname),'/' + bucket + '/' + key);
        assert.equal(url.searchParams.get('partNumber'),'1');
        assert.equal(response.status,200);
        receipts.push(response.headers.get('etag'));
        if(mode==='replace') {
          const peer=spawnSync('python3',['-B',driver,'--replace-fixture-part'],
            {input:JSON.stringify({endpoint,token,bucket,key,upload}),encoding:'utf8'});
          assert.equal(peer.status,0,peer.stderr);
          replaced=peer.stdout.trim();
          assert.equal(replaced,etag('sdk-new'));
        }
      } else if (url.searchParams.has('part-number-marker')&&receipts.length) {
        assert.equal(response.status,200);
        readbacks++;
        if (mode==='hold') {
          mode='plain';
          await new Promise(resolve=>{release=resolve;});
        }
      }
      return response;
    };
    w.eval(bundle);
    const button=label=>[...d.querySelectorAll('button')].find(b=>b.textContent.trim()===label);
    const has=text=>d.body.textContent.includes(text);
    const row=()=>d.querySelector('input[type=radio][aria-label="Select upload '+key+'"]');
    const pick=async bytes=>{
      const part=await until(()=>d.querySelector('input[type=file][aria-label="Part file"]'),'part file input missing');
      Object.defineProperty(part,'files',{configurable:true,value:[new File([bytes],'part.bin',{type:'application/octet-stream'})]});
      part.dispatchEvent(new w.Event('change',{bubbles:true}));
      (await until(()=>{const b=button('Save part');return b&&!b.disabled&&b;},'Save part not ready')).click();
    };

    (await until(()=>[...d.querySelectorAll('a')].find(a=>a.textContent.trim()===bucket),'bucket missing')).click();
    (await until(()=>[...d.querySelectorAll('[role=tab]')].find(t=>t.textContent.trim()==='Management'),
      'management tab missing')).click();
    (await until(row,'upload missing')).click();
    await pick('initial');
    await until(()=>release,'no held real ListParts readback');
    assert.deepEqual(receipts,[etag('initial')]);
    assert.ok(!has('Part saved.'),'acknowledgement precedes ListParts');
    release();
    await until(()=>has('Part saved.')&&[...d.querySelectorAll('tr')].some(r=>r.textContent.includes(etag('initial'))),
      'same-state part was not confirmed');

    mode='replace';
    await pick('browser');
    await until(()=>has('Could not confirm this exact saved part.'),'old browser ETag was presented as confirmed');
    assert.deepEqual(receipts,[etag('initial'),etag('browser')]);
    assert.ok(!has('Part saved.')&&!has(etag('browser')));
    assert.ok([...d.querySelectorAll('tr')].some(r=>r.textContent.includes(replaced)&&r.textContent.includes('7 B')));
    button('Refresh uploads').click();
    (await until(()=>{const r=row();return r&&!r.checked&&!button('Save part')&&r;},'fresh upload not selectable')).click();
    await until(()=>[...d.querySelectorAll('tr')].some(r=>r.textContent.includes(replaced)),'real refresh lost saved SDK result');
    assert.equal(receipts.length,2,'refresh replayed the old write');
    assert.ok(readbacks>=3&&!has('Part saved.'));
  }finally{
    release?.();
    dom.window.close();
  }
})().catch(e=>{console.error(e);process.exitCode=1});
