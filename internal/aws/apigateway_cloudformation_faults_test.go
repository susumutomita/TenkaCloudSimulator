package aws

import (
	"context"
	"strings"
	"testing"
)

// A failed fingerprint write or late native insert cannot leave a partially
// owned API graph. Existing native resources outside that graph remain intact.
func TestCFNAPIPersistenceFailuresAreAtomic(t *testing.T) {
	for _, fault := range []string{"resource-insert", "snapshot-insert", "fingerprint-save"} {
		t.Run(fault, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			apis, err := NewAPIGatewayStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := apis.CreateAPI(context.Background(), "us-east-1", "foreign-native", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			record, err := compileStack("us-east-1", "failed-api", apiStackTemplate, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			sql := `CREATE TRIGGER api_persistence_fault BEFORE INSERT ON aws_apigateway_resources WHEN NEW.api_id!='` + foreign.ID + `' BEGIN SELECT RAISE(ABORT,'native resource insertion fault'); END`
			if fault == "snapshot-insert" {
				sql = `CREATE TRIGGER api_persistence_fault BEFORE INSERT ON aws_apigateway_deployments BEGIN SELECT RAISE(ABORT,'snapshot insertion fault'); END`
			}
			if fault == "fingerprint-save" {
				sql = `CREATE TRIGGER api_persistence_fault BEFORE UPDATE ON aws_cfn_stacks BEGIN SELECT RAISE(ABORT,'fingerprint persistence fault'); END`
			}
			if _, err = db.Database().Exec(sql); err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err == nil {
				t.Fatal("failed native graph persistence reported success")
			}
			if rows, err := stacks.Records(record.Region, ""); err != nil || len(rows) != 0 {
				t.Fatal("partial stack retained", rows, err)
			}
			var claims int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_cfn_ownership").Scan(&claims); err != nil || claims != 0 {
				t.Fatal("partial ownership retained", claims, err)
			}
			listed, err := apis.APIs(context.Background(), record.Region, "", 100)
			if err != nil || len(listed) != 1 || listed[0].ID != foreign.ID {
				t.Fatal("failed stack damaged foreign native API", listed, err)
			}
			if _, err = db.Database().Exec("DROP TRIGGER api_persistence_fault"); err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err != nil {
				t.Fatal("create did not retry after repair", err)
			}
			if err = stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCFNAPIMissingStateCannotAuthorizeDestructiveCleanup(t *testing.T) {
	for _, table := range []string{"aws_apigateway_resources", "aws_apigateway_deployments", "aws_apigateway_stages"} {
		t.Run(table, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			record, err := compileStack("us-east-1", "missing-api-state", apiStackTemplate, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Database().Exec("ALTER TABLE " + table + " RENAME TO saved_native_state"); err != nil {
				t.Fatal(err)
			}
			if err = stacks.Delete(record.Region, record.ID); err == nil {
				t.Fatal("unreadable native state treated as absent owned state")
			}
			rows, err := stacks.Records(record.Region, record.ID)
			if err != nil || len(rows) != 1 {
				t.Fatal("failed cleanup forgot stack", rows, err)
			}
			var claims int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?", record.ID).Scan(&claims); err != nil || claims != len(record.Resources) {
				t.Fatal("failed cleanup released ownership", claims, err)
			}
			if _, err = db.Database().Exec("ALTER TABLE saved_native_state RENAME TO " + table); err != nil {
				t.Fatal(err)
			}
			if err = stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal("cleanup failed after state restoration", err)
			}
		})
	}
}

func TestCFNAPITagsCannotSilentlyCoerceUnsupportedValues(t *testing.T) {
	for _, tags := range []string{"true", "[{Key: purpose, Value: true}]", "[{Key: purpose, Value: dummy, Extra: forbidden}]", "[{Key: purpose, Value: one}, {Key: purpose, Value: two}]"} {
		t.Run(tags, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			template := strings.Replace(apiStackTemplate, "Name: local-cfn-api", "Name: local-cfn-api\n      Tags: "+tags, 1)
			record, err := compileStack("us-east-1", "invalid-api-tags", template, nil, "")
			if err == nil {
				err = stacks.Create(record)
			}
			if err == nil {
				t.Fatal("invalid tag metadata was silently stored")
			}
			var count int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_apigateway_apis").Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid metadata left native API", count, err)
			}
		})
	}
}
