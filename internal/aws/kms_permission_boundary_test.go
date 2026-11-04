package aws

import (
 "encoding/json"
 "net/http/httptest"
 "strings"
 "testing"
)

func TestKMSNativeOperationPermissionsUseIndependentLiteralResources(t *testing.T){
 _,s:=kmsTestStore(t,"")
 invoke:=func(op,body,action,resource string,allowed bool)(*httptest.ResponseRecorder,map[string]json.RawMessage){
  t.Helper();r:=httptest.NewRequest("POST","/",strings.NewReader(body));r.Header.Set("Content-Type","application/x-amz-json-1.1");r.Header.Set("X-Amz-Target","TrentService."+op);w:=httptest.NewRecorder();calls:=0
  s.ServeHTTP(w,r,"us-east-1",func(actualAction,actualResource string)bool{calls++;if actualAction!=action || actualResource!=resource{t.Fatalf("wrong action/resource for %s: %s %s",op,actualAction,actualResource)};return allowed})
  if calls!=1{t.Fatal("permission check omitted/repeated",op,calls)};out:=map[string]json.RawMessage{};if w.Body.Len()>0{if err:=json.Unmarshal(w.Body.Bytes(),&out);err!=nil{t.Fatal(err)}};return w,out
 }
 w,out:=invoke("CreateKey",`{}`,"kms:CreateKey","*",true);if w.Code!=200{t.Fatal(w.Code,w.Body.String())};metadata:=map[string]json.RawMessage{};if err:=json.Unmarshal(out["KeyMetadata"],&metadata);err!=nil{t.Fatal(err)}
 var id,arn string;if err:=json.Unmarshal(metadata["KeyId"],&id);err!=nil{t.Fatal(err)};if err:=json.Unmarshal(metadata["Arn"],&arn);err!=nil{t.Fatal(err)};expectedARN:="arn:aws:kms:us-east-1:000000000000:key/"+id;if arn!=expectedARN || string(metadata["AWSAccountId"])!=`"000000000000"`{t.Fatal("incorrect literal metadata ARN/account fields")}
 ciphertext:=""
 for _,c:=range []struct{op,action,body,resource string}{
  {"DescribeKey","kms:DescribeKey",`{"KeyId":"`+id+`"}`,expectedARN},
  {"ListKeys","kms:ListKeys",`{"Limit":1}`,"*"},
  {"GetKeyPolicy","kms:GetKeyPolicy",`{"KeyId":"`+id+`"}`,expectedARN},
  {"DisableKey","kms:DisableKey",`{"KeyId":"`+id+`"}`,expectedARN},
  {"EnableKey","kms:EnableKey",`{"KeyId":"`+id+`"}`,expectedARN},
  {"Encrypt","kms:Encrypt",`{"KeyId":"`+arn+`","Plaintext":"bG9jYWw="}`,expectedARN},
 }{
  before:=kmsStateFingerprint(t,s);refused,_:=invoke(c.op,c.body,c.action,c.resource,false);if refused.Code!=403 || !strings.Contains(refused.Body.String(),`"AccessDeniedException"`){t.Fatal("missing permission admitted",c.op,refused.Code)};if kmsStateFingerprint(t,s)!=before{t.Fatal("authorization refusal changed ledger",c.op)}
  accepted,result:=invoke(c.op,c.body,c.action,c.resource,true);if accepted.Code!=200{t.Fatal("authorized operation failed",c.op,accepted.Code,accepted.Body.String())};if c.op=="Encrypt"{ciphertext=string(result["CiphertextBlob"]);if ciphertext==""{t.Fatal("actual encryption omitted")}}
 }
 before:=kmsStateFingerprint(t,s);body:=`{"CiphertextBlob":`+ciphertext+`}`;denied,_:=invoke("Decrypt",body,"kms:Decrypt",expectedARN,false);if denied.Code!=403 || kmsStateFingerprint(t,s)!=before{t.Fatal("ciphertext-derived authority refusal changed state")}
 accepted,result:=invoke("Decrypt",body,"kms:Decrypt",expectedARN,true);if accepted.Code!=200 || string(result["Plaintext"])!=`"bG9jYWw="`{t.Fatal("authorized actual decryption failed",accepted.Code,accepted.Body.String())};if kmsStateFingerprint(t,s)!=before{t.Fatal("decryption changed persistent material/counter")}
 refused,_:=invoke("CreateKey",`{}`,"kms:CreateKey","*",false);if refused.Code!=403 || kmsStateFingerprint(t,s)!=before{t.Fatal("CreateKey permission denial changed ledger")}
}
