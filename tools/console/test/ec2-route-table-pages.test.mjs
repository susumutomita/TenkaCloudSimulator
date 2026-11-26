import assert from 'node:assert/strict';
import test from 'node:test';
import {loadEC2RouteTablePages} from '../src/ec2-route-table-pages.mjs';
import {deleteVpcNativeRouteTable,loadVpcNativeRouteTables} from '../src/ec2-native-route-tables.mjs';
import {loadVpcRouteTables} from '../src/ec2-route-tables.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const region='us-east-1';
const vpc={id:'vpc-11111111111111111',cidr:'10.1.0.0/16',managedBy:'ec2'};
const cfn={id:'vpc-22222222222222222',cidr:'10.2.0.0/16',managedBy:'cloudformation'};
const id=(kind,n)=>kind+'-'+n.toString(16).padStart(17,'0');
const pageToken=n=>String(n).padEnd(48,'a');
const node=(localName,data=[])=>{
  const children=Array.isArray(data)?data:[];
  return {localName,namespaceURI:ns,children,textContent:typeof data==='string'?data:'',nodeType:1};
};
const fields=members=>Object.entries(members).map(([k,v])=>node(k,v));
const doc=(action,members)=>({documentElement:node(action+'Response',fields({requestId:'local-request',...members}))});
const pair=(table,subnet,assoc=id('rtbassoc',1))=>node('item',fields({
  routeTableId:id('rtb',table),subnetId:id('subnet',subnet),routeTableAssociationId:assoc,main:'false',
}));
const table=(n,opts={})=>node('item',fields({
  routeTableId:id('rtb',n),vpcId:vpc.id,ownerId:'000000000000',routeSet:[],associationSet:[],tagSet:[],...opts,
}));
const first=({hasPeer=false}={})=>Array.from({length:100},(_,i)=>table(i+2,
  hasPeer&&i===0?{associationSet:[pair(i+2,1)]}:{}));
const page=(rows,token)=>doc('DescribeRouteTables',{routeTableSet:rows,...(token===undefined?{}:{nextToken:token})});
const wrap=n=>new Response(String(n),{headers:{'Content-Type':'text/xml'}});
function fixture(replies) {
  const calls=[];
  return {calls,parse:text=>replies[Number(text)],request:async function(...args) {
    assert.equal(args.length,2);
    const [path,init]=args;
    const n=calls.length;
    assert.equal(path,'/');
    assert.equal(init.credentials,'same-origin');
    assert.equal(init.redirect,'error');
    assert.equal(init.headers['X-Simulator-Region'],region);
    assert.ok(n<replies.length,'unexpected additional request');
    calls.push(Object.fromEntries(init.body));
    return typeof replies[n]==='function'?replies[n]():wrap(n);
  }};
}
const action={Action:'DescribeRouteTables',Version:'2016-11-15'};
const describe={...action,MaxResults:'100'};
const invalid=/Invalid or incomplete EC2 (?:route table )?response/;

test('publishes only once terminal verified; both VPC panes share page contract and single-ID read stays separate',async()=>{
  for(const [target,load,expected] of [
    [vpc,loadVpcNativeRouteTables,id('rtb',100)],
    [cfn,loadVpcRouteTables,id('rtb',105)],
  ]) {
    let release,settled=false;
    const tail=page([table(105,{vpcId:cfn.id})]);
    const pages=fixture([page(first(),pageToken(1)),()=>new Promise(resolve=>{release=()=>resolve(wrap(1));})]);
    const read=load(pages.request,region,target,{parse:x=>Number(x)===1?tail:pages.parse(x)}).then(rows=>{settled=true;return rows;});
    for(let i=0;!release&&i<8;i++) await new Promise(resolve=>setImmediate(resolve));
    assert.ok(release&&!settled,'first page must not appear before the promised continuation');
    release();
    const rows=await read;
    assert.ok(rows.some(row=>row.id===expected));
    assert.equal(rows.length,target===vpc?100:1);
    assert.deepEqual(pages.calls,[describe,{...describe,NextToken:pageToken(1)}]);
  }
  const exact=fixture([page([table(5)])]);
  const state=await loadEC2RouteTablePages(exact.request,region,{...exact,exactID:id('rtb',5)});
  assert.deepEqual(state.tables.map(r=>r.id),[id('rtb',5)]);
  assert.deepEqual(exact.calls,[{...action,'RouteTableId.1':id('rtb',5)}]);
  const wrong=fixture([page([table(6)])]);
  await assert.rejects(loadEC2RouteTablePages(wrong.request,region,{...wrong,exactID:id('rtb',5)}),invalid);
  const nonterminalExact=fixture([page([table(5)],pageToken(1))]);
  await assert.rejects(loadEC2RouteTablePages(nonterminalExact.request,region,{...nonterminalExact,exactID:id('rtb',5)}),invalid);
});

test('invalid later page, duplicate relationship, overflow or lost continuation never turns a prefix into inventory',async()=>{
  const base=page(first({hasPeer:true}),pageToken(1));
  for(const pages of [
    [base,page([])],
    [base,page([table(101)])],
    [base,page([table(120,{associationSet:[pair(120,2)]})])], // reused regional association ID
    [base,page([table(120,{associationSet:[pair(120,1,id('rtbassoc',9))]})])], // reused regional subnet
    [base,page([table(120)],pageToken(2))],
    [base,page([table(120,{associationSet:Array.from({length:900},(_,i)=>pair(120,900+i,id('rtbassoc',900+i)) )})])],
    [page(first(),pageToken(1)),page(Array.from({length:100},(_,i)=>table(102+i)),pageToken(1))],
    [base,()=>new Response('unavailable',{status:400})],
  ]) {
    const f=fixture(pages);
    await assert.rejects(loadEC2RouteTablePages(f.request,region,f));
    assert.equal(f.calls.length,2);
  }
  const tooMany=fixture(Array.from({length:10},(_,pageIndex)=>page(
    Array.from({length:100},(_,index)=>table(pageIndex*100+index+1)),pageToken(pageIndex))));
  await assert.rejects(loadEC2RouteTablePages(tooMany.request,region,tooMany),invalid);
  assert.equal(tooMany.calls.length,10);
  const allowedEmpty=fixture([page([])]);
  assert.deepEqual((await loadEC2RouteTablePages(allowedEmpty.request,region,allowedEmpty)).tables,[]);
});

test('stale scope and empty terminal continuation cannot confirm a saved table deletion or replay a write',async()=>{
  const init=page([table(1)]);
  const removed=doc('DeleteRouteTable',{return:'true'});
  const completePrefix=page(first(),pageToken(1));
  const incomplete=fixture([init,removed,completePrefix,page([])]);
  const [target]=await loadVpcNativeRouteTables(incomplete.request,region,vpc,incomplete);
  await assert.rejects(deleteVpcNativeRouteTable(incomplete.request,region,vpc,target,incomplete),invalid);
  assert.deepEqual(incomplete.calls,[describe,{...action,Action:'DeleteRouteTable',RouteTableId:id('rtb',1)},
    describe,{...describe,NextToken:pageToken(1)}]);
  const controller=new AbortController();
  const aborted=fixture([completePrefix,()=>{controller.abort();return wrap(1);}]);
  await assert.rejects(loadEC2RouteTablePages(aborted.request,region,{...aborted,signal:controller.signal}),{name:'AbortError'});
  assert.equal(aborted.calls.length,2);
});
