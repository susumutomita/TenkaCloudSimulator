import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';

const require=createRequire(import.meta.url);
const {outputFiles}=await build({
  absWorkingDir:fileURLToPath(new URL('..',import.meta.url)),
  entryPoints:['src/ec2-readonly.jsx'],bundle:true,write:false,platform:'node',format:'cjs',loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const runtime={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,runtime,runtime.exports);
const {createElement}=require('react'),{renderToStaticMarkup}=require('react-dom/server');
const {VPCDetailsTabs}=runtime.exports;
const props={
  request:()=>assert.fail('SSR must not issue a resource inventory request'),
  region:'us-east-1',vpc:{id:'vpc-'+'a'.repeat(17),cidr:'10.1.0.0/16',managedBy:'ec2'},
  capabilities:{services:{ec2:{console:true,operations:['DescribeVpcs','DescribeInstances','DescribeSubnets'],subnetLifecycle:['DescribeSubnets']}}},
  authenticationRequired:true,saving:false,selectionLocked:()=>false,
};
function markup(overrides={}) {
  return renderToStaticMarkup(createElement(VPCDetailsTabs,{...props,...overrides},createElement('p',null,'Existing details content')));
}
function tabHTML(body,label) {
  const match=(body.match(/<button\b[^>]*role="tab"[^>]*>.*?<\/button>/g)||[]).find(s=>s.includes('>'+label+'<'));
  assert.ok(match,'Missing '+label+' tab');
  return match;
}

test('one selected authenticated VPC has a Details-first Map tab without prefetching it',()=>{
  const body=markup();
  assert.match(body,/aria-label="VPC details views"/);
  assert.match(tabHTML(body,'Details'),/aria-selected="true"/);
  assert.match(tabHTML(body,'Resource map'),/aria-selected="false"/);
  assert.equal(body.match(/role="tablist"/g).length,1);
  assert.match(body,/Existing details content/);
  assert.doesNotMatch(body,/Loading resource map|Saved local configuration/);
});

test('active sibling lease leaves details mounted and disables selecting the Resource Map',()=>{
  const body=markup({saving:true,selectionLocked:()=>true});
  const details=tabHTML(body,'Details'),map=tabHTML(body,'Resource map');
  assert.match(details,/aria-selected="true"/);
  assert.doesNotMatch(details,/aria-disabled="true"/);
  assert.match(map,/aria-disabled="true"/);
  assert.match(body,/Existing details content/);
  assert.doesNotMatch(body,/Loading resource map/);
});

test('missing capability, auth, region, or verified VPC metadata preserves the existing details without tabs',()=>{
  const fallbacks=[
    {vpc:null}, {vpc:{...props.vpc,managedBy:'unknown'}}, {vpc:{...props.vpc,cidr:'10.1.7.0/16'}},
    {region:'us-east-1\n'}, {authenticationRequired:false},
    {capabilities:{...props.capabilities,consoleScope:{region:props.region}}},
    {capabilities:{services:{ec2:{console:true,operations:['DescribeVpcs','DescribeInstances']}}}},
  ];
  for (const overrides of fallbacks)assert.equal(markup(overrides),'<p>Existing details content</p>');
});
