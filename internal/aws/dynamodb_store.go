package aws

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// DynamoDBStore owns AWS DynamoDB state in provider-owned SQLite tables.
// It never shares the S3/SSM resource namespaces. Items are stored as
// canonical JSON documents; identity is the canonical typed key encoding
// produced by dynamoKeyComponent, so S/N/B keys never collide.
type DynamoDBStore struct{ db *sql.DB }

const (
	maxDynamoDBTables       = 100
	maxDynamoDBItemsPerTab  = 10000
	maxDynamoDBItemBytes    = 400 << 10 // real DynamoDB item size limit
	maxDynamoDBRequestBytes = 1 << 20
)

// DynamoDBTable is the persisted table definition.
type DynamoDBTable struct {
	Region      string
	Name        string
	TableID     string
	KeySchema   []DynamoDBKeyElement
	Definitions map[string]string // attribute name -> S|N|B
	BillingMode string            // PAY_PER_REQUEST or PROVISIONED
	ReadUnits   int64
	WriteUnits  int64
	Created     float64
}

type DynamoDBKeyElement struct {
	AttributeName string
	KeyType       string // HASH or RANGE
}

func NewDynamoDBStore(db *sql.DB) (*DynamoDBStore, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_dynamodb_tables(region TEXT NOT NULL,name TEXT NOT NULL,table_id TEXT NOT NULL,schema TEXT NOT NULL,billing TEXT NOT NULL,read_units INTEGER NOT NULL,write_units INTEGER NOT NULL,created REAL NOT NULL,PRIMARY KEY(region,name));`); err != nil {
		return nil, err
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_dynamodb_items(region TEXT NOT NULL,table_name TEXT NOT NULL,pk TEXT NOT NULL,sk TEXT NOT NULL,item TEXT NOT NULL,PRIMARY KEY(region,table_name,pk,sk));`)
	if err != nil { return nil, err }
 // Refuse legacy orphan data without migrating, deleting or adopting any row.
 var orphan int
 err = db.QueryRow(`SELECT 1 FROM aws_dynamodb_items i LEFT JOIN aws_dynamodb_tables t ON t.region=i.region AND t.name=i.table_name WHERE t.name IS NULL LIMIT 1`).Scan(&orphan)
 if err == nil { return nil, fmt.Errorf("DynamoDB state contains orphaned items; refusing startup without modifying existing data") }
 if err != sql.ErrNoRows { return nil, err }
 return &DynamoDBStore{db}, nil
}

func (t *DynamoDBTable) PartitionKey() string {
	for _, k := range t.KeySchema {
		if k.KeyType == "HASH" {
			return k.AttributeName
		}
	}
	return ""
}

func (t *DynamoDBTable) SortKey() string {
	for _, k := range t.KeySchema {
		if k.KeyType == "RANGE" {
			return k.AttributeName
		}
	}
	return ""
}

// KeyFor extracts the canonical (pk, sk) pair from a key attribute set
// (a full item for PutItem, or a Key object for Get/DeleteItem).
func (t *DynamoDBTable) KeyFor(attrs map[string]any) (string, string, error) {
	pkName := t.PartitionKey()
	pk, err := dynamoKeyComponent(attrs[pkName], t.Definitions[pkName])
	if err != nil {
		return "", "", dynamoFailure{"ValidationException", fmt.Sprintf("Type mismatch or missing value for key attribute %s.", pkName)}
	}
	sk := ""
	if skName := t.SortKey(); skName != "" {
		sk, err = dynamoKeyComponent(attrs[skName], t.Definitions[skName])
		if err != nil {
			return "", "", dynamoFailure{"ValidationException", fmt.Sprintf("Type mismatch or missing value for key attribute %s.", skName)}
		}
	}
	return pk, sk, nil
}

func (s *DynamoDBStore) CreateTable(t DynamoDBTable) (DynamoDBTable, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return DynamoDBTable{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT name FROM aws_dynamodb_tables WHERE region=? AND name=?", t.Region, t.Name).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return DynamoDBTable{}, err
	}
	if err == nil {
		return DynamoDBTable{}, dynamoFailure{"ResourceInUseException", "Table already exists: " + t.Name}
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_dynamodb_tables").Scan(&count); err != nil {
		return DynamoDBTable{}, err
	}
	if count >= maxDynamoDBTables {
		return DynamoDBTable{}, dynamoFailure{"LimitExceededException", "Simulator DynamoDB table limit reached."}
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return DynamoDBTable{}, err
	}
	t.TableID = hex.EncodeToString(idBytes)
	t.Created = float64(time.Now().UnixMilli()) / 1000
	schema, err := json.Marshal(map[string]any{"keySchema": t.KeySchema, "definitions": t.Definitions})
	if err != nil {
		return DynamoDBTable{}, err
	}
	if _, err = tx.Exec(`INSERT INTO aws_dynamodb_tables VALUES(?,?,?,?,?,?,?,?)`, t.Region, t.Name, t.TableID, string(schema), t.BillingMode, t.ReadUnits, t.WriteUnits, t.Created); err != nil {
		return DynamoDBTable{}, err
	}
	return t, tx.Commit()
}

func (s *DynamoDBStore) GetTable(region, name string) (DynamoDBTable, error) {
 return readDynamoTable(s.db, region, name)
}

func (s *DynamoDBStore) ListTables(region, startAfter string, limit int) ([]string, string, error) {
	rows, err := s.db.Query("SELECT name FROM aws_dynamodb_tables WHERE region=? ORDER BY name", region)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, "", err
		}
		if n > startAfter {
			names = append(names, n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	last := ""
	if limit > 0 && len(names) > limit {
		last = names[limit-1]
		names = names[:limit]
	}
	return names, last, nil
}

func (s *DynamoDBStore) DeleteTable(region, name string) (DynamoDBTable, error) {
 table, err := s.GetTable(region, name)
 if err != nil { return DynamoDBTable{}, err }
 return s.deleteTableSnapshot(table)
}

func (s *DynamoDBStore) deleteTableSnapshot(table DynamoDBTable) (DynamoDBTable, error) {
 tx, err := s.db.Begin()
 if err != nil { return DynamoDBTable{}, err }
 defer tx.Rollback()
 if err = checkDynamoGeneration(tx, table); err != nil { return DynamoDBTable{}, err }
 if _, err = tx.Exec("DELETE FROM aws_dynamodb_items WHERE region=? AND table_name=?", table.Region, table.Name); err != nil { return DynamoDBTable{}, err }
 if _, err = tx.Exec("DELETE FROM aws_dynamodb_tables WHERE region=? AND name=? AND table_id=?", table.Region, table.Name, table.TableID); err != nil { return DynamoDBTable{}, err }
 if err = tx.Commit(); err != nil { return DynamoDBTable{}, err }
 return table, nil
}

func (s *DynamoDBStore) TableStats(table DynamoDBTable) (int64, int64, error) {
 tx, err := s.db.Begin()
 if err != nil { return 0, 0, err }
 defer tx.Rollback()
 if err = checkDynamoGeneration(tx, table); err != nil { return 0, 0, err }
 rows, err := tx.Query("SELECT item FROM aws_dynamodb_items WHERE region=? AND table_name=?", table.Region, table.Name)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var count, size int64
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return 0, 0, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(encoded), &item); err != nil {
			return 0, 0, fmt.Errorf("invalid stored DynamoDB item: %v", err)
		}
		n, err := dynamoItemSize(item)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid stored DynamoDB item: %v", err)
		}
		count++
		size += n
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
 if err = rows.Close(); err != nil { return 0, 0, err }
 if err = tx.Commit(); err != nil { return 0, 0, err }
 return count, size, nil
}

// PutItem stores itemJSON under (pk, sk). condition is "", "exists" or
// "not_exists" and is evaluated against the same key inside the transaction.
func (s *DynamoDBStore) PutItem(snapshot DynamoDBTable, pk, sk, itemJSON, condition string) error {
 region, table := snapshot.Region, snapshot.Name
 if err := validDynamoConditionMode(condition); err != nil { return err }
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
 if err = checkDynamoGeneration(tx, snapshot); err != nil { return err }
 var item map[string]any
 if err = json.Unmarshal([]byte(itemJSON), &item); err != nil { return fmt.Errorf("invalid DynamoDB item document") }
 actualPK, actualSK, err := snapshot.KeyFor(item)
 if err != nil { return err }
 if actualPK != pk || actualSK != sk { return dynamoFailure{"ValidationException", "Stored key does not match item attributes"} }

	var n int
	if err := tx.QueryRow("SELECT count(*) FROM aws_dynamodb_items WHERE region=? AND table_name=? AND pk=? AND sk=?", region, table, pk, sk).Scan(&n); err != nil {
		return err
	}
	exists := n > 0
	if condition == "exists" && !exists || condition == "not_exists" && exists {
		return dynamoFailure{"ConditionalCheckFailedException", "The conditional request failed"}
	}
	if !exists {
		var total int
		if err := tx.QueryRow("SELECT count(*) FROM aws_dynamodb_items WHERE region=? AND table_name=?", region, table).Scan(&total); err != nil {
			return err
		}
		if total >= maxDynamoDBItemsPerTab {
			return dynamoFailure{"LimitExceededException", "Simulator DynamoDB per-table item limit reached."}
		}
	}
	if _, err = tx.Exec(`INSERT INTO aws_dynamodb_items VALUES(?,?,?,?,?) ON CONFLICT(region,table_name,pk,sk) DO UPDATE SET item=excluded.item`, region, table, pk, sk, itemJSON); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DynamoDBStore) GetItem(snapshot DynamoDBTable, pk, sk string) (map[string]any, bool, error) {
 tx, err := s.db.Begin()
 if err != nil { return nil, false, err }
 defer tx.Rollback()
 if err = checkDynamoGeneration(tx, snapshot); err != nil { return nil, false, err }
 var raw string
 err = tx.QueryRow("SELECT item FROM aws_dynamodb_items WHERE region=? AND table_name=? AND pk=? AND sk=?", snapshot.Region, snapshot.Name, pk, sk).Scan(&raw)
 if err == sql.ErrNoRows { return nil, false, tx.Commit() }
 if err != nil { return nil, false, err }
 var item map[string]any
 if json.Unmarshal([]byte(raw), &item) != nil { return nil, false, fmt.Errorf("invalid stored DynamoDB item") }
 if _, err = dynamoItemSize(item); err != nil { return nil, false, fmt.Errorf("invalid stored DynamoDB item") }
 actualPK, actualSK, err := snapshot.KeyFor(item)
 if err != nil || actualPK != pk || actualSK != sk { return nil, false, fmt.Errorf("invalid stored DynamoDB key") }
 if err = tx.Commit(); err != nil { return nil, false, err }
 return item, true, nil
}

// DeleteItem removes the item if present. Deleting a missing item succeeds,
// matching real DynamoDB, unless condition "exists" is requested.
func (s *DynamoDBStore) DeleteItem(snapshot DynamoDBTable, pk, sk, condition string) error {
 region, table := snapshot.Region, snapshot.Name
 if err := validDynamoConditionMode(condition); err != nil { return err }
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
 if err = checkDynamoGeneration(tx, snapshot); err != nil { return err }

	var n int
	if err := tx.QueryRow("SELECT count(*) FROM aws_dynamodb_items WHERE region=? AND table_name=? AND pk=? AND sk=?", region, table, pk, sk).Scan(&n); err != nil {
		return err
	}
	if condition == "exists" && n == 0 || condition == "not_exists" && n > 0 {
		return dynamoFailure{"ConditionalCheckFailedException", "The conditional request failed"}
	}
	if _, err = tx.Exec("DELETE FROM aws_dynamodb_items WHERE region=? AND table_name=? AND pk=? AND sk=?", region, table, pk, sk); err != nil {
		return err
	}
	return tx.Commit()
}
