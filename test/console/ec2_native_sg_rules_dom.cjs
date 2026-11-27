const assert=require('node:assert/strict');
const {createHash,randomUUID}=require('node:crypto');
const {readFileSync}=require('node:fs');
const {JSDOM,VirtualConsole}=require(process.env.JSDOM_MODULE);
const {port,cookie,vpc,group,legacy,asset,tuples}=JSON.parse(readFileSync(0,'utf8'));
assert.ok(Number.isInteger(port)&&port>0&&port<65536);
const origin='http://127.0.0.1:'+port;
const allowed=new Set(['DescribeVpcs','DescribeSubnets','DescribeSecurityGroups','DescribeNetworkAcls','DescribeRouteTables',
  'DescribeInternetGateways','AuthorizeSecurityGroupIngress','AuthorizeSecurityGroupEgress','RevokeSecurityGroupIngress','RevokeSecurityGroupEgress']);
const ruleActions=[...allowed].slice(-4);
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(find,message) {
  for (const end=Date.now()+12000;Date.now()<end;) {
    const got=find();if(got)return got;
    await pause(16);
  }
  assert.fail(message);
}
async function local(path,init={}) {
  assert.ok(path.startsWith('/')&&!path.startsWith('//'));
  const headers=new Headers(init.headers);
  headers.set('Cookie',cookie);
  return fetch(origin+path,{...init,headers,redirect:'error'});
}

(async()=>{
  const [html,bundle]=await Promise.all(['','app.js'].map(path=>local('/_simulator/console/'+path).then(async result=>{
    assert.equal(result.status,200);return result.text();
  })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'),asset,'served exact candidate application');
  const dom=new JSDOM(html,{url:origin+'/_simulator/console/#/vpc',runScripts:'outside-only',
    pretendToBeVisual:true,virtualConsole:new VirtualConsole()});
  const w=dom.window,d=w.document;
  const writes=[];
  let waitForReadback=false,release;
  try {
    Object.assign(w,{TextEncoder,TextDecoder,matchMedia:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}),
      ResizeObserver:class{observe(){}unobserve(){}disconnect(){}},IntersectionObserver:class{observe(){}unobserve(){}disconnect(){}},scrollTo(){}});
    Object.defineProperty(w.crypto,'randomUUID',{value:randomUUID});
    w.HTMLElement.prototype.scrollIntoView=function(){};
    w.localStorage.setItem('tenkacloud-simulator-locale','en');
    w.fetch=async(path,init={})=>{
      assert.ok(path==='/'||path==='/_simulator/v1/capabilities');
      const body=init.body?.toString(), form=Object.fromEntries(new URLSearchParams(body));
      if(path==='/'){
        assert.ok(allowed.has(form.Action),form.Action);
        assert.equal(new Headers(init.headers).get('X-Simulator-Region'),'us-east-1');
      }
      const bridge=init.signal&&new AbortController();
      if(bridge){
        if(init.signal.aborted)bridge.abort();else init.signal.addEventListener('abort',()=>bridge.abort(),{once:true});
      }
      const reply=await local(path,{...init,body,signal:bridge?.signal});
      if(path==='/'&&ruleActions.includes(form.Action)){
        assert.equal(reply.status,200);
        assert.equal(form.GroupId,group);
        const xml=new w.DOMParser().parseFromString(await reply.clone().text(),'application/xml');
        assert.equal(xml.documentElement.localName,form.Action+'Response');
        assert.equal(xml.querySelector('return').textContent,'true');
        writes.push(form);
      }
      if(waitForReadback&&form.Action==='DescribeSecurityGroups'&&writes.length===1){
        assert.equal(form['Filter.1.Value.1'],vpc);
        waitForReadback=false;
        await new Promise(r=>{release=r;});
      }
      return reply;
    };
    w.eval(bundle);
    const shown=text=>d.body.textContent.includes(text);
    const button=(text,node=d)=>[...(node?.querySelectorAll('button')??[])].find(b=>b.textContent.trim()===text||b.getAttribute('aria-label')===text);
    const click=async(text,node=d)=>(await until(()=>{const b=button(text,node);return b&&!b.disabled&&b;},'missing '+text)).click();
    const select=async id=>(await until(()=>d.querySelector('input[type="radio"][aria-label="Select '+id+'"]'),'missing VPC selection')).click();
    const dialog=()=>d.querySelector('button[aria-label="Close security group dialog"]')?.closest('[role=dialog]');
    const fill=async(i,text)=>{
      const input=await until(()=>dialog()?.querySelectorAll('input[type=text]:not([disabled])')?.[i],'missing rule input '+i);
      const value=String(text);
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(input,value);
      input.dispatchEvent(new w.InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:value}));
      input.dispatchEvent(new w.Event('change',{bubbles:true}));
      await pause(20);
    };
    await select(legacy.Vpc);
    await until(()=>shown('Legacy rule readback')&&shown('TCP 443 · 198.51.100.0/24'),'saved CFN rule not rendered');
    assert.ok(!button('Add inbound rule')&&!button('Add outbound rule'));
    await select(vpc);
    await until(()=>shown(group)&&shown(tuples[0].cidr)&&shown(tuples[1].cidr),'SDK-origin native tuples not rendered');
    assert.ok(shown('saved rules do not affect traffic.'));
    const add=async(direct,rule,badFirst=false)=>{
      const action='Add '+direct+' rule';
      await click(action);
      await until(()=>dialog(),'missing rule modal');
      assert.ok(button(action,dialog()).disabled);
      await fill(0,rule.from);await fill(1,rule.to);
      if(badFirst){
        await fill(2,'0.0.0.0/0');
        assert.ok(button(action,dialog()).disabled,'outside-VPC CIDR was offered');
        assert.equal(writes.length,0);
      }
      await fill(2,rule.cidr);
      await click(action,dialog());
    };
    waitForReadback=true;
    await add('inbound',tuples[2],true);
    await until(()=>release,'real UI re-read not requested');
    assert.ok(!shown('Saved security group rule:'),'UI success before verified readback');
    assert.ok(button('Services').disabled,'global navigation not held during SG rule write/read');
    release();
    await until(()=>shown('Saved security group rule: '+group)&&shown(tuples[2].cidr),'inbound write not rendered');
    await add('outbound',tuples[3]);
    await until(()=>shown('Saved security group rule: '+group)&&shown(tuples[3].cidr),'outbound write not rendered');
    for (const [i,direction] of ['inbound','outbound'].entries()){
      const r=tuples[i], action='Remove '+direction+' rule';
      await click(action+' '+r.cidr+' '+r.from+'–'+r.to);
      await click(action,await until(()=>dialog(),'missing removal confirmation'));
      await until(()=>shown('Removed security group rule: '+group)&&!shown(r.cidr),'selected existing rule not removed');
      assert.ok(shown(tuples[2].cidr)&&shown(tuples[3].cidr));
    }
    assert.deepEqual(writes.map(x=>x.Action),ruleActions);
    for (const [i,idx] of [2,3,0,1].entries()){
      const r=tuples[idx];
      assert.deepEqual(writes[i],{Action:ruleActions[i],Version:'2016-11-15',GroupId:group,
        'IpPermissions.1.IpProtocol':'tcp','IpPermissions.1.FromPort':String(r.from),
        'IpPermissions.1.ToPort':String(r.to),'IpPermissions.1.IpRanges.1.CidrIp':r.cidr});
    }
    console.log('PASS served exact Cloudscape JSDOM: SDK-origin rules, native controls both directions, live receipt, held readback, no CFN controls.');
  } catch(err){
    console.error('Observed UI state:',d.body.textContent.slice(-1400));
    throw err;
  } finally {
    release?.();
    w.close();
  }
})().catch(err=>{console.error(err);process.exitCode=1;});
