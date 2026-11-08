package aws

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestDynamoDBTableQuotaRejectsWithoutExtraState(t *testing.T) {
	g, base, closeDB := dynamoGenerationFixture(t)
	defer closeDB()
	for i := 1; i < maxDynamoDBTables; i++ {
		table := base
		table.Name = fmt.Sprintf("quota%03d", i)
		if _, err := g.DynamoDB.CreateTable(table); err != nil {
			t.Fatal("quota fixture create", i, err)
		}
	}
	table := base
	table.Name = "quotaOverflow"
	if _, err := g.DynamoDB.CreateTable(table); err == nil {
		t.Fatal("table quota exceeded without error")
	} else {
		failure, ok := err.(dynamoFailure)
		if !ok || failure.code != "LimitExceededException" {
			t.Fatal(err)
		}
	}
	var count int
	if err := g.Store.Database().QueryRow(`SELECT count(*) FROM aws_dynamodb_tables`).Scan(&count); err != nil || count != maxDynamoDBTables {
		t.Fatal(count, err)
	}
}
func TestDynamoDBItemQuotaAllowsReplacementButRejectsNewItem(t *testing.T) {
	g, table, closeDB := dynamoGenerationFixture(t)
	defer closeDB()
	db := g.Store.Database()
	// Native SQLite fixtures use correctly typed, matching primary-key JSON. No
	// production quota is lowered and no 4GiB item allocation is required.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxDynamoDBItemsPerTab; i++ {
		name := fmt.Sprintf("key%05d", i)
		body, _ := json.Marshal(map[string]any{"id": map[string]any{"S": name}})
		if _, err = tx.Exec(`INSERT INTO aws_dynamodb_items VALUES(?,?,?,?,?)`, table.Region, table.Name, "S\x00"+name, "", string(body)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = g.DynamoDB.PutItem(table, "S\x00fresh", "", `{"id":{"S":"fresh"}}`, ""); err == nil {
		t.Fatal("new item exceeded quota")
	} else {
		failure, ok := err.(dynamoFailure)
		if !ok || failure.code != "LimitExceededException" {
			t.Fatal(err)
		}
	}
	if err = g.DynamoDB.PutItem(table, "S\x00key00000", "", `{"id":{"S":"key00000"},"v":{"S":"replaced"}}`, ""); err != nil {
		t.Fatal("replacement incorrectly hit quota", err)
	}
	count, _, err := g.DynamoDB.TableStats(table)
	if err != nil || count != maxDynamoDBItemsPerTab {
		t.Fatal(count, err)
	}
	item, found, err := g.DynamoDB.GetItem(table, "S\x00key00000", "")
	if err != nil || !found || item["v"].(map[string]any)["S"] != "replaced" {
		t.Fatal(item, found, err)
	}
}
