'use strict';
// Opt-in Linux witness. Dependencies are supplied explicitly; this test
// installs nothing and imports its ephemeral CA into an isolated NSS HOME.
// SIMULATOR_BINARY=/abs/simulator PLAYWRIGHT_MODULE=/abs/playwright-core CHROMIUM_BINARY=/abs/chromium \
//   node test/e2e/local_sso_strict_browser.cjs
const assert=require('node:assert/strict');
const {spawn,execFileSync}=require('node:child_process');
const {mkdtemp,mkdir,readFile,rm,stat,access,readdir,writeFile}=require('node:fs/promises');
const {join,dirname,isAbsolute}=require('node:path');
const {tmpdir}=require('node:os');
const {setTimeout:sleep}=require('node:timers/promises');
for(const key of ['SIMULATOR_BINARY','PLAYWRIGHT_MODULE','CHROMIUM_BINARY']) {
  if(!process.env[key]||!isAbsolute(process.env[key])) throw Error('Provide the absolute path in '+key);
}
if(process.platform!=='linux') throw Error('The opt-in Chromium/NSS witness is Linux-only');
const {chromium}=require(process.env.PLAYWRIGHT_MODULE);

const bin=process.env.SIMULATOR_BINARY;
const has=async path=>{try{await access(path);return true;}catch(e){if(e.code==='ENOENT')return false;throw e;}};
const mode=async(path,m)=>assert.equal((await stat(path)).mode&0o777,m);
let checkpoint='initialization';
function start(argv,cwd,env){
  const child=spawn(bin,argv,{cwd,env,stdio:['ignore','pipe','pipe']});
  const info={child,out:'',done:new Promise((ok,bad)=>{child.once('error',bad);child.once('close',(code,signal)=>ok({code,signal}));})};
  child.stdout.on('data',chunk=>{info.out=(info.out+chunk).slice(-4096);});
  // Do not publish stderr, URLs with query values, SAML payloads, or credentials.
  const bounded=/^(signed local Cognito (?:CreateUserPool|CreateIdentityProvider|CreateUserPoolClient) failed|local Cognito SAML service provider unavailable|invalid local Cognito (?:pool|app client)|signed Cognito SP metadata could not be confirmed|local Entra metadata could not be confirmed|could not initialize local (?:Cognito|Entra)|cannot bind isolated local SSO listeners|local sample browser page unavailable|cannot allocate private SSO state|could not close a local SSO listener|could not close local SSO state|could not remove private local SSO state)$/;
  child.stderr.on('data',chunk=>{
    for(const line of String(chunk).split('\n'))if(bounded.test(line.trim()))info.problem=line.trim();
  });
  return info;
}
async function waitFor(info,rx){
  for(let i=0;i<120;i++){
    const match=rx.exec(info.out);
    if(match)return match;
    if(info.child.exitCode!==null||info.child.signalCode!==null)break;
    await sleep(90);
  }
  if(info.problem)console.log('launcher stop reason: '+info.problem);
  assert.fail('readiness not reached');
}
async function stop(info){
  if(!info)return;
  if(info.child.exitCode===null&&info.child.signalCode===null)info.child.kill('SIGTERM');
  const timeout=setTimeout(()=>info.child.kill('SIGKILL'),12000);
  try{assert.deepEqual(await info.done,{code:0,signal:null});}finally{clearTimeout(timeout);}
}
const loopbackTLS=s=>/^https:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}\/$/.test(s);
async function main(){
  const root=await mkdtemp(join(tmpdir(),'local-sso-qualification-'));
  let run,browser;
  try{
    const home=join(root,'home'),temp=join(root,'tmp'),nss=join(home,'.pki','nssdb');
    await mkdir(nss,{recursive:true,mode:0o700});await mkdir(temp,{mode:0o700});
    const env={PATH:process.env.PATH,HOME:home,TMPDIR:temp,XDG_CONFIG_HOME:join(home,'config'),XDG_CACHE_HOME:join(home,'cache')};
    checkpoint='deterministic failed CLI start, before scratch or listener allocation';
    const blockedTemp=join(root,'not-a-directory');
    await writeFile(blockedTemp,'',{mode:0o600,flag:'wx'});
    run=start(['local-sso'],root,{...env,TMPDIR:blockedTemp});
    const failed=run;
    let deadline;
    try {
      const ended=await Promise.race([failed.done,new Promise((_,reject)=>{
        deadline=setTimeout(()=>reject(Error('failed startup did not terminate')),6500);
      })]);
      run=null;
      assert.deepEqual(ended,{code:1,signal:null});
      assert.equal(failed.problem,'cannot allocate private SSO state');
      assert.ok(!failed.out.includes('Local SSO sample ready'));
      assert.deepEqual(await readdir(temp),[]);
      assert.ok((await stat(blockedTemp)).isFile());
    } finally { clearTimeout(deadline); }

    checkpoint='unchanged standard CLI';
    assert.match(execFileSync(bin,['--help'],{cwd:root,env,encoding:'utf8'}),/--?listen/);
    assert.match(execFileSync(bin,['local-sso','--help'],{cwd:root,env,encoding:'utf8'}),/Usage: .*local-sso/);
    assert.deepEqual(await readdir(temp),[]);
    run=start(['--listen','127.0.0.1:0','--state',':memory:','--token-file',join(root,'ordinary-token')],root,env);
    const ordinary=(await waitFor(run,/http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}/))[0];
    const unauthed=await fetch(ordinary+'/_simulator/v1/capabilities',{redirect:'error'});
    assert.equal(unauthed.status,401);
    await stop(run);run=null;

    checkpoint='single command, private modes, and untrusted certificate refusal';
    run=start(['local-sso'],root,env);
    const match=await waitFor(run,/Open: (https:\/\/127\.0\.0\.1:\d+\/)\nTrust this per-run CA only in an isolated browser profile: ([^\n]+)\nOne-shot Entra code file \(private\): ([^\n]+)\nStop with Ctrl\+C;/);
    const [,url,ca,codeFile]=match;
    assert.ok(loopbackTLS(url));assert.equal(dirname(ca),dirname(codeFile));assert.equal(dirname(dirname(ca)),temp);
    await mode(dirname(ca),0o700);await mode(ca,0o600);await mode(codeFile,0o600);
    const code=(await readFile(codeFile,'utf8')).trim();
    assert.match(code,/^[A-Za-z0-9_-]{43}$/);
    assert.ok(!run.out.includes(code));
    await assert.rejects(fetch(url,{redirect:'error'}),e=>e?.cause?.code==='UNABLE_TO_VERIFY_LEAF_SIGNATURE'||e?.cause?.code==='SELF_SIGNED_CERT_IN_CHAIN');

    checkpoint='isolated NSS CA import and real Chromium SSO';
    execFileSync('certutil',['-N','--empty-password','-d','sql:'+nss],{env,stdio:'ignore'});
    execFileSync('certutil',['-A','-d','sql:'+nss,'-n','Ephemeral Local SSO CA','-t','C,,','-i',ca],{env,stdio:'ignore'});
    browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_BINARY,env,
      args:['--disable-background-networking','--disable-component-update','--disk-cache-size=1']});
    const ctx=await browser.newContext({ignoreHTTPSErrors:false,serviceWorkers:'block'});
    const page=await ctx.newPage();
    page.setDefaultTimeout(10000);
    await ctx.route('**/*',route=>{
      const u=new URL(route.request().url());
      return u.protocol==='https:'&&u.hostname==='127.0.0.1'&&u.port ? route.continue() : route.abort();
    });
    const paths=[];
    page.on('response',res=>{const u=new URL(res.url()); if(u.protocol==='https:'&&u.hostname==='127.0.0.1')
      paths.push({port:u.port,path:u.pathname,status:res.status()});});
    checkpoint='trusted sample page';
    await page.goto(url,{waitUntil:'domcontentloaded'});
    checkpoint='signed redirect and local Entra prompt';
    await page.getByRole('link',{name:'Sign in with local Entra',exact:true}).click();
    await page.getByLabel('One-time synthetic login code').fill(code);
    checkpoint='local Entra reply, Cognito ACS and RP JWT callback';
    await page.getByRole('button',{name:'Sign in synthetic identity',exact:true}).click();
    await page.getByRole('heading',{name:'Local sign-in verified',exact:true}).waitFor();
    assert.ok(paths.some(row=>row.path.endsWith('/oauth2/authorize')));
    assert.ok(paths.some(row=>row.path.endsWith('/saml2/idpresponse')));
    assert.ok(paths.some(row=>row.path==='/callback'&&row.status===200));
    assert.equal(new Set(paths.map(row=>row.port)).size,3);
    await ctx.close();await browser.close();browser=null;

    checkpoint='cleanup and released listeners';
    await stop(run);run=null;
    assert.equal(await has(dirname(ca)),false);
    for(const port of new Set(paths.map(row=>row.port))){
      await assert.rejects(fetch('https://127.0.0.1:'+port+'/',{redirect:'error'}),e=>e?.cause?.code==='ECONNREFUSED');
    }
    console.log('PASS finite failed CLI start, no fixture files, normal CLI auth, local-sso private modes, default trust refusal, isolated NSS CA, real Chromium Cognito+Entra callback, no secret stdout, SIGTERM cleanup and 3 ports released.');
  } finally {
    await browser?.close();
    try{await stop(run);}finally{await rm(root,{recursive:true,force:true});}
  }
}
main().catch(e=>{console.error('STOP at '+checkpoint+': '+e.name);process.exitCode=1;});
