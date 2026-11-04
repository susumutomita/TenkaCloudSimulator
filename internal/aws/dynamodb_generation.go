package aws

import (
 "database/sql"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "reflect"
)

type dynamoTableQuery interface { QueryRow(string, ...any) *sql.Row }

func readDynamoTable(query dynamoTableQuery, region, name string) (DynamoDBTable, error) {
	var t DynamoDBTable
	var schema string
	err := query.QueryRow("SELECT name,table_id,schema,billing,read_units,write_units,created FROM aws_dynamodb_tables WHERE region=? AND name=?", region, name).
		Scan(&t.Name, &t.TableID, &schema, &t.BillingMode, &t.ReadUnits, &t.WriteUnits, &t.Created)
	if err == sql.ErrNoRows {
		return DynamoDBTable{}, dynamoFailure{"ResourceNotFoundException", "Cannot do operations on a non-existent table"}
	}
	if err != nil {
		return DynamoDBTable{}, err
	}
	t.Region = region
	var decoded struct {
		KeySchema   []DynamoDBKeyElement `json:"keySchema"`
		Definitions map[string]string    `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		return DynamoDBTable{}, err
	}
	t.KeySchema, t.Definitions = decoded.KeySchema, decoded.Definitions
 if err := validateDynamoTableMetadata(t); err != nil { return DynamoDBTable{}, err }
	return t, nil
}


// A validated schema belongs to one immutable TableID. Compare under the same
// SQLite transaction as condition evaluation and item/table mutation.
func checkDynamoGeneration(tx *sql.Tx, expected DynamoDBTable) error {
 current, err := readDynamoTable(tx, expected.Region, expected.Name)
 if err != nil { return err }
 if expected.TableID == "" || current.TableID != expected.TableID { return dynamoFailure{"ResourceNotFoundException", "The table generation no longer exists"} }
 if !reflect.DeepEqual(expected.KeySchema,current.KeySchema) || !reflect.DeepEqual(expected.Definitions,current.Definitions) { return fmt.Errorf("DynamoDB table schema changed within one generation") }
 return nil
}
func validateDynamoTableMetadata(table DynamoDBTable) error {
 invalid := func() error { return fmt.Errorf("invalid stored DynamoDB table metadata") }
 if len(table.TableID)!=32 { return invalid() }
 if _,err:=hex.DecodeString(table.TableID);err!=nil{return invalid()}
 if len(table.KeySchema)<1||len(table.KeySchema)>2||len(table.Definitions)!=len(table.KeySchema){return invalid()}
 names:=map[string]bool{};hash,sort:=0,0
 for _,key:=range table.KeySchema {
  kind:=table.Definitions[key.AttributeName]
  if key.AttributeName==""||names[key.AttributeName]||(kind!="S"&&kind!="N"&&kind!="B"){return invalid()};names[key.AttributeName]=true
  switch key.KeyType{case "HASH":hash++;case "RANGE":sort++;default:return invalid()}
 }
 if hash!=1||sort>1{return invalid()};return nil
}
func validDynamoConditionMode(condition string)error {
 if condition!=""&&condition!="exists"&&condition!="not_exists" { return dynamoFailure{"ValidationException","Unsupported conditional mode"} }
 return nil
}
