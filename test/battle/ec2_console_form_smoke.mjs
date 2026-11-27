import {readFileSync} from 'node:fs';
import {spawnSync} from 'node:child_process';
import {
  confirmInstanceStack,
  instanceStackLaunchAvailable,
  submitInstanceStack,
} from '../../tools/console/src/ec2-instance-stack-launch.mjs';

// Guest/runtime provisioning belongs to the unchanged Battle rehearsal.
// This adapter drives the production form's wire path, not a real browser
// layout. Credentials are passed on stdin to the isolated child, never in argv.
const xmlTree = [
  'import json, sys',
  'from xml.etree import ElementTree',
  'def project(n):',
  '    namespace, name = n.tag[1:].split("}", 1) if n.tag.startswith("{") else (None, n.tag)',
  '    return dict(namespaceURI=namespace, localName=name, children=[project(c) for c in n], textContent=n.text or "")',
  'print(json.dumps({"documentElement": project(ElementTree.fromstring(sys.stdin.buffer.read()))}))',
].join('\n');

let stage = 'input';
try {
  const input = JSON.parse(readFileSync(0, 'utf8'));
  const target = new URL(input.endpoint);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(input.endpoint) || !target.port ||
      !/^tcsim_[0-9a-f]{12}=[0-9a-f]{64}$/.test(input.cookie) ||
      !Number.isInteger(input.timeout) || input.timeout < 1000 || input.timeout > 330000) {
    throw Error('Use the disposable authenticated loopback rehearsal.');
  }
  const parser = text => {
    // Node has no DOMParser. Convert the actual bounded XML to the DOM-shaped
    // structure consumed by the same production validation functions.
    const result = spawnSync(input.python, ['-I', '-c', xmlTree], {
      input: text, encoding: 'utf-8', timeout: 10000, maxBuffer: 8 * 1024 * 1024, env: {},
    });
    if (result.status !== 0) throw Error('XML readback could not be parsed.');
    return JSON.parse(result.stdout);
  };
  const operations = [];
  const principal = 'simulator-admin';
  const auth = new AbortController();
  const timer = setTimeout(() => auth.abort(), input.timeout);
  try {
    const request = (path, options = {}) => {
      if (path !== '/' && path !== '/_simulator/v1/capabilities') throw Error('Unexpected local request.');
      if (path === '/') operations.push(options.body?.get('Action'));
      return fetch(target.origin + path, {...options, redirect:'error', signal:auth.signal, headers:{
        ...options.headers, 'Cookie':input.cookie, 'Origin':target.origin, 'X-Simulator-Principal':principal,
      }});
    };
    stage = 'discover';
    const advertised = await request('/_simulator/v1/capabilities');
    if (advertised.status !== 200 || !instanceStackLaunchAvailable(await advertised.json(),
        advertised.headers.get('X-Simulator-Authentication') === 'required', principal)) {
      throw Error('Actual runtime was not advertised to this browser identity.');
    }
    const options = {allowed:true, signal:auth.signal, parse:parser};
    stage = 'submit';
    const receipt = await submitInstanceStack(request, input.region, {
      stackName:input.stackName, imageId:input.imageId, acknowledge:true,
    }, options);
    stage = 'readback';
    const verified = await confirmInstanceStack(request, input.region, receipt, options);
    if (operations.join('|') !== 'CreateStack|DescribeStacks|DescribeInstances') throw Error('Unexpected action/retry.');
    const row = verified.instance;
    process.stdout.write(JSON.stringify({stackId:verified.stackId, id:row.id, image:row.image, type:row.type,
      vpc:row.vpc, subnet:row.subnet, groups:row.groupIds}));
  } finally {
    clearTimeout(timer);
  }
} catch {
  // Do not print untrusted XML/HTTP diagnostics into the uploaded job logs.
  process.stderr.write('EC2 console form smoke failed in ' + stage + '\n');
  process.exitCode = 1;
}
