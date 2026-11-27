/* Scoped Batch acceptance in jsdom, not a browser/layout test.
 * Execute the authentic locally served Cloudscape bundle and relay only its
 * own authenticated, expected requests to the single owned loopback process.
 */
const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {isAbsolute,join} = require('node:path');

assert.equal(process.version,'v24.22.3');
const jsdom = process.env.JSDOM_MODULE;
assert.ok(isAbsolute(jsdom));
assert.equal(JSON.parse(readFileSync(join(jsdom,'package.json'),'utf8')).version,'30.0.1');
const {JSDOM,VirtualConsole} = require(jsdom);
const {port,cookie,hashes,family,mode,seedRows} = JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<=65535&&typeof cookie==='string'&&cookie&&!/[\r\n]/.test(cookie));
assert.ok(/^[A-Za-z0-9_-]{1,128}$/.test(family)&&['write','deny','reopen','leave'].includes(mode));
const origin = 'http://127.0.0.1:'+port, root = '/_simulator/console/';
const arn = rev => 'arn:aws:batch:us-east-1:000000000000:job-definition/'+family+':'+rev;
const register = '/v1/registerjobdefinition', describe = '/v1/describejobdefinitions', deregister = '/v1/deregisterjobdefinition';
const descriptor = '/_simulator/v1/catalog/aws', policies = '/v1/listschedulingpolicies';
const registration = {jobDefinitionName:family,type:'container',platformCapabilities:['EC2'],
  containerProperties:{image:'example.invalid/metadata:console',resourceRequirements:[{type:'VCPU',value:'2'},{type:'MEMORY',value:'1024'}]}};
const trace = [], errors = [], violations = [];
const wait = async (read, reason) => {
  for (const end=Date.now()+5500; Date.now()<end;) {
    const found=read();
    if (found) return found;
    await new Promise(resolve=>setTimeout(resolve,20));
  }
  assert.fail(reason+' | trace: '+JSON.stringify(trace)+' | jsdom: '+JSON.stringify(errors)+' | IO: '+JSON.stringify(violations));
};

async function asset(path) {
  const response=await fetch(origin+path,{headers:{Cookie:cookie},redirect:'error',signal:AbortSignal.timeout(5000)});
  assert.equal(response.status,200);
  const bytes=Buffer.from(await response.arrayBuffer()), text=bytes.toString('utf8');
  let original=bytes;
  if(path===root){
    const [nonce,...duplicates]=[...text.matchAll(/<meta name="editor-style-nonce" content="([A-Za-z0-9+/]{32})">/g)];
    assert.ok(nonce&&!duplicates.length&&response.headers.get('Content-Security-Policy')?.includes("'nonce-"+nonce[1]+"'"));
    original=text.replace(nonce[0],'');
  }
  assert.equal(createHash('sha256').update(original).digest('hex'),hashes[path],path);
  return text;
}

async function run(){
  const [html,bundle]=await Promise.all([asset(root),asset(root+'app.js')]);
  const virtualConsole=new VirtualConsole();
  virtualConsole.on('jsdomError',error=>errors.push(error.message));
  const dom=new JSDOM(html,{url:origin+root+'#/catalog',pretendToBeVisual:true,runScripts:'outside-only',virtualConsole});
  const w=dom.window,d=w.document;
  const one=(scope,query,test)=>[...scope.querySelectorAll(query)].find(test);
  const button=(scope,label)=>one(scope,'button',el=>!el.closest('[aria-hidden="true"],[hidden]')&&
    (el.getAttribute('aria-label')||el.textContent.trim())===label);
  const click=async(scope,label)=>(await wait(()=>{const next=button(scope,label);return next&&!next.disabled&&next;},
    'Missing enabled control: '+label)).click();
  const input=(element,value)=>{
    assert.ok(element&&!element.disabled);
    Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(element,value);
    element.dispatchEvent(new w.Event('input',{bubbles:true}));
  };
  const row=rev=>one(d,'tbody tr',el=>[...el.cells].some(cell=>cell.textContent.trim()===arn(rev)));
  const statuses = [1,2,3].map(arn);
  const rows=()=>[...d.querySelectorAll('tbody tr')].flatMap(r=>{
    const fields=[...r.cells].map(cell=>cell.textContent.trim());
    return statuses.filter(key=>fields.includes(key)).map(key=>[key,fields.find(x=>['ACTIVE','INACTIVE'].includes(x))]);
  });
  const showRows=async expected=>wait(()=>JSON.stringify(rows().sort())===JSON.stringify(expected.map(
    ([rev,status])=>[arn(rev),status]).sort()),'List did not show exact revisions and statuses');
  const seen=path=>trace.filter(event=>event.path===path);
  const exactReads=rev=>seen(describe).filter(call=>JSON.stringify(call.fields)===JSON.stringify({jobDefinitions:[arn(rev)]}));
  const detail=rev=>one(d,'h1',el=>el.textContent.trim()===arn(rev));
  const dialog=rev=>one(d,'[role="dialog"]',el=>el.textContent.includes(arn(rev))&&!el.closest('[hidden],[aria-hidden="true"]'));
  const text=()=>d.body.textContent;
  const savedStatus=kind=>one(d,'.catalog-key',el=>el.textContent.trim()===kind);
  const japanese=mode==='reopen';
  const deregisterLabel=japanese?'リビジョンの登録を解除':'Deregister revision';
  const reloadLabel=japanese?'リビジョンを更新':'Refresh revision';
  const deferred=()=>{let resolve;const promise=new Promise(ready=>{resolve=ready});return {promise,resolve};};
  const gates=Object.fromEntries([deregister,register].map(path=>[path,{delivery:deferred(),observed:deferred()}]));
  async function release(path){
    gates[path].delivery.resolve();
    await gates[path].observed.promise;
    await new Promise(resolve=>setImmediate(resolve));
  }
  try{
    if(japanese)w.localStorage.setItem('tenkacloud-simulator-locale','ja');
    Object.assign(w,{TextEncoder,TextDecoder,matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
      ResizeObserver:class{observe(){} unobserve(){} disconnect(){}},
      IntersectionObserver:class{observe(){} unobserve(){} disconnect(){}},scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.fetch=async(path,options={})=>{
      const method=options.method||'GET',headers=new Headers(options.headers);
      let fields;
      try{
        const get=['/_simulator/v1/capabilities',descriptor,...statuses.map(value=>'/v1/tags/'+encodeURIComponent(value))];
        const post=[policies,describe,...(['write','leave'].includes(mode)?[register,deregister]:mode==='deny'?[deregister]:[])];
        assert.ok(method==='GET'&&get.includes(path)||method==='POST'&&post.includes(path),'Unexpected '+method+' '+path);
        for(const key of ['cookie','authorization','x-simulator-token','x-amz-target'])assert.ok(!headers.has(key));
        fields=options.body?JSON.parse(options.body):{};
        if(!['/_simulator/v1/capabilities',descriptor].includes(path)){
          assert.equal(headers.get('x-simulator-service'),'batch');
          assert.equal(headers.get('x-simulator-region'),'us-east-1');
        }
        if(options.body)assert.equal(headers.get('content-type'),'application/json');
        if(path===register)assert.deepEqual(fields,registration);
        else if(path===deregister)assert.deepEqual(fields,{jobDefinition:arn(2)});
        else if(path===describe&&Object.keys(fields).length){
          assert.deepEqual(Object.keys(fields),['jobDefinitions']);
          assert.equal(fields.jobDefinitions.length,1);
          assert.ok(statuses.includes(fields.jobDefinitions[0]));
        }else assert.deepEqual(fields,{});
      }catch(error){violations.push(error.message);throw error;}
      headers.set('Cookie',cookie);
      const response=await fetch(origin+path,{...options,headers,redirect:'error',signal:AbortSignal.timeout(5000)});
      trace.push({path,method,status:response.status,fields});
      if(mode==='leave'&&gates[path]){
        // Only delay delivery of the genuine local response. Report when its
        // unchanged body is consumed so the test can drain the old handler.
        const read=response.text.bind(response);
        response.text=async()=>{try{return await read();}finally{gates[path].observed.resolve();}};
        await gates[path].delivery.promise;
      }
      return response;
    };
    for(const name of ['XMLHttpRequest','WebSocket','EventSource'])w[name]=function(){violations.push(name);throw Error('Unexpected IO')};
    w.eval(bundle);
    async function pickBatch(){
      const nextPolicyList=seen(policies).length+1;
      const picker=await wait(()=>one(d,'button',el=>el.textContent.includes('Account Management')),'Missing catalog picker');
      picker.dispatchEvent(new w.MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));
      input(await wait(()=>d.querySelector('input[role="combobox"]'),'Catalog dropdown did not open'),'AWS Batch');
      const option=await wait(()=>one(d,'[role="option"]',el=>el.textContent.trim().startsWith('AWS Batch')),'Missing AWS Batch');
      option.dispatchEvent(new w.MouseEvent('mouseup',{bubbles:true,button:0}));
      await wait(()=>seen(policies).length===nextPolicyList,'Missing native scheduling policy list');
      if(mode==='write'){
        await click(d,'Create scheduling policy');
        const generic=await wait(()=>d.querySelector('#catalog-field-name')?.closest('form'),'Unrelated generic form missing');
        assert.ok(generic.querySelector('#catalog-field-fairsharePolicy')&&button(generic,'Create')&&!button(d,deregisterLabel));
        await click(generic,'Cancel');
      }
      (await wait(()=>one(d,'[role="tab"]',el=>/^Job definitions$/i.test(el.textContent.trim())),'Missing Job Definitions tab')).click();
    }
    const navigationLock=()=>one(d,'.cloud-provider-choices button',el=>el.textContent.trim()==='AWS');
    async function leaveForHome(){
      await wait(()=>navigationLock()?.disabled,'Saving owner did not lock navigation');
      w.location.hash='#/home';
      await wait(()=>one(d,'h1',el=>el.textContent.trim()==='Console Home')&&navigationLock()?.disabled===false,
        'Saving owner did not unlock navigation after hash unmount');
    }
    async function reenterCatalog(){
      (await wait(()=>one(d,'.console-home a[href="#/catalog"]',el=>el.textContent.trim()==='Service catalog'),
        'Missing live home link back to the catalog')).click();
      await pickBatch();
    }
    async function fillRegistration(memory){
      await click(d,'Create job definition');
      const form=await wait(()=>d.querySelector('form input[id^="catalog-field-"]')?.closest('form'),'Missing registration form');
      const fields=[...form.querySelectorAll('input,textarea,select')].map(el=>el.id).sort();
      assert.deepEqual(fields,['catalog-field-image','catalog-field-jobDefinitionName','catalog-field-memoryMiB','catalog-field-vcpus']);
      for(const [key,value] of Object.entries({jobDefinitionName:family,image:registration.containerProperties.image,vcpus:'2',memoryMiB:memory}))
        input(form.querySelector('#catalog-field-'+key),value);
      return form;
    }
    await pickBatch();
    await showRows(seedRows);
    await wait(()=>button(d,deregisterLabel),'Missing bounded job definition action');
    assert.ok(!button(d,japanese?'削除':'Delete'),'No physical delete button should be present');

    if(mode==='write'){
      const form=await fillRegistration('3');
      await click(form,'Create');
      await wait(()=>text().includes('Enter a whole number from 4 to 2,147,483,647.'),'Missing field-level memory validation');
      assert.deepEqual(seen(register),[]);
      input(form.querySelector('#catalog-field-memoryMiB'),'1024');
      await click(form,'Create');
      await showRows([...seedRows,[3,'ACTIVE']]);
      assert.deepEqual(seen(register).map(({status})=>status),[200]);

      (await wait(()=>row(2)?.querySelector('input[type="radio"]'),'Missing exact revision selection')).click();
      await click(d,deregisterLabel);
      const cancel=await wait(()=>dialog(2),'Confirmation lacks exact revision ARN');
      assert.match(cancel.textContent,/Only this revision becomes INACTIVE/);
      await click(cancel,'Cancel');
      await wait(()=>!dialog(2),'Cancellation did not dismiss confirmation');
      assert.deepEqual(seen(deregister),[]);
    }

    const target=2;
    (await wait(()=>row(target)?.querySelector('a'),'Missing exact revision details link')).click();
    await wait(()=>detail(target)&&exactReads(target).length===1&&button(d,reloadLabel)&&!button(d,reloadLabel).disabled,'Missing actual exact detail read');
    assert.equal(exactReads(target)[0].status,200);
    if(mode==='write'||mode==='reopen'){
      await click(d,reloadLabel);
      await wait(()=>exactReads(target).length===2&&button(d,reloadLabel)&&!button(d,reloadLabel).disabled,'Missing exact refresh');
    }
    if(mode==='reopen'){
      assert.equal(d.documentElement.lang,'ja');
      assert.ok(savedStatus('INACTIVE'));
      assert.ok(button(d,deregisterLabel)?.disabled);
    }else{
      await click(d,deregisterLabel);
      await click(await wait(()=>dialog(target),'Missing exact revision confirmation'),deregisterLabel);
      if(mode==='leave'){
        await wait(()=>seen(deregister)[0]?.status===200,'Native deregistration did not reach the delayed reply');
        await leaveForHome();
        await reenterCatalog();
        await showRows([[1,'ACTIVE'],[2,'INACTIVE']]);
        const form=await fillRegistration('1024');
        await click(form,'Create');
        await wait(()=>seen(register)[0]?.status===200&&navigationLock()?.disabled,
          'New owner did not acquire the saving latch');
        await release(deregister);
        assert.ok(navigationLock()?.disabled,'Old reply cleared the newer owner');
        assert.equal(exactReads(target).length,1,'Old reply reloaded an unmounted detail');
        assert.ok(!text().includes('Revision deregistered.')&&!detail(target));
        await leaveForHome();
        await release(register);
        await reenterCatalog();
        await showRows([[1,'ACTIVE'],[2,'INACTIVE'],[3,'ACTIVE']]);
        assert.equal(exactReads(target).length,1);
        assert.ok(!text().includes('Resource created.')&&!detail(target));
        assert.deepEqual(seen(deregister).map(({status})=>status),[200]);
        assert.deepEqual(seen(register).map(({status})=>status),[200]);
      }else if(mode==='deny'){
        await wait(()=>text().includes('Request failed')&&/403|AccessDeniedException/.test(text()),'Refused request did not remain visible');
        assert.deepEqual(seen(deregister).map(({status})=>status),[403]);
        assert.equal(exactReads(target).length,1);
        assert.ok(savedStatus('ACTIVE')&&!savedStatus('INACTIVE'));
        assert.ok(!text().includes('Revision deregistered.'));
      }else{
        await wait(()=>text().includes('Revision deregistered. It remains visible as INACTIVE.')&&savedStatus('INACTIVE')&&
          exactReads(target).length===3&&button(d,reloadLabel)&&!button(d,reloadLabel).disabled,'Missing acknowledged exact inactive read-back');
        assert.deepEqual(seen(deregister).map(({status})=>status),[200]);
        assert.ok(button(d,deregisterLabel)?.disabled);
        (await wait(()=>one(d,'a',el=>el.textContent.trim()==='Job definitions'),'Missing Back to job definitions')).click();
        await showRows([[1,'ACTIVE'],[2,'INACTIVE'],[3,'ACTIVE']]);
      }
    }
    assert.deepEqual(violations,[]);
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({mode,trace,jsdom:'30.0.1',node:process.version,errors,violations}));
  }finally{w.close();}
}
run().catch(error=>{console.error(error);process.exitCode=1;});
