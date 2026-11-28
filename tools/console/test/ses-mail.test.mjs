import test from 'node:test';
import assert from 'node:assert/strict';
import {canonicalDomain,checkedReceipt,composeMessage,domainProof,mailbox,messageId,receiptList} from '../src/ses-mail.mjs';
const id='a'.repeat(32), second='b'.repeat(32);
const proof={IdentityType:'DOMAIN',VerifiedForSendingStatus:false,VerificationStatus:'PENDING',DkimAttributes:{Status:'PENDING',Tokens:[id,second,'c'.repeat(32)]}};
test('display all three distinct current DNS records supplied by the identity',()=>{
 assert.equal(canonicalDomain(' Owned.TEST '),'owned.test');
 assert.throws(()=>canonicalDomain('owned.example.org'));
 assert.deepEqual(domainProof('owned.test',proof).records[0],{name:id+'._domainkey.owned.test.',value:id+'.dkim.amazonses.com.'});
 assert.throws(()=>domainProof('owned.test',{...proof,DkimAttributes:{...proof.DkimAttributes,Tokens:[id,id,id]}}));
 assert.throws(()=>domainProof('owned.test',{...proof,VerifiedForSendingStatus:true}));
});
test('compose actual native simple or named template data without replacing text',()=>{
 const form={from:'sender@owned.test',to:mailbox,mode:'simple',subject:'日本語',text:'{{x}}',html:''};
 assert.deepEqual(composeMessage(form).Content,{Simple:{Subject:{Data:'日本語',Charset:'UTF-8'},Body:{Text:{Data:'{{x}}',Charset:'UTF-8'}}}});
 const data='{"who":"テスト"}';assert.deepEqual(composeMessage({...form,mode:'template',template:'sample',data}).Content,{Template:{TemplateName:'sample',TemplateData:data}});
 assert.throws(()=>composeMessage({...form,mode:'template',template:'sample',data:'null'}));
 assert.equal(messageId({MessageId:id}),id);assert.throws(()=>messageId({MessageId:3}));
});
test('distinguish saved accepted local rejection, delivered bytes and absent data',()=>{
 const delivered={messageId:id,from:'sender@owned.test',to:mailbox,acceptedAt:'2026-01-01T00:00:00Z',outcome:'delivered',contentBytes:310};
 const rejected={...delivered,messageId:second,to:'missing@inbox.ses.test',outcome:'rejected',reason:'mailbox-not-found',contentBytes:0};
 assert.deepEqual(receiptList({messages:[rejected,delivered]}),[rejected,delivered]);
 assert.throws(()=>checkedReceipt({...rejected,contentBytes:1}));assert.throws(()=>receiptList({messages:[delivered,delivered]}));
});
