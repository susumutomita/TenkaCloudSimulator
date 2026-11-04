package aws

import (
 "encoding/json"
 "errors"
 "sync"
 "testing"
)

func dynamoGenerationFixture(t *testing.T)(*Gateway,DynamoDBTable,func()) {
 t.Helper();g,db:=dynamoTestGateway(t,":memory:")
 dynamoCall(t,g,"CreateTable",dynamoSizeTable,200,nil)
 table,err:=g.DynamoDB.GetTable("us-east-1","sizes");if err!=nil{db.Close();t.Fatal(err)}
 return g,table,func(){db.Close()}
}
func dynamoGenerationError(t *testing.T,err error) {
 t.Helper();if err==nil{t.Fatal("stale/missing table operation succeeded")}
 var failure dynamoFailure
 // ResourceNotFound is intentionally externally bounded, not an SQL disclosure.
 if !errors.As(err,&failure)||failure.code!="ResourceNotFoundException"{t.Fatalf("want generation ResourceNotFound got %v",err)}
}
func TestDynamoDBCachedGenerationRejectsMissingAndRecreatedTable(t *testing.T){
 g,old,closeDB:=dynamoGenerationFixture(t);defer closeDB()
 if _,err:=g.DynamoDB.DeleteTable(old.Region,old.Name);err!=nil{t.Fatal(err)}
 body:=`{"id":{"S":"k"}}`;key:="S\x00k"
 dynamoGenerationError(t,g.DynamoDB.PutItem(old,key,"",body,""))
 dynamoGenerationError(t,g.DynamoDB.DeleteItem(old,key,"",""))
 _,err:=g.DynamoDB.deleteTableSnapshot(old);dynamoGenerationError(t,err)
 var orphans int
 if err=g.Store.Database().QueryRow(`SELECT count(*) FROM aws_dynamodb_items`).Scan(&orphans);err!=nil||orphans!=0{t.Fatal("orphan written after delete",orphans,err)}
 // Reuse the name with a DIFFERENT key type; old validated metadata cannot be reused.
 dynamoCall(t,g,"CreateTable",`{"TableName":"sizes","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"N"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`,200,nil)
 fresh,err:=g.DynamoDB.GetTable(old.Region,old.Name);if err!=nil||fresh.TableID==old.TableID{t.Fatal(fresh,err)}
 dynamoCall(t,g,"PutItem",`{"TableName":"sizes","Item":{"id":{"N":"1"},"v":{"S":"keep"}}}`,200,nil)
 dynamoGenerationError(t,g.DynamoDB.PutItem(old,key,"",body,""))
 dynamoGenerationError(t,g.DynamoDB.DeleteItem(old,"N\x001","",""))
 _,err=g.DynamoDB.deleteTableSnapshot(old);dynamoGenerationError(t,err)
 _,_,err=g.DynamoDB.GetItem(old,"N\x001","");dynamoGenerationError(t,err)
 _,_,err=g.DynamoDB.TableStats(old);dynamoGenerationError(t,err)
 got:=dynamoCall(t,g,"GetItem",`{"TableName":"sizes","Key":{"id":{"N":"1.0"}}}`,200,nil)
 if got["Item"].(map[string]any)["v"].(map[string]any)["S"]!="keep"{t.Fatal("stale request changed new generation")}
 dynamoErrorCode(t,dynamoCall(t,g,"PutItem",`{"TableName":"sizes","Item":{"id":{"S":"k"}}}`,400,nil),"ValidationException")
}
func TestDynamoDBConstructorRefusesOrphansWithoutDeletion(t *testing.T){
 g,_,closeDB:=dynamoGenerationFixture(t);defer closeDB()
 db:=g.Store.Database()
 if _,err:=db.Exec(`INSERT INTO aws_dynamodb_items VALUES('us-east-1','missing','pk','','{"id":{"S":"preserve"}}')`);err!=nil{t.Fatal(err)}
 if _,err:=NewDynamoDBStore(db);err==nil{t.Fatal("existing orphan was silently adopted")}
 var retained string
 if err:=db.QueryRow(`SELECT item FROM aws_dynamodb_items WHERE table_name='missing'`).Scan(&retained);err!=nil||retained!=`{"id":{"S":"preserve"}}`{t.Fatal("constructor changed old data",retained,err)}
}
func TestDynamoDBConditionalPutSerializesOneWinner(t *testing.T){
 g,table,closeDB:=dynamoGenerationFixture(t);defer closeDB()
 start:=make(chan struct{});out:=make(chan error,2);var wait sync.WaitGroup
 for _,value:=range []string{"first","second"}{wait.Add(1);go func(value string){defer wait.Done();<-start
  encoded,_:=json.Marshal(map[string]any{"id":map[string]any{"S":"k"},"v":map[string]any{"S":value}})
  out<-g.DynamoDB.PutItem(table,"S\x00k","",string(encoded),"not_exists")
 }(value)}
 close(start);wait.Wait();close(out);success,conditional:=0,0
 for err:=range out{if err==nil{success++}else{failure,ok:=err.(dynamoFailure);if !ok||failure.code!="ConditionalCheckFailedException"{t.Fatal(err)};conditional++}}
 if success!=1||conditional!=1{t.Fatal("conditional check escaped transaction",success,conditional)}
 count,_,err:=g.DynamoDB.TableStats(table);if err!=nil||count!=1{t.Fatal(count,err)}
 if err=g.DynamoDB.DeleteItem(table,"S\x00k","","exists");err!=nil{t.Fatal(err)}
 if err=g.DynamoDB.DeleteItem(table,"S\x00k","","exists");err==nil{t.Fatal("missing conditional delete succeeded")}
 if err=g.DynamoDB.DeleteItem(table,"S\x00k","","not_exists");err!=nil{t.Fatal(err)}
}
func TestDynamoDBSameGenerationSchemaDriftFailsClosed(t *testing.T){
 g,table,closeDB:=dynamoGenerationFixture(t);defer closeDB()
 schema:=`{"keySchema":[{"AttributeName":"id","KeyType":"HASH"}],"definitions":{"id":"N"}}`
 if _,err:=g.Store.Database().Exec(`UPDATE aws_dynamodb_tables SET schema=?`,schema);err!=nil{t.Fatal(err)}
 if err:=g.DynamoDB.PutItem(table,"S\x00k","",`{"id":{"S":"k"}}`,"");err==nil{t.Fatal("mutated cached schema accepted")}
 var count int;if err:=g.Store.Database().QueryRow(`SELECT count(*) FROM aws_dynamodb_items`).Scan(&count);err!=nil||count!=0{t.Fatal(count,err)}
}
func TestDynamoDBKeyRevalidationBeforeStoreMutation(t *testing.T){
 g,table,closeDB:=dynamoGenerationFixture(t);defer closeDB()
 for _,tc:=range []struct{pk,body string}{{"S\x00wrong",`{"id":{"S":"k"}}`},{"S\x00k",`{"id":{"N":"1"}}`},{"S\x00k",`not JSON`}}{
  if err:=g.DynamoDB.PutItem(table,tc.pk,"",tc.body,"");err==nil{t.Fatal("invalid supplied key succeeded",tc)}
 }
 for _,mode:=range []string{"unknown","attribute_exists(id)"}{
  if err:=g.DynamoDB.PutItem(table,"S\x00k","",`{"id":{"S":"k"}}`,mode);err==nil{t.Fatal("unsupported store condition")}
  if err:=g.DynamoDB.DeleteItem(table,"S\x00k","",mode);err==nil{t.Fatal("unsupported delete condition")}
 }
 item,found,err:=g.DynamoDB.GetItem(table,"S\x00k","");if err!=nil||found||item!=nil{t.Fatal(item,found,err)}
 // Corrupt persisted JSON remains an internal error, not a fabricated item.
 if _,err=g.Store.Database().Exec(`INSERT INTO aws_dynamodb_items VALUES(?,?,?,?,?)`,table.Region,table.Name,"S\x00k","",`{"id":{"S":"other"}}`);err!=nil{t.Fatal(err)}
 _,_,err=g.DynamoDB.GetItem(table,"S\x00k","");if err==nil{t.Fatal("corrupt persisted key accepted")}
}
func TestDynamoDBLateCommitFailureRollsBack(t *testing.T){
 g,table,closeDB:=dynamoGenerationFixture(t);defer closeDB();db:=g.Store.Database()
 // Fixture only: a deferred FK is deliberately violated by a late item trigger.
 // No foreign-key schema/migration is added to the product.
 for _,query:=range []string{
  `CREATE TABLE commit_parent(id INTEGER PRIMARY KEY)`,
  `CREATE TABLE commit_child(parent INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
  `CREATE TRIGGER commit_item_fault AFTER INSERT ON aws_dynamodb_items BEGIN INSERT INTO commit_child VALUES(1); END`,
 }{if _,err:=db.Exec(query);err!=nil{t.Fatal("deferred-commit fixture unsupported",err)}}
 before,_,err:=g.DynamoDB.TableStats(table);if err!=nil{t.Fatal(err)}
 if err=g.DynamoDB.PutItem(table,"S\x00k","",`{"id":{"S":"k"}}`,"");err==nil{t.Fatal("deferred SQL commit failure ignored")}
 after,_,err:=g.DynamoDB.TableStats(table);if err!=nil||before!=after{t.Fatal("failed commit changed item count",before,after,err)}
 var children int;if err=db.QueryRow(`SELECT count(*) FROM commit_child`).Scan(&children);err!=nil||children!=0{t.Fatal("trigger rows escaped rollback",children,err)}
}
func TestDynamoDBStoredMetadataValidationAndConstructorFailure(t *testing.T){
 g,table,closeDB:=dynamoGenerationFixture(t);db:=g.Store.Database()
 for _,schema:=range []string{`not JSON`,`null`,`{"keySchema":[],"definitions":{}}`,`{"keySchema":[{"AttributeName":"id","KeyType":"HASH"}],"definitions":{"id":"BAD"}}`}{
  if _,err:=db.Exec(`UPDATE aws_dynamodb_tables SET schema=?`,schema);err!=nil{t.Fatal(err)}
  if _,err:=g.DynamoDB.GetTable(table.Region,table.Name);err==nil{t.Fatal("invalid persisted schema accepted",schema)}
 }
 closeDB();if _,err:=NewDynamoDBStore(db);err==nil{t.Fatal("closed database constructor succeeded")}
}
