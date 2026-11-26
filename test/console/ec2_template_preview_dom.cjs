const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, operator, reviewer, denied} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port <= 65535);
const host = 'http://127.0.0.1:' + port;
const pause = ms => new Promise(r => setTimeout(r, ms));
const button = (document, title) => [...document.querySelectorAll('button')].find(node => node.textContent.trim() === title);
const wait = async (condition, note) => {
  for (let deadline = Date.now() + 7000; Date.now() < deadline;) {
    const result = condition();
    if (result) return result;
    await pause(14);
  }
  assert.fail(note);
};

async function browser(cookie, check, {handoff = false} = {}) {
  const headers = {Cookie: cookie};
  const [index, bundle] = await Promise.all(['/', '/app.js'].map(path =>
    fetch(host + '/_simulator/console' + path, {headers, redirect:'error'}).then(async resp => {
      assert.ok(resp.ok);
      return resp.text();
    })));
  const dom = new JSDOM(index, {url:host+'/_simulator/console/#/ec2', pretendToBeVisual:true,
    runScripts:'outside-only', virtualConsole:new VirtualConsole()});
  const w = dom.window, d = w.document, calls = [];
  let hold = false, release = null;
  try {
    Object.assign(w, {TextEncoder, TextDecoder, matchMedia: () => ({
      matches:false, addListener(){}, removeListener(){}, addEventListener(){}, removeEventListener(){},
    }), ResizeObserver:class{observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver:class{observe(){} unobserve(){} disconnect(){}}, scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView = function(){};
    w.fetch = async (url, options = {}) => {
      assert.ok(url === '/' || url === '/_simulator/v1/capabilities', 'unexpected network path');
      const h = new Headers(options.headers);
      h.set('Cookie', cookie);
      const params = new URLSearchParams(options.body);
      const action = params.get('Action');
      if (url === '/') assert.ok(['DescribeInstances', 'ValidateTemplate'].includes(action), 'unexpected operation');
      // Bridge the DOM's signal into Node's genuine loopback fetch; retain
      // the page signal for the assertions. Undici rejects other realms.
      const transport = options.signal && new AbortController();
      if (transport) {
        if (options.signal.aborted) transport.abort();
        else options.signal.addEventListener('abort', () => transport.abort(), {once:true});
      }
      const res = await fetch(host + url, {...options, headers:h, body:options.body?.toString(),
        signal:transport?.signal, redirect:'error'});
      if (handoff && url.endsWith('/capabilities')) {
        // Fixture projection only: this does not issue or exercise a real handoff.
        const caps = await res.json();
        caps.consoleScope = {instanceId:'i-00000000000000001', region:'us-east-1'};
        return new Response(JSON.stringify(caps), {status:res.status, headers:res.headers});
      }
      if (action !== 'ValidateTemplate') return res;
      assert.equal(options.method,'POST');
      assert.equal(options.credentials,'same-origin');
      assert.equal(options.redirect,'error');
      assert.equal(h.get('X-Simulator-Service'),'cloudformation');
      assert.deepEqual([...params.keys()], ['Action','Version','TemplateBody']);
      assert.equal(params.get('Version'),'2010-05-15');
      calls.push({region:h.get('X-Simulator-Region'), status:res.status, signal:options.signal});
      if (hold) {
        hold = false;
        return new Promise(resolve => {release = () => resolve(res);});
      }
      return res;
    };
    w.eval(bundle);
    await check({w, d, calls, holdNext:() => {hold=true;}, held:() => release, finish:() => {release(); release=null;}});

  } finally {
    release?.();
    w.close();
  }
}

(async () => {
  const open = d => button(d, 'Preview CloudFormation template');
  const dialog = d => d.querySelector('button[aria-label="Close preview"]')?.closest('[role="dialog"]');
  const select = (d, name) => button(dialog(d), name).click();
  const validate = async d => {
    (await wait(() => {
      const candidate = button(dialog(d), 'Validate template');
      return candidate && !candidate.disabled && candidate;
    }, 'valid draft did not enable native validation')).click();
  };
  const contains = (d, text) => !!dialog(d)?.textContent.includes(text);
  const edit = (w, d, text) => {
    const target = dialog(d).querySelector('textarea');
    Object.getOwnPropertyDescriptor(w.HTMLTextAreaElement.prototype, 'value').set.call(target, text);
    target.dispatchEvent(new w.Event('input', {bubbles:true}));
  };
  const prompt = async d => {
    (await wait(() => open(d), 'missing EC2 preview action')).click();
    await wait(() => dialog(d)?.querySelector('textarea'), 'missing modal body');
  };
  await browser(operator, async h => {
    const {w, d, calls} = h;
    await prompt(d);
    assert.equal(calls.length, 0);
    assert.ok(contains(d, 'Preview only.'));
    assert.equal(button(dialog(d),'Launch'), undefined);
    select(d, 'Use example template');
    assert.equal(calls.length,0,'loading an example must not submit');
    await validate(d);
    await wait(() => contains(d, 'Template declaration accepted'), 'valid native YAML did not confirm');
    assert.ok(contains(d, 'No resources were created.'));
    assert.ok(contains(d, 'CAPABILITY_IAM'));
    assert.ok(contains(d, 'ImageId'));
    assert.equal(calls[0].status,200);
    assert.equal(calls[0].region,'us-east-1');

    h.holdNext();
    await validate(d);
    await wait(h.held, 'native reply was not held');
    assert.ok(!contains(d, 'Template declaration accepted'), 'old success persisted while refreshing');
    edit(w, d, 'Parameters: invalid');
    await wait(() => calls[1].signal.aborted, 'edit did not abort old request');
    await validate(d);
    await wait(() => contains(d, 'CloudFormation rejected this declaration'), 'missing native rejection');
    h.finish();
    await pause(85);
    assert.ok(!contains(d, 'Template declaration accepted'), 'late reply after draft change was accepted');

    edit(w, d, '界'.repeat(17067));
    await wait(() => contains(d, 'The template exceeds 51,200 UTF-8 bytes.'), 'missing local byte limit');
    assert.equal(calls.length, 3);
    assert.ok(button(dialog(d),'Validate template').disabled);
    dialog(d).querySelector('button[aria-label="Close preview"]').click();
    await wait(() => !dialog(d), 'preview did not close');
    await prompt(d);
    assert.equal(dialog(d).querySelector('textarea').value, '', 'closed draft was retained');

    select(d, 'Use example template');
    h.holdNext();
    await validate(d);
    await wait(h.held,'missing regional pending preview');
    w.location.hash = '/ec2?region=ap-northeast-1';
    await wait(() => !dialog(d) && calls[3].signal.aborted, 'region change did not discard preview');
    h.finish();
    await prompt(d);
    select(d,'Use example template');
    await validate(d);
    await wait(() => contains(d,'Template declaration accepted'), 'new native region did not validate');
    assert.equal(calls[4].region,'ap-northeast-1');
  });
  await browser(reviewer, async ({w, d, calls}) => {
    await prompt(d);
    edit(w, d, `Parameters:
  PublicValue: {Type: String, Default: fixture-default}
  MaskedValue: {Type: String, NoEcho: true, Default: fixture-masked}
Resources:
  Vpc: {Type: 'AWS::EC2::VPC', Properties: {CidrBlock: '10.91.0.0/16'}}
`);
    await validate(d);
    await wait(() => contains(d,'Template declaration accepted'), 'preview-only browser could not validate');
    assert.equal(calls[0].status, 200);
    const metadata = dialog(d).querySelector('table').textContent;
    assert.match(metadata, /PublicValue/);
    assert.match(metadata, /MaskedValue.*Not shown/);
    assert.doesNotMatch(metadata, /fixture-default|fixture-masked/);
  });
  await browser(denied, async ({d, calls}) => {
    await prompt(d);
    select(d,'Use example template');
    await validate(d);
    await wait(() => contains(d,'The current identity needs cloudformation:ValidateTemplate'), '403 became a preview');
    assert.equal(calls[0].status, 403);
    assert.ok(!contains(d,'Template declaration accepted'));
  });
  await browser(operator, async ({d, calls}) => {
    await wait(() => button(d,'Connect with Session Manager'), 'handoff fixture did not render');
    assert.equal(open(d),undefined);
    assert.equal(calls.length,0);
  }, {handoff:true});
  console.log('PASS served embedded Console/native ValidateTemplate: both regions, scoped identities, bounded edits, cancellation and no launch; JSDOM, handoff rendering fixture.');
})().catch(error => {console.error(error); process.exitCode=1;});
