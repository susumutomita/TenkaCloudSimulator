// Render the real embedded app. The default and refusal checks use the
// actual local CLI; explicitly synthetic fixtures project only availability
// or readback outcomes to verify rendering; this test never starts a VM.
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, operator, participant} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port <= 65535);
const host = 'http://127.0.0.1:' + port;
const pause = () => new Promise(resolve => setTimeout(resolve, 12));
const button = (d, label) => [...d.querySelectorAll('button')].find(node =>
  node.textContent.trim() === label || [...node.children].some(part => part.textContent.trim() === label));
const openLabel = 'Create instance stack', previewLabel = 'Preview CloudFormation template';
const confirmLabel = 'Create local instance stack';
const wait = async (check, note) => {
  for (let deadline = Date.now() + 7000; Date.now() < deadline;) {
    const result = check();
    if (result) return result;
    await pause();
  }
  assert.fail(note);
};

async function browser(cookie, check, fixture = '') {
  const [index, bundle] = await Promise.all(['/', '/app.js'].map(path =>
    fetch(host + '/_simulator/console' + path, {headers:{Cookie:cookie}, redirect:'error'}).then(async res => {
      assert.equal(res.status, 200);
      return res.text();
    })));
  const dom = new JSDOM(index, {url:host+'/_simulator/console/#/ec2', pretendToBeVisual:true,
    runScripts:'outside-only', virtualConsole:new VirtualConsole()});
  const w = dom.window, d = w.document, calls = [];
  let discoveryReads = 0, syntheticChecks = 0;
  const syntheticStack = 'arn:aws:cloudformation:us-east-1:000000000000:stack/console-private-instance/'+'3'.repeat(32);
  const syntheticIDs = {InstanceId:'i-', VpcId:'vpc-', SubnetId:'subnet-', GroupId:'sg-'};
  for (const key of Object.keys(syntheticIDs)) syntheticIDs[key] += '3'.repeat(17);
  const cfn = (action, inside) => new Response('<'+action+'Response xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"><'+
    action+'Result>'+inside+'</'+action+'Result></'+action+'Response>');
  try {
    Object.assign(w, {TextEncoder, TextDecoder, matchMedia: () => ({
      matches:false, addListener(){}, removeListener(){}, addEventListener(){}, removeEventListener(){},
    }), ResizeObserver:class{observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver:class{observe(){} unobserve(){} disconnect(){}}, scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView = function(){};
    w.fetch = async (path, options = {}) => {
      assert.ok(['/', '/_simulator/v1/capabilities'].includes(path));
      const headers = new Headers(options.headers);
      headers.set('Cookie', cookie);
      const p = new URLSearchParams(options.body), action = p.get('Action');
      if (fixture === 'synthetic-readback') {
        if (action === 'CreateStack') {
          assert.equal(headers.get('X-Simulator-Service'), 'cloudformation');
          assert.equal(p.get('StackName'), 'console-private-instance');
          calls.push([action, 200]);
          return cfn(action, '<StackId>'+syntheticStack+'</StackId>');
        }
        if (action === 'DescribeStacks') {
          assert.equal(p.get('StackName'), syntheticStack);
          calls.push([action, 200]);
          const phase = ++syntheticChecks === 1 ? 'CREATE_IN_PROGRESS' : 'CREATE_COMPLETE';
          const outputs = Object.entries(syntheticIDs).map(([key,id])=>'<member><OutputKey>'+key+'</OutputKey><OutputValue>'+id+'</OutputValue></member>').join('');
          return cfn(action, '<Stacks><member><StackId>'+syntheticStack+'</StackId><StackName>console-private-instance</StackName><StackStatus>'+
            phase+'</StackStatus><Outputs>'+outputs+'</Outputs></member></Stacks>');
        }
        if (action === 'DescribeInstances' && syntheticChecks >= 2) {
          calls.push([action, 200]);
          const m=syntheticIDs;
          return new Response('<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet><item><instancesSet><item>'+
            '<instanceId>'+m.InstanceId+'</instanceId><imageId>ami-00000000</imageId><instanceType>t3.micro</instanceType><vpcId>'+m.VpcId+
            '</vpcId><subnetId>'+m.SubnetId+'</subnetId><groupSet><item><groupId>'+m.GroupId+
            '</groupId></item></groupSet><tagSet><item><key>Name</key><value>console-private-instance</value></item></tagSet>'+
            '<simulatorRuntimeState>ready</simulatorRuntimeState></item></instancesSet></item></reservationSet></DescribeInstancesResponse>');
        }
      }
      const res = await fetch(host+path, {...options, headers, body:options.body?.toString(), signal:undefined, redirect:'error'});
      if (path.endsWith('/capabilities')) {
        discoveryReads++;
        if (['advertised','synthetic-readback'].includes(fixture) || fixture === 'revoked' && discoveryReads === 1) {
          const cap = await res.json();
          assert.equal(cap.services.ec2.instanceStackLaunch, undefined, 'fixture is on unconfigured default');
          cap.services.ec2.instanceStackLaunch = 'CloudFormation/CreateStack';
          return new Response(JSON.stringify(cap), {status:res.status, headers:res.headers});
        }
        return res;
      }
      assert.ok(['DescribeInstances', 'CreateStack'].includes(action), 'unexpected operation '+action);
      if (action === 'CreateStack') {
        assert.equal(fixture, 'advertised');
        assert.equal(headers.get('X-Simulator-Region'), 'us-east-1');
        assert.equal(headers.get('X-Simulator-Service'), 'cloudformation');
        assert.equal(p.get('StackName'), 'console-private-instance');
        assert.equal(p.get('Capabilities.member.1'), 'CAPABILITY_IAM');
        const {Resources} = JSON.parse(p.get('TemplateBody'));
        assert.equal(Resources.Instance.Properties.ImageId, 'ami-00000000');
        assert.equal(Resources.Group.Properties.SecurityGroupIngress, undefined);
      }
      calls.push([action, res.status]);
      return res;
    };
    w.eval(bundle);
    const dialog = () => d.querySelector('[aria-label="Close instance stack form"]')?.closest('[role="dialog"]');
    const fill = (input, text) => {
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype, 'value').set.call(input, text);
      input.dispatchEvent(new w.Event('input', {bubbles:true}));
    };
    const prepare = async () => {
      const launch = await wait(() => button(d, openLabel), 'missing launch control');
      assert.notEqual(launch.getAttribute('aria-disabled'), 'true');
      assert.ok(!launch.disabled);
      launch.click();
      const modal = await wait(dialog, 'missing launch dialog');
      const inputs = [...modal.querySelectorAll('input[type="text"]')];
      assert.equal(inputs.length, 2);
      fill(inputs[0], 'console-private-instance');
      fill(inputs[1], 'ami-00000000');
      const go = () => button(dialog(), confirmLabel);
      await wait(() => go()?.disabled, 'missing required IAM acknowledgement');
      modal.querySelector('input[type="checkbox"]').click();
      return wait(() => go() && !go().disabled && go(), 'valid bounded form does not enable submit');
    };
    await check({d, calls, dialog, prepare, discoveryReads:()=>discoveryReads});
  } finally { w.close(); }
}

(async () => {
  await browser(operator, async ({d, calls, dialog}) => {
    const create = await wait(() => button(d, openLabel), 'missing default admin control').catch(error => {
      throw Error(error.message + ': ' + [...d.querySelectorAll('button')].map(b => b.textContent.trim()).slice(-8).join(' | '));
    });
    assert.ok(create.disabled || create.getAttribute('aria-disabled') === 'true');
    create.click();
    assert.equal(dialog(), undefined);
    assert.ok(button(d, previewLabel));
    assert.match(d.body.textContent, /Instance creation requires the opt-in local VM runtime/);
    assert.equal(calls.filter(([a]) => a !== 'DescribeInstances').length, 0);
  });
  await browser(participant, async ({d, calls}) => {
    await wait(() => button(d, previewLabel), 'participant preview missing');
    assert.equal(button(d, openLabel), undefined);
    assert.equal(calls.filter(([a]) => a !== 'DescribeInstances').length, 0);
  });
  await browser(operator, async ({calls, dialog, prepare, discoveryReads}) => {
    (await prepare()).click();
    await wait(() => dialog()?.textContent.includes('No create request was sent.'), 'missing stale availability refusal');
    assert.equal(discoveryReads(), 2);
    assert.equal(calls.filter(([a]) => a !== 'DescribeInstances').length, 0);
  }, 'revoked');
  await browser(operator, async ({calls, dialog, prepare, discoveryReads}) => {
    (await prepare()).click();
    await wait(() => dialog()?.textContent.includes('nothing was retried.'), 'missing explicit uncertain create result');
    assert.equal(discoveryReads(), 2);
    assert.deepEqual(calls.filter(([a]) => a !== 'DescribeInstances'), [['CreateStack', 501]]);
    assert.ok(button(dialog(), confirmLabel).disabled);
    assert.doesNotMatch(dialog().textContent, /CloudFormation request accepted|Stack complete and instance record confirmed/);
  }, 'advertised');
  await browser(operator, async ({calls, dialog, prepare}) => {
    (await prepare()).click();
    await wait(() => dialog()?.textContent.includes('Could not confirm both stack completion'), 'in-progress stack was not held');
    assert.match(dialog().textContent, /CloudFormation request accepted/);
    assert.doesNotMatch(dialog().textContent, /Stack complete and instance record confirmed/);
    button(dialog(), 'Check stack and instance status').click();
    await wait(() => dialog()?.textContent.includes('Stack complete and instance record confirmed'), 'exact saved readback did not render');
    assert.match(dialog().textContent, /Current guest liveness was not checked/);
    assert.deepEqual(calls.filter(([a]) => a !== 'DescribeInstances'), [['CreateStack',200],['DescribeStacks',200],['DescribeStacks',200]]);
  }, 'synthetic-readback');
  console.log('PASS embedded EC2 form: real default/participant restrictions and native 501; synthetic expiry and readback, no replay or liveness claim (JSDOM, no VM).');
})().catch(error => {console.error(error); process.exitCode=1;});
