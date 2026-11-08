package aws

import (
	"strings"
	"testing"
)

// Completion-event, history and pruning writes are part of cleanup. Their
// triggers must finish before final resource/stack/claim acknowledgement checks.
func TestStackEventsLateWritesCannotResurrectCleanup(t *testing.T) {
	for _, phase := range []string{"completion-event", "sequence-update", "deleted-update", "history-prune", "event-prune", "stack-residue", "claim-residue"} {
		t.Run(phase, func(t *testing.T) {
			g, db := eventFixture(t, "")
			defer db.Close()
			if strings.Contains(phase, "prune") {
				for i := 0; i < stackDeletedHistoryLimit; i++ {
					r, err := compileStack("us-east-1", "archived", eventTemplate, nil, "")
					if err != nil {
						t.Fatal(err)
					}
					if err = g.Stacks.Create(r); err != nil {
						t.Fatal(err)
					}
					if err = g.Stacks.Delete(r.Region, r.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if w := cleanupACKQuery(g, "CreateStack", "events", eventTemplate); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			// SQL fixture backups are inert until the deliberately injected late write.
			for _, q := range []string{
				`CREATE TABLE event_fault_parameter AS SELECT * FROM aws_ssm_parameters WHERE name='/events/parameter'`,
				`CREATE TABLE event_fault_stack AS SELECT * FROM aws_cfn_stacks WHERE name='events'`,
				`CREATE TABLE event_fault_claim AS SELECT * FROM aws_cfn_ownership WHERE physical_id='/events/parameter'`,
			} {
				if _, err := db.Database().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			trigger := `AFTER INSERT ON aws_cfn_events WHEN NEW.status='DELETE_COMPLETE' AND NEW.resource_type='AWS::CloudFormation::Stack'`
			action := `INSERT INTO aws_ssm_parameters SELECT * FROM event_fault_parameter;`
			switch phase {
			case "sequence-update":
				trigger = `AFTER UPDATE OF last_sequence ON aws_cfn_event_history WHEN EXISTS(SELECT 1 FROM aws_cfn_events WHERE sequence=NEW.last_sequence AND status='DELETE_COMPLETE' AND resource_type='AWS::CloudFormation::Stack')`
			case "deleted-update":
				trigger = `AFTER UPDATE OF deleted ON aws_cfn_event_history WHEN NEW.deleted=1`
			case "history-prune":
				trigger = `AFTER DELETE ON aws_cfn_event_history WHEN OLD.stack_name='archived'`
			case "event-prune":
				trigger = `AFTER DELETE ON aws_cfn_events WHEN OLD.status='CREATE_IN_PROGRESS' AND OLD.resource_type='AWS::CloudFormation::Stack'`
			case "stack-residue":
				action = `INSERT INTO aws_cfn_stacks SELECT * FROM event_fault_stack;`
			case "claim-residue":
				action = `INSERT INTO aws_cfn_stacks SELECT * FROM event_fault_stack; INSERT INTO aws_cfn_ownership SELECT * FROM event_fault_claim;`
			}
			if _, err := db.Database().Exec("CREATE TRIGGER late_event_resurrection " + trigger + " BEGIN " + action + " END"); err != nil {
				t.Fatal(err)
			}
			before := cleanupACKFrame(t, db.Database())
			w := cleanupACKQuery(g, "DeleteStack", "events", "")
			if w.Code != 500 {
				t.Errorf("late %s resurrection acknowledged with HTTP %d; want 500", phase, w.Code)
			}
			if before != cleanupACKFrame(t, db.Database()) {
				t.Error("failed cleanup did not restore full stack/resource/history/pruning frame")
			}
			if w.Code != 500 {
				return
			}
			if _, err := db.Database().Exec("DROP TRIGGER late_event_resurrection"); err != nil {
				t.Fatal(err)
			}
			if w = cleanupACKQuery(g, "DeleteStack", "events", ""); w.Code != 200 {
				t.Fatalf("repaired cleanup failed: %d", w.Code)
			}
			var remaining int
			if err := db.Database().QueryRow("SELECT count(*) FROM aws_ssm_parameters WHERE name='/events/parameter'").Scan(&remaining); err != nil || remaining != 0 {
				t.Fatal("physical residue after repaired cleanup", remaining, err)
			}
		})
	}
}

func TestStackEventsLateCompletionCannotResurrectBucket(t *testing.T) {
	db, s3, stacks, parameters := s3StackState(t)
	g := &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters}
	body := "Resources:\n  B:\n    Type: AWS::S3::Bucket\n    Properties: {BucketName: cfn-event-resurrection}\n"
	if w := cleanupACKQuery(g, "CreateStack", "bucket-events", body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := db.Database().Exec(`CREATE TABLE event_fault_bucket AS SELECT * FROM buckets WHERE name='cfn-event-resurrection'; CREATE TRIGGER late_event_bucket AFTER INSERT ON aws_cfn_events WHEN NEW.status='DELETE_COMPLETE' AND NEW.resource_type='AWS::CloudFormation::Stack' BEGIN INSERT INTO buckets SELECT * FROM event_fault_bucket; END`); err != nil {
		t.Fatal(err)
	}
	before := cleanupACKFrame(t, db.Database())
	w := cleanupACKQuery(g, "DeleteStack", "bucket-events", "")
	if w.Code != 500 {
		t.Errorf("late completion event resurrected bucket with HTTP %d; want 500", w.Code)
	}
	if before != cleanupACKFrame(t, db.Database()) {
		t.Error("failed cleanup changed full bucket/stack/history frame")
	}
	if w.Code != 500 {
		return
	}
	if _, err := db.Database().Exec("DROP TRIGGER late_event_bucket"); err != nil {
		t.Fatal(err)
	}
	if w = cleanupACKQuery(g, "DeleteStack", "bucket-events", ""); w.Code != 200 {
		t.Fatalf("repaired cleanup failed: %d", w.Code)
	}
	var remaining int
	if err := db.Database().QueryRow("SELECT count(*) FROM buckets WHERE name='cfn-event-resurrection'").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("bucket residue", remaining, err)
	}
}
