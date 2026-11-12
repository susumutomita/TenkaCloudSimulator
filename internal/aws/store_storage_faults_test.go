package aws

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestSQSStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	queue := SQSQueue{Name: "faults", VisibilityTimeout: 30, RetentionSeconds: 60}
	seeded := func(t *testing.T, db *sql.DB, messages int) (*SQSStore, SQSQueue) {
		t.Helper()
		s, err := NewSQSStore(db)
		if err != nil {
			t.Fatal(err)
		}
		q, err := s.CreateQueue("us-east-1", queue)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < messages; i++ {
			if _, err = s.Send("us-east-1", q, "body", map[string]SQSMessageAttribute{}, -1); err != nil {
				t.Fatal(err)
			}
		}
		return s, q
	}
	t.Run("create queue", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, err := NewSQSStore(db)
			if err != nil {
				t.Fatal(err)
			}
			return func(context.Context) error { _, err := s.CreateQueue("us-east-1", queue); return err }
		})
	})
	t.Run("send", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, q := seeded(t, db, 0)
			return func(context.Context) error { _, err := s.Send("us-east-1", q, "payload", nil, 0); return err }
		})
	})
	t.Run("receive", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, q := seeded(t, db, 2)
			return func(context.Context) error {
				got, err := s.Receive("us-east-1", q, 2, 30)
				if err == nil && len(got) != 2 {
					t.Fatalf("received %d of 2 messages", len(got))
				}
				return err
			}
		})
	})
	received := func(t *testing.T, db *sql.DB) (*SQSStore, SQSQueue, string) {
		t.Helper()
		s, q := seeded(t, db, 1)
		got, err := s.Receive("us-east-1", q, 1, 30)
		if err != nil || len(got) != 1 {
			t.Fatal(got, err)
		}
		return s, q, got[0].ReceiptHandle
	}
	t.Run("change visibility", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, q, receipt := received(t, db)
			return func(context.Context) error { return s.ChangeVisibility("us-east-1", q.Name, receipt, 60) }
		})
	})
	t.Run("delete message", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, q, receipt := received(t, db)
			return func(context.Context) error { return s.DeleteMessage("us-east-1", q.Name, receipt) }
		})
	})
	t.Run("reads and delete queue", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, q := seeded(t, db, 1)
			return func(context.Context) error {
				if visible, _, err := s.Counts("us-east-1", q); err != nil {
					return err
				} else if visible != 1 {
					t.Fatalf("visible count %d", visible)
				}
				if list, err := s.ListQueues("us-east-1", "fa"); err != nil {
					return err
				} else if len(list) != 1 {
					t.Fatalf("listed %d queues", len(list))
				}
				if _, err := s.GetQueue("us-east-1", q.Name); err != nil {
					return err
				}
				return s.DeleteQueue("us-east-1", q.Name)
			}
		})
	})
}

func TestDynamoDBStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	table := DynamoDBTable{Region: "us-east-1", Name: "faults", KeySchema: []DynamoDBKeyElement{{"id", "HASH"}}, Definitions: map[string]string{"id": "S"}, BillingMode: "PAY_PER_REQUEST"}
	item := `{"id":{"S":"k"},"payload":{"S":"value"}}`
	seeded := func(t *testing.T, db *sql.DB, withItem bool) (*DynamoDBStore, DynamoDBTable) {
		t.Helper()
		s, err := NewDynamoDBStore(db)
		if err != nil {
			t.Fatal(err)
		}
		created, err := s.CreateTable(table)
		if err != nil {
			t.Fatal(err)
		}
		if withItem {
			if err = s.PutItem(created, "S\x00k", "", item, ""); err != nil {
				t.Fatal(err)
			}
		}
		return s, created
	}
	t.Run("create table", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, err := NewDynamoDBStore(db)
			if err != nil {
				t.Fatal(err)
			}
			return func(context.Context) error { _, err := s.CreateTable(table); return err }
		})
	})
	t.Run("put item", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, created := seeded(t, db, false)
			return func(context.Context) error { return s.PutItem(created, "S\x00k", "", item, "") }
		})
	})
	t.Run("read and delete item", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, created := seeded(t, db, true)
			return func(context.Context) error {
				got, found, err := s.GetItem(created, "S\x00k", "")
				if err != nil {
					return err
				}
				if !found || got["payload"] == nil {
					t.Fatalf("item not read back: %v %v", got, found)
				}
				if items, _, err := s.TableStats(created); err != nil {
					return err
				} else if items != 1 {
					t.Fatalf("item count %d", items)
				}
				return s.DeleteItem(created, "S\x00k", "", "")
			}
		})
	})
	t.Run("list and delete table", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, _ := seeded(t, db, true)
			return func(context.Context) error {
				names, _, err := s.ListTables("us-east-1", "", 10)
				if err != nil {
					return err
				}
				if len(names) != 1 {
					t.Fatalf("listed %v", names)
				}
				if _, err = s.GetTable("us-east-1", "faults"); err != nil {
					return err
				}
				_, err = s.DeleteTable("us-east-1", "faults")
				return err
			}
		})
	})
}

func TestCloudWatchLogsStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	seeded := func(t *testing.T, db *sql.DB, stream bool) *CloudWatchLogsStore {
		t.Helper()
		s, err := NewCloudWatchLogsStore(db)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.CreateGroup(context.Background(), "us-east-1", "group"); err != nil {
			t.Fatal(err)
		}
		if stream {
			if err = s.CreateStream(context.Background(), "us-east-1", "group", "stream"); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	t.Run("create group", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, err := NewCloudWatchLogsStore(db)
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error { return s.CreateGroup(ctx, "us-east-1", "group") }
		})
	})
	t.Run("create stream", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := seeded(t, db, false)
			return func(ctx context.Context) error { return s.CreateStream(ctx, "us-east-1", "group", "stream") }
		})
	})
	t.Run("put events", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := seeded(t, db, true)
			now := time.Now().UnixMilli()
			return func(ctx context.Context) error {
				rejected, err := s.PutEvents(ctx, "us-east-1", "group", "stream", []LogsInputEvent{{now, "first"}, {now + 1, "second"}})
				if err == nil && rejected != nil {
					t.Fatalf("events rejected %+v", rejected)
				}
				return err
			}
		})
	})
	t.Run("delete group", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := seeded(t, db, true)
			return func(ctx context.Context) error { return s.DeleteGroup(ctx, "us-east-1", "group") }
		})
	})
}

func TestWAFStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	newWAF := func(t *testing.T, db *sql.DB) *WAFStore {
		t.Helper()
		if _, err := NewAPIGatewayStore(db); err != nil {
			t.Fatal(err)
		}
		s, err := NewWAFStore(db)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	seeded := func(t *testing.T, db *sql.DB) (*WAFStore, WAFSummary) {
		t.Helper()
		s, err := newWAF(t, db), error(nil)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := s.Create(context.Background(), wafTestRegion, wafTestACL())
		if err != nil {
			t.Fatal(err)
		}
		return s, summary
	}
	t.Run("create", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := newWAF(t, db)
			return func(ctx context.Context) error { _, err := s.Create(ctx, wafTestRegion, wafTestACL()); return err }
		})
	})
	t.Run("get and update", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, summary := seeded(t, db)
			return func(ctx context.Context) error {
				acl, token, err := s.Get(ctx, wafTestRegion, summary.ID, summary.Name)
				if err != nil {
					return err
				}
				acl.Tags = nil
				acl.Rules[0].Action = WAFAction{Block: &WAFEmpty{}}
				_, err = s.Update(ctx, wafTestRegion, acl, token)
				return err
			}
		})
	})
	t.Run("list tags and delete", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s, summary := seeded(t, db)
			return func(ctx context.Context) error {
				list, err := s.List(ctx, wafTestRegion, "", 10)
				if err != nil {
					return err
				}
				if len(list) != 1 {
					t.Fatalf("listed %d web ACLs", len(list))
				}
				tags, err := s.Tags(ctx, wafTestRegion, summary.ARN)
				if err != nil {
					return err
				}
				if len(tags) != 1 {
					t.Fatalf("tags %v", tags)
				}
				resources, err := s.Resources(ctx, wafTestRegion, summary.ARN)
				if err != nil {
					return err
				}
				if len(resources) != 0 {
					t.Fatalf("unexpected associations %v", resources)
				}
				_, token, err := s.Get(ctx, wafTestRegion, summary.ID, summary.Name)
				if err != nil {
					return err
				}
				return s.Delete(ctx, wafTestRegion, summary.ID, summary.Name, token)
			}
		})
	})
}
