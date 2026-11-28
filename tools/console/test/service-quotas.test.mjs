import test from 'node:test';
import assert from 'node:assert/strict';
import {quotaIdentity,quotaOperations,quotaConsoleAvailable,quotaCall,quotaValue,quotaRecord,quotaHistory,quotaEpoch,definitiveQuotaRefusal} from '../src/service-quotas.mjs';
const record={...quotaIdentity,Id:'physical-id',Status:'NOT_APPROVED',DesiredValue:8,Created:1700000000.123,QuotaRequestedAtLevel:'ACCOUNT'};
test('discovery requires all actual native operations and exact protocol',()=>{
 const service={model:'service-quotas',protocol:'json',jsonVersion:'1.1',targetPrefix:'ServiceQuotasV20190624',operations:[...quotaOperations]};assert.equal(quotaConsoleAvailable(service),true);
 assert.equal(quotaConsoleAvailable({...service,operations:quotaOperations.slice(1)}),false);assert.equal(quotaConsoleAvailable({...service,protocol:'query'}),false);
});
test('JSON1.1 native request preserves operation payload region and cancellation',async()=>{
 const controller=new AbortController();let calls=0;
 await quotaCall(async(path,options)=>{calls++;assert.equal(path,'/');assert.equal(options.method,'POST');assert.equal(options.signal,controller.signal);assert.equal(options.redirect,'error');assert.deepEqual(options.headers,{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'ServiceQuotasV20190624.RequestServiceQuotaIncrease','X-Simulator-Region':'us-west-2'});assert.deepEqual(JSON.parse(options.body),{...quotaIdentity,DesiredValue:8,SupportCaseAllowed:false});return {ok:true,json:async()=>({RequestedQuota:record})}},'us-west-2','RequestServiceQuotaIncrease',{...quotaIdentity,DesiredValue:8,SupportCaseAllowed:false},controller.signal);assert.equal(calls,1);
 await assert.rejects(quotaCall(()=>{throw Error('must not send')},'us-east-1','ApproveRequest',{}));
});
test('capacity and saved physical records fail closed without fabricated zero or ID',()=>{
 assert.equal(quotaValue({...quotaIdentity,Value:5}).Value,5);assert.throws(()=>quotaValue({...quotaIdentity}));assert.throws(()=>quotaValue({...quotaIdentity,Value:null}));
 assert.equal(quotaRecord(record).Id,'physical-id');for(const bad of [{...record,Id:''},{...record,Status:'APPROVED'},{...record,Created:'today'},{...record,QuotaRequestedAtLevel:'RESOURCE'}])assert.throws(()=>quotaRecord(bad));
 assert.deepEqual(quotaHistory({RequestedQuotas:[],NextToken:'opaque'}),{items:[],next:'opaque'});assert.throws(()=>quotaHistory({RequestedQuotas:[{DesiredValue:8}]}));assert.throws(()=>quotaHistory({RequestedQuotas:[],NextToken:4}));
});
test('scope change aborts reads and rejects both stale success and error ownership',()=>{
 const epochs=quotaEpoch(),first=epochs.begin();assert.equal(first.valid(),true);const second=epochs.begin();assert.equal(first.signal.aborted,true);assert.equal(first.valid(),false);assert.equal(second.valid(),true);epochs.cancel();assert.equal(second.valid(),false);assert.equal(second.signal.aborted,true);
});
test('only definitive native refusal permits another write; transport and bad acknowledgement remain uncertain',()=>{
 for(const message of ['400: Invalid','403: Denied','409: Pending'])assert.equal(definitiveQuotaRefusal(Error(message)),true);
 for(const message of ['Failed to fetch','500: Internal','Invalid request response.','AbortError'])assert.equal(definitiveQuotaRefusal(Error(message)),false);
});
