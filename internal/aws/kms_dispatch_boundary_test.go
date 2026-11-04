package aws

import (
 "context"
 "encoding/json"
 "net/http/httptest"
 "strings"
 "sync"
 "time"
 "testing"

 "github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestKMSIdentityStoreOutageIsInfrastructureAndRetryPreservesKeys(t *testing.T){
 db,s:=kmsTestStore(t,"");ids:=core.NewIdentities(db);g:=&Gateway{Store:db,KMS:s,Identities:ids}
 key,err:=s.CreateKey(context.Background(),"us-east-1","private-key-must-not-leak");if err!=nil{t.Fatal(err)}
 if err=ids.Set("kms-learner",core.Policy{Version:"2012-10-17",Statement:core.Statements{{Effect:"Allow",Action:core.StringList{"kms:DescribeKey"},Resource:core.StringList{key.Arn}}}});err!=nil{t.Fatal(err)}
 before:=kmsStateFingerprint(t,s)
 w,_:=kmsHTTP(t,g,"DescribeKey",`{"KeyId":"`+key.KeyID+`"}`,"kms-learner");if w.Code!=200{t.Fatal("positive authorized read failed",w.Code)}
 if _,err=s.db.Exec("ALTER TABLE identity_policies RENAME TO private_unavailable_policies");err!=nil{t.Fatal(err)}
 w,out:=kmsHTTP(t,g,"DescribeKey",`{"KeyId":"`+key.KeyID+`"}`,"kms-learner")
 // This must fail before the fix: old dispatch returned403 for any DB error.
 kmsNativeInternalFailure(t,w,out)
 if strings.Contains(w.Body.String(),"identity_policies") || strings.Contains(w.Body.String(),key.KeyID){t.Fatal("identity outage leaked lookup/resource information")}
 if kmsStateFingerprint(t,s)!=before{t.Fatal("policy outage changed saved keys")}
 if _,err=s.db.Exec("ALTER TABLE private_unavailable_policies RENAME TO identity_policies");err!=nil{t.Fatal(err)}
 w,_=kmsHTTP(t,g,"DescribeKey",`{"KeyId":"`+key.KeyID+`"}`,"kms-learner");if w.Code!=200 || kmsStateFingerprint(t,s)!=before{t.Fatal("clean identity-store retry failed or changed state",w.Code)}
 w,_=kmsHTTP(t,g,"DescribeKey",`{"KeyId":"`+key.KeyID+`"}`,"missing-identity");if w.Code!=403 || kmsStateFingerprint(t,s)!=before{t.Fatal("unknown identity must remain ordinary refusal")}
 if err=ids.Set("kms-denied",core.Policy{Version:"2012-10-17",Statement:core.Statements{{Effect:"Deny",Action:core.StringList{"kms:*"},Resource:core.StringList{"*"}}}});err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithCancel(context.Background());cancel();r:=httptest.NewRequest("POST","/",strings.NewReader(`{}`)).WithContext(ctx);r.Header.Set("Content-Type","application/x-amz-json-1.1");r.Header.Set("X-Amz-Target","TrentService.ListKeys");r.Header.Set("X-Simulator-Principal","kms-denied");response:=httptest.NewRecorder();g.ServeHTTP(response,r)
 result:=map[string]json.RawMessage{};if err=json.Unmarshal(response.Body.Bytes(),&result);err!=nil{t.Fatal(err)};kmsNativeInternalFailure(t,response,result)
 if kmsStateFingerprint(t,s)!=before{t.Fatal("canceled policy read changed saved keys")}
}
func TestKMSSigningMetadataMissingTargetCannotFallThroughToS3(t *testing.T){
 db,s:=kmsTestStore(t,"");s3,err:=NewS3Store(db.Database());if err!=nil{t.Fatal(err)};parameters,err:=NewParameterStore(db.Database());if err!=nil{t.Fatal(err)};queues,err:=NewSQSStore(db.Database());if err!=nil{t.Fatal(err)}
 g:=&Gateway{Store:db,S3:s3,KMS:s,Parameters:parameters,Queues:queues}
 if err=db.Create("kms-route-peer","us-east-1");err!=nil{t.Fatal(err)};if _,err=db.Put("kms-route-peer","sentinel","text/plain",[]byte("private-s3-peer"),"","");err!=nil{t.Fatal(err)}
 if _,err=s.CreateKey(context.Background(),"us-east-1","private-kms-key");err!=nil{t.Fatal(err)};before:=kmsStateFingerprint(t,s)
 signature:=func(service,key string)string{return "AWS4-HMAC-SHA256 Credential="+key+"/20261104/us-east-1/"+service+"/aws4_request, SignedHeaders=host, Signature=dummy"}
 for _,c:=range []struct{name,method,target,service,auth string;status int}{
  {"signed-get-missing-target","GET","","",signature("kms","TCSIMLOCAL"),400},
  {"signed-post-missing-target","POST","","",signature("kms","TCSIMLOCAL"),400},
  {"signed-wrong-ssm-target","POST","AmazonSSM.GetParameter","",signature("kms","TCSIMLOCAL"),400},
  {"signed-wrong-sqs-target","POST","AmazonSQS.ListQueues","",signature("kms","TCSIMLOCAL"),400},
  {"real-credential-kms-intent","GET","","",signature("kms","REALKEY"),403},
  {"header-missing-target","GET","","kms","",400},
  {"target-conflicts-with-signing","POST","TrentService.ListKeys","",signature("ssm","TCSIMLOCAL"),403},
  {"service-header-conflict","POST","TrentService.ListKeys","ssm","",400},
  {"service-header-signing-conflict","GET","","s3",signature("kms","TCSIMLOCAL"),400},
 }{t.Run(c.name,func(t *testing.T){
  r:=httptest.NewRequest(c.method,"/",strings.NewReader(`{}`));r.Header.Set("Content-Type","application/x-amz-json-1.1");if c.target!=""{r.Header.Set("X-Amz-Target",c.target)};if c.service!=""{r.Header.Set("X-Simulator-Service",c.service)};if c.auth!=""{r.Header.Set("Authorization",c.auth)};w:=httptest.NewRecorder();g.ServeHTTP(w,r)
  if w.Code!=c.status{t.Fatalf("KMS intent incorrectly routed: status%d want%d %s",w.Code,c.status,w.Body.String())}
  var result struct{Type string `json:"__type"`;Message string `json:"message"`};if err:=json.Unmarshal(w.Body.Bytes(),&result);err!=nil{t.Fatal("KMS intent returned non-KMS wire",w.Body.String())}
  want:="ValidationException";if c.status==403{want="AccessDeniedException"};if result.Type!=want || strings.Contains(w.Body.String(),"ListAllMyBuckets") || strings.Contains(w.Body.String(),"kms-route-peer") || strings.Contains(w.Body.String(),"private-s3-peer"){t.Fatal("routing refusal exposed wrong service/resource")}
  if c.status==400 && result.Message!="Invalid or unsupported KMS request fields."{t.Fatal("wrong service supplied coincidentally400",result.Message)}
  if kmsStateFingerprint(t,s)!=before{t.Fatal("routing refusal changed KMS state")};object,e:=db.Get("kms-route-peer","sentinel");if e!=nil || string(object.Data)!="private-s3-peer"{t.Fatal("routing refusal changed S3 peer")}
 })}
 // A genuine S3 request remains S3 after tightening KMS intent recognition.
 for _,auth:=range []string{"",signature("s3","TCSIMLOCAL")}{r:=httptest.NewRequest("GET","/",nil);r.Header.Set("Authorization",auth);w:=httptest.NewRecorder();g.ServeHTTP(w,r);if w.Code!=200 || !strings.Contains(w.Body.String(),"ListAllMyBucketsResult") || !strings.Contains(w.Body.String(),"kms-route-peer"){t.Fatal("ordinary S3 routing regressed",w.Code,w.Body.String())}}
}

// The barrier is the real HTTP body reader, which runs after dispatch captures
// policy and before native authorization. It is not a replacement authorizer.
type kmsPolicyBodyBarrier struct{reader *strings.Reader;started,resume chan struct{};once sync.Once}
func(b *kmsPolicyBodyBarrier)Read(p []byte)(int,error){b.once.Do(func(){close(b.started);<-b.resume});return b.reader.Read(p)}
func TestKMSNativePolicySnapshotRemainsImmutableAcrossBodyRead(t *testing.T){
 for _,initialAllow:=range []bool{true,false}{t.Run(map[bool]string{true:"revoke-after-snapshot",false:"grant-after-snapshot"}[initialAllow],func(t *testing.T){
  db,s:=kmsTestStore(t,"");ids:=core.NewIdentities(db);g:=&Gateway{KMS:s,Identities:ids};key,err:=s.CreateKey(context.Background(),"us-east-1","dummy");if err!=nil{t.Fatal(err)}
  set:=func(allow bool){effect:="Deny";if allow{effect="Allow"};if e:=ids.Set("snapshot-user",core.Policy{Version:"2012-10-17",Statement:core.Statements{{Effect:effect,Action:core.StringList{"kms:DescribeKey"},Resource:core.StringList{key.Arn}}}});e!=nil{t.Fatal(e)}}
  set(initialAllow);before:=kmsStateFingerprint(t,s)
  barrier:=&kmsPolicyBodyBarrier{reader:strings.NewReader(`{"KeyId":"`+key.KeyID+`"}`),started:make(chan struct{}),resume:make(chan struct{})};r:=httptest.NewRequest("POST","/",barrier);r.Header.Set("Content-Type","application/x-amz-json-1.1");r.Header.Set("X-Amz-Target","TrentService.DescribeKey");r.Header.Set("X-Simulator-Principal","snapshot-user");w:=httptest.NewRecorder();done:=make(chan struct{});go func(){g.ServeHTTP(w,r);close(done)}();var releaseOnce sync.Once;releaseBody:=func(){releaseOnce.Do(func(){close(barrier.resume)})};defer func(){releaseBody();select{case <-done:case <-time.After(5*time.Second):t.Error("owned snapshot request did not terminate")}}()
  select{case <-barrier.started:case <-time.After(5*time.Second):releaseBody();t.Fatal("request did not reach body reader after policy capture")}
  set(!initialAllow);releaseBody()
  select{case <-done:case <-time.After(5*time.Second):t.Fatal("request failed to complete after body release")}
  want:=403;if initialAllow{want=200};if w.Code!=want{t.Fatal("inflight request adopted changed policy",w.Code,want)}
  next,_:=kmsHTTP(t,g,"DescribeKey",`{"KeyId":"`+key.KeyID+`"}`,"snapshot-user");nextWant:=200;if initialAllow{nextWant=403};if next.Code!=nextWant{t.Fatal("subsequent request did not observe changed policy",next.Code,nextWant)}
  if kmsStateFingerprint(t,s)!=before{t.Fatal("policy snapshot requests changed persisted keys")}
 })}
}
