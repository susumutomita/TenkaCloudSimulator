/** Real platform integration, development only. Never contacts AWS or evaluates catalog scripts. */
import { strict as assert } from 'node:assert';
import { mkdtempSync, readFileSync, rmSync, existsSync, mkdirSync, copyFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { spawn } from 'node:child_process';
import { Database } from 'bun:sqlite';
const platform = process.env.TENKACLOUD_ROOT;
const catalog = process.env.CHALLENGE_ROOT;
assert(platform && catalog, 'Set TENKACLOUD_ROOT and CHALLENGE_ROOT to reviewed local checkouts.');
const load = (path: string) => import(join(platform!, path));
const { CloudFormationEngine } = await load('scripts/local-host/cloudformation-engine.ts');
const { HostingService } = await load('scripts/local-host/service.ts');
const { HostStore } = await load('scripts/local-host/store.ts');
const { downloadCatalog } = await load('scripts/problem-pack/downloaded-catalog.ts');
const { startHttpHost } = await load('scripts/local-host/http.ts');
const { CloudFormationClient } = await import(join(platform!, 'node_modules/@aws-sdk/client-cloudformation'));
const { SSMClient, GetParameterCommand } = await import(join(platform!, 'node_modules/@aws-sdk/client-ssm'));
const directory = mkdtempSync(join(tmpdir(), 'simulator-platform-'));
const processes: ReturnType<typeof spawn>[] = [];
const clients: any[] = [];
let store: any;
let adminHttp: any, participantHttp: any;
try {
  mkdirSync(join(directory,'assets'));
  copyFileSync(resolve('test/platform/portal/host.html'),join(directory,'host.html'));
  copyFileSync(resolve('test/platform/portal/practice.js'),join(directory,'assets/practice.js'));
  // The platform's acquisition validates, hashes and seals its own catalog snapshot.
  downloadCatalog(directory, {schemaVersion: 1, sources: [{path: resolve(catalog!)}]});
  store = new HostStore(new Database(join(directory, 'host.sqlite')));
  const accounts = ['111111111111', '222222222222'];
  const runtimes = new Map<string, {endpoint: string, token: string}>();
  for (const account of accounts) {
    const tokenFile = join(directory, `${account}.token`);
    const child = spawn(resolve('dist/tenkacloud-simulator'), ['--listen', '127.0.0.1:0', '--state', join(directory, `${account}.sqlite`), '--token-file', tokenFile], {stdio:['ignore','pipe','pipe']});
    processes.push(child);
    let output = '', error = '';
    child.stdout!.on('data', data => {output += data});
    child.stderr!.on('data', data => {error += data});
    let endpoint: string | undefined;
    for (let attempt=0; attempt<200; attempt++) {
      endpoint = output.match(/Console (http:\/\/127\.0\.0\.1:\d+)/)?.[1];
      if (endpoint && existsSync(tokenFile)) break;
      if (child.exitCode !== null) throw new Error(`Simulator exited: ${error}`);
      await Bun.sleep(25);
    }
    assert(endpoint, 'Simulator did not start');
    runtimes.set(account, {endpoint, token: readFileSync(tokenFile, 'utf8').trim()});
  }
  for (const account of accounts) {
    const runtime=runtimes.get(account)!;
    const response=await fetch(runtime.endpoint+'/',{method:'POST',headers:{'X-Simulator-Token':runtime.token,'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'AmazonSSM.PutParameter'},body:JSON.stringify({Name:'/isolation/probe',Value:account,Type:'String'})});
    assert.equal(response.status,200);
  }
  for (const account of accounts) {
    const runtime=runtimes.get(account)!;
    const response=await fetch(runtime.endpoint+'/',{method:'POST',headers:{'X-Simulator-Token':runtime.token,'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'AmazonSSM.GetParameter'},body:JSON.stringify({Name:'/isolation/probe'})});
    assert.equal((await response.json()).Parameter.Value,account,'Same-name state crossed team databases');
  }
  // Explicit local routing seam, NOT an STS implementation. Synthetic credentials
  // only select a private process; native AWS federation/console links are unavailable.
  const sts = {send: async (command: any) => {
    const account = command.input.RoleArn?.split(':')[4];
    assert(runtimes.has(account));
    return {Credentials:{AccessKeyId:`TCSIM${account}`, SecretAccessKey:'local-routing-only', SessionToken:'local-routing-only'}};
  }};
  const engine = new CloudFormationEngine(directory, {
    region:'ap-northeast-1', externalId:'local-host-external-id-2026',
    operatorAccountId: async () => '000000000000', sts,
    cloudFormation: (credentials: any, region: string) => ({send: async (command: any) => {
      const account = (await credentials()).accessKeyId.slice(5);
      const runtime = runtimes.get(account)!;
      const client = new CloudFormationClient({region, endpoint:runtime.endpoint, maxAttempts:1, credentials:{accessKeyId:'TCSIMHOST',secretAccessKey:'dummy'}});
      clients.push(client);
      client.middlewareStack.add((next: any) => (args: any) => {args.request.headers['x-simulator-token']=runtime.token;return next(args)}, {step:'build', name:'localToken'});
      return client.send(command);
    }}),
    team:(job: any)=>store.team(job.teamId), sleep: async()=>{}, pollIntervalMs:0, timeoutMs:10000,
  });
  // Restrict the selectable catalog to the verified subset. Never offer the battle.
  const supported = engine.catalog().filter((problem: any)=>problem.problemId==='hello-world');
  assert.equal(supported.length, 1);
  engine.catalog = () => supported;
  engine.participantAwsAccess = async () => {throw new Error('Native AWS access is unavailable in local simulation')};
  const hostKey = 'disposable-local-host-bootstrap-key';
  const service = new HostingService(store, engine, hostKey);
  service.accountConnection = {region:'ap-northeast-1',operatorAccountId:'000000000000',externalId:'local-host-external-id-2026',verify:async(account: string)=>assert(runtimes.has(account))};
  adminHttp = await startHttpHost({kind:'admin',hostname:'127.0.0.1',port:0,staticRoot:directory,service});
  participantHttp = await startHttpHost({kind:'participant',hostname:'127.0.0.1',port:0,staticRoot:directory,service});
  async function api(kind: string, method: string, path: string, body: any, token='') {
    const origin = kind==='admin'?adminHttp.origin:participantHttp.origin;
    const response = await fetch(`${origin}/api${path}`, {method, headers:{authorization:`Bearer ${token}`,'content-type':'application/json'}, ...(body===undefined?{}:{body:JSON.stringify(body)})});
    const value = await response.json();
    assert(response.ok, `${method} ${path}: ${response.status} ${JSON.stringify(value)}`);
    return value;
  }
  const bootstrap = await api('admin','POST','/host/bootstrap',{key:hostKey,username:'local-organizer',password:'disposable simulator password 2026'});
  const admin = (method:string,path:string,body:any={})=>api('admin',method,path,body,bootstrap.idToken);
  for (const account of accounts) {
    await admin('POST','/admin/competitor-accounts',{awsAccountId:account,competitorRoleName:'LocalRoutingRole'});
    await admin('POST',`/admin/competitor-accounts/${account}/verify`);
  }
  const event = await admin('POST','/events',{name:'Local hello-world',teams:accounts.map((account,i)=>({internalSlug:`team-${i+1}`,awsAccountId:account})),problems:[{problemId:'hello-world'}]});
  await admin('POST',`/events/${event.eventId}/deploy`);
  await service.drain();
  const jobs=store.jobs(event.eventId);
  assert(jobs.every((job:any)=>job.status==='COMPLETE'), JSON.stringify(jobs.map((job:any)=>({status:job.status,error:job.error}))));
  await admin('PATCH',`/events/${event.eventId}/schedule`,{startNow:true,endsAt:new Date(Date.now()+600000).toISOString()});
  const flags: string[]=[];
  const access: string[]=[];
  for (const [index,team] of event.teams.entries()) {
    const job = jobs.find((item:any)=>item.teamId===team.teamId);
    const outputs = JSON.parse(job.unit).outputs;
    const runtime=runtimes.get(accounts[index])!;
    const response=await fetch(`${runtime.endpoint}/_simulator/v1/access`,{method:'POST',headers:{'x-simulator-token':runtime.token,'content-type':'application/json'},body:JSON.stringify({Principal:outputs.ParticipantViewerRoleArn.split('/').at(-1)})});
    assert.equal(response.status,200);
    const participantToken=(await response.json()).AccessToken;
    access.push(participantToken);
    const ssm=new SSMClient({region:'ap-northeast-1',endpoint:runtime.endpoint,maxAttempts:1,credentials:{accessKeyId:'TCSIMPARTICIPANT',secretAccessKey:'dummy'}});
    clients.push(ssm);
    ssm.middlewareStack.add((next:any)=>(args:any)=>{args.request.headers['x-simulator-token']=participantToken;return next(args)}, {step:'build',name:'localToken'});
    flags.push((await ssm.send(new GetParameterCommand({Name:outputs.ParameterName}))).Parameter.Value);
    const view=await api('participant','GET','/portal/me',undefined,team.teamLoginKey);
    assert(!JSON.stringify(view).includes(outputs.ParameterValue),'Private answer leaked into portal');
    // Even with valid team A credential, team B process refuses entry.
    const other=runtimes.get(accounts[1-index])!;
    assert.equal((await fetch(`${other.endpoint}/_simulator/v1/capabilities`,{headers:{'x-simulator-token':participantToken}})).status,401);
    assert.equal((await fetch(`${other.endpoint}/_simulator/v1/capabilities`,{headers:{'x-simulator-token':runtime.token}})).status,401);
    const denied=await fetch(`${runtime.endpoint}/`,{method:'POST',headers:{'x-simulator-token':participantToken,'content-type':'application/x-www-form-urlencoded'},body:'Action=DescribeStacks&Version=2010-05-15'});
    assert.equal(denied.status,403,'Participant could read private stack outputs');
  }
  if (process.env.SIMULATOR_PRACTICE==='1') {
    const files=event.teams.map((team:any,index:number)=>{
      const file=join(directory,`team-${index+1}.json`);
      writeFileSync(file,JSON.stringify({teamKey:team.teamLoginKey,consoleUrl:runtimes.get(accounts[index])!.endpoint+'/_simulator/console/#/parameters',consoleToken:access[index],portalUrl:participantHttp.origin},null,2),{mode:0o600});return file;
    });
    console.log(JSON.stringify({practiceReady:true,portal:participantHttp.origin,teamFiles:files,expiresMinutes:10}));
    await new Promise<void>(done=>{process.once('SIGINT',()=>done());process.once('SIGTERM',()=>done())});
    await admin('DELETE',`/events/${event.eventId}`);await service.drain();
    console.log(JSON.stringify({practiceStopped:true,teardown:store.jobs(event.eventId).map((job:any)=>job.status)}));
    // Keep automation assertions separate from participant-driven practice.
  } else {
  assert.notEqual(flags[0],flags[1]);
  const submit=(i:number,flag:string)=>api('participant','POST','/portal/me/submit-flag',{problemId:'hello-world',flag},event.teams[i].teamLoginKey);
  assert.equal((await submit(1,flags[0])).kind,'wrong');
  const answers=await Promise.all(Array.from({length:8},()=>submit(0,flags[0])));
  assert.equal(answers.filter(a=>a.kind==='ok').length,1);
  assert.equal((await submit(1,flags[1])).kind,'ok');
  assert.equal(store.team(event.teams[0].teamId).score,100);
  assert.equal(store.team(event.teams[1].teamId).score,95);
  assert.equal(store.team(event.teams[0].teamId).scoreEvents.length,1);
  await admin('DELETE',`/events/${event.eventId}`);
  await service.drain();
  assert(store.jobs(event.eventId).every((job:any)=>job.status==='DELETED'));
  console.log(JSON.stringify({catalog:'hello-world',teams:2,realPlatformScoring:true,scores:[100,95],duplicateAwards:0,crossTeamTokenStatus:401,privateOutputStatus:403,teardown:'DELETED',nativeSTS:false}));
  }
} finally {
  await adminHttp?.close(); await participantHttp?.close();
  for (const client of clients) client.destroy();
  store?.close();
  for (const child of processes) child.kill();
  await Promise.all(processes.map(child=>new Promise<void>(done=>child.exitCode!==null?done():child.once('exit',()=>done()))));
  rmSync(directory,{recursive:true,force:true});
}
