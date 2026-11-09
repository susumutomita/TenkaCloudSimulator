import test from 'node:test';
import assert from 'node:assert/strict';
import {decodeLogTail,invokeFunction} from '../../tools/console/src/lambda-invoke.mjs';
const encoded=text=>Buffer.from(text).toString('base64');

test('log tail decoding preserves text and bounded byte slicing without HTML parsing',()=>{
 assert.equal(decodeLogTail(encoded('日本語\n<script>alert(1)</script>')),'日本語\n<script>alert(1)</script>');
 assert.equal(decodeLogTail(''),'');
 assert.equal(decodeLogTail(Buffer.concat([Buffer.from([0x81,0x82]),Buffer.from('end')]).toString('base64')),'\ufffd\ufffdend');
 assert.equal(decodeLogTail(encoded('x'.repeat(4096))).length,4096);
 for(const invalid of [null,undefined,'bad','%%%%',encoded('x'.repeat(4097)),encoded('x'.repeat(5000))])assert.throws(()=>decodeLogTail(invalid),/log tail/);
});

test('invocation logs require explicit opt-in and retain native request authority',async()=>{
 let seen;const controller=new AbortController();
 const request=async(path,options)=>{seen={path,options};return {json:async()=>({answer:42}),headers:new Headers({'X-Amz-Log-Result':encoded('private output')})}};
 const ordinary=await invokeFunction(request,'ap-northeast-1','name/雪','{}',{signal:controller.signal});
 assert.equal(ordinary.logTail,null);assert.equal('X-Amz-Log-Type' in seen.options.headers,false);
 const explicit=await invokeFunction(request,'ap-northeast-1','name/雪','{}',{includeLogs:true,signal:controller.signal});
 assert.deepEqual(explicit,{data:{answer:42},functionError:false,logTail:'private output'});
 assert.equal(seen.path,'/2015-03-31/functions/name%2F%E9%9B%AA/invocations');assert.equal(seen.options.signal,controller.signal);
 assert.deepEqual(seen.options.headers,{'Content-Type':'application/json','X-Simulator-Service':'lambda','X-Simulator-Region':'ap-northeast-1','X-Amz-Log-Type':'Tail'});
 assert.equal(seen.options.method,'POST');assert.equal(seen.options.body,'{}');
});

test('function errors retain explicitly requested logs; auth failures and missing logs never become success',async()=>{
 const error=await invokeFunction(async()=>({json:async()=>({errorMessage:'failed'}),headers:new Headers({'X-Amz-Function-Error':'Unhandled','X-Amz-Log-Result':encoded('before failure')})}),'us-east-1','sample','{}',{includeLogs:true});
 assert.equal(error.functionError,true);assert.equal(error.logTail,'before failure');
 await assert.rejects(invokeFunction(async()=>{throw Error('AccessDenied')},'us-east-1','sample','{}',{includeLogs:true}),/AccessDenied/);
 await assert.rejects(invokeFunction(async()=>({json:async()=>({}),headers:new Headers()}),'us-east-1','sample','{}',{includeLogs:true}),/valid log tail/);
});
