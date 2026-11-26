const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, cookie, state, asset} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port < 65536);
const origin = 'http://127.0.0.1:' + port;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(predicate, message) {
  for (const end = Date.now() + 8000; Date.now() < end;) {
    const match = predicate();
    if (match) return match;
    await sleep(20);
  }
  assert.fail(message);
}

async function boot(html, bundle, locale) {
  const dom = new JSDOM(html, {url: origin+'/_simulator/console/#/vpc', runScripts: 'outside-only',
    pretendToBeVisual: true, virtualConsole: new VirtualConsole()});
  const w = dom.window, d = w.document;
  const receipt = {calls: [], nextReadIsHeld: false, release: null};
  Object.assign(w, {TextEncoder, TextDecoder,
    matchMedia: () => ({matches: false, addListener(){}, removeListener(){}, addEventListener(){}, removeEventListener(){}}),
    ResizeObserver: class {observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver: class {observe(){} unobserve(){} disconnect(){}},
    scrollTo(){}});
  w.localStorage.setItem('tenkacloud-simulator-locale', locale);
  w.HTMLElement.prototype.scrollIntoView = function(){};
  w.fetch = async (path, init = {}) => {
    assert.ok(path === '/' || path === '/_simulator/v1/capabilities');
    const headers = new Headers(init.headers);
    headers.set('Cookie', cookie);
    const body = init.body?.toString();
    const form = new URLSearchParams(body), action = form.get('Action');
    const response = await fetch(origin + path, {...init, headers, body, signal: undefined, redirect: 'error'});
    if (action?.includes('InternetGateway')) {
      assert.equal(headers.get('X-Simulator-Region'), 'us-east-1');
      assert.equal(form.get('Version'), '2016-11-15');
      receipt.calls.push({action, data: Object.fromEntries(form), status: response.status});
      if (action === 'DescribeInternetGateways' && receipt.nextReadIsHeld) {
        receipt.nextReadIsHeld = false;
        await new Promise(resolve => {receipt.release = resolve;});
      }
    }
    return response;
  };
  w.eval(bundle);
  return {dom, window:w, document:d, receipt};
}

(async () => {
  const [html, bundle] = await Promise.all(['/', '/app.js'].map(tail =>
    fetch(origin+'/_simulator/console'+tail, {headers: {Cookie:cookie}, redirect:'error'}).then(async response => {
      assert.equal(response.status, 200);
      return response.text();
    })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'), asset);
  const {dom, window:w, document:d, receipt} = await boot(html,bundle,'en');
  try {
    const button = (label, parent = d) => [...parent.querySelectorAll('button')].find(node=>node.textContent.trim()===label);
    const row = id => d.querySelector('input[aria-label="Select gateway '+id+'"]')?.closest('tr');
    const getDialog = () => d.querySelector('button[aria-label="Close internet gateway dialog"]')?.closest('[role=dialog]');
    const change = (input, value) => {
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype,'value').set.call(input,value);
      input.dispatchEvent(new w.InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:value}));
      input.dispatchEvent(new w.Event('change',{bubbles:true}));
    };
    const stackRow = await until(()=>row(state.Gateway),'missing stack-owned gateway');
    assert.ok(stackRow.textContent.includes('CloudFormation'));
    assert.ok(stackRow.textContent.includes(state.Vpc));
    stackRow.querySelector('input').click();
    assert.ok(button('Delete internet gateway').disabled, 'CFN gateway became writable');
    assert.ok(!button('Attach internet gateway'), 'unsupported attachment is exposed');
    assert.ok(!button('Create route'), 'unsupported native route write is exposed');
    button('Create internet gateway').click();
    const editor = await until(()=>getDialog()?.querySelector('input'),'missing create form');
    change(editor,'Console & 練習');
    receipt.nextReadIsHeld = true;
    button('Create',getDialog()).click();
    await until(()=>receipt.release,'real post-create readback was not issued');
    assert.ok(!d.body.textContent.includes('Saved internet gateway:'),'create claimed success before readback');
    receipt.release();
    const native = await until(()=>[...d.querySelectorAll('tr')].find(item=>item.textContent.includes('Console & 練習')),'saved gateway not shown');
    assert.ok(native.textContent.includes('Detached'));
    const id = native.querySelector('input').getAttribute('aria-label').replace('Select gateway ','');
    assert.match(id,/^igw-[0-9a-f]{17}$/);
    assert.ok(d.body.textContent.includes('Saved internet gateway: '+id));
    native.querySelector('input').click();
    button('Delete internet gateway').click();
    const confirm = await until(()=>getDialog()?.querySelector('input[placeholder="igw-…"]'),'missing delete confirmation');
    change(confirm,state.Gateway);
    assert.ok(button('Delete',getDialog()).disabled);
    change(confirm,id);
    await until(()=>!button('Delete',getDialog())?.disabled,'matching confirmation did not enable Delete');
    receipt.nextReadIsHeld = true;
    receipt.release = null;
    button('Delete',getDialog()).click();
    await until(()=>receipt.release,'real post-delete readback was not issued');
    assert.ok(!d.body.textContent.includes('Deleted internet gateway:'),'delete claimed success before readback');
    receipt.release();
    await until(()=>d.body.textContent.includes('Deleted internet gateway: '+id),'saved deletion not shown');
    assert.ok(!row(id) && row(state.Gateway));
    assert.deepEqual(receipt.calls.map(call=>[call.action,call.status]),[
      ['DescribeInternetGateways',200], ['CreateInternetGateway',200], ['DescribeInternetGateways',200],
      ['DeleteInternetGateway',200], ['DescribeInternetGateways',200],
    ]);
    assert.equal(receipt.calls[1].data['TagSpecification.1.Tag.1.Value'],'Console & 練習');
    assert.equal(receipt.calls[3].data.InternetGatewayId,id);
  } finally {
    dom.window.close();
  }

  const ja = await boot(html,bundle,'ja');
  try {
    await until(()=>ja.document.documentElement.lang==='ja' &&
      ja.document.body.textContent.includes('インターネットゲートウェイを作成') &&
      ja.document.body.textContent.includes(state.Gateway),'Japanese view did not load actual readback');
  } finally {
    ja.dom.window.close();
  }
})().catch(error => {console.error(error); process.exitCode=1;});
