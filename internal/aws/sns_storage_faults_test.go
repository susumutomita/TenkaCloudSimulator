package aws

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

func snsRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"aws_sns_topics", "aws_sns_subscriptions", "aws_sns_messages", "aws_sns_deliveries"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var text strings.Builder
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&text, "%#v\n", values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = text.String()
	}
	return out
}

func TestSNSHTTPReadFailuresReturnErrorsWithoutPartialWrites(t *testing.T) {
	for _, operation := range []string{"CreateTopic", "GetTopicAttributes", "ListTopics", "Subscribe", "Unsubscribe", "ListSubscriptions", "ListSubscriptionsByTopic", "Publish", "DeleteTopic"} {
		for cutoff := 1; cutoff <= 40; cutoff++ {
			t.Run(fmt.Sprintf("%s/read-%02d", operation, cutoff), func(t *testing.T) {
				db, _ := s3TestState(t)
				g := snsTestGateway(t, db)
				topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
				if err != nil {
					t.Fatal(err)
				}
				sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = g.SNS.Publish(topic, "keep", ""); err != nil {
					t.Fatal(err)
				}
				before := snsRows(t, db.Database())
				fields := url.Values{"Action": {operation}, "Version": {"2010-03-31"}}
				switch operation {
				case "CreateTopic":
					fields.Set("Name", "new")
				case "Subscribe":
					fields.Set("TopicArn", topic.ARN)
					fields.Set("Protocol", "email")
					fields.Set("Endpoint", "new@example.com")
				case "Unsubscribe":
					fields.Set("SubscriptionArn", sub.ARN)
				case "GetTopicAttributes", "ListSubscriptionsByTopic", "DeleteTopic":
					fields.Set("TopicArn", topic.ARN)
				case "Publish":
					fields.Set("TopicArn", topic.ARN)
					fields.Set("Message", "new")
				}
				reads := 0
				denied := false
				snsSetAuthorizer(t, db.Database(), func(action sqlite3.AuthorizerActionCode, table, _, _, _ string) sqlite3.AuthorizerReturnCode {
					if action == sqlite3.AUTH_READ && strings.HasPrefix(table, "aws_sns_") {
						reads++
						if reads == cutoff {
							denied = true
							return sqlite3.AUTH_DENY
						}
					}
					return sqlite3.AUTH_OK
				})
				r := httptest.NewRequest("POST", "/", strings.NewReader(fields.Encode()))
				r.Header.Set("X-Simulator-Service", "sns")
				w := httptest.NewRecorder()
				g.ServeHTTP(w, r)
				snsSetAuthorizer(t, db.Database(), nil)
				if denied {
					if w.Code != 500 {
						t.Errorf("database read failure returned HTTP%d", w.Code)
					}
					if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
						t.Error("HTTP failure changed committed state")
					}
				} else if w.Code != 200 {
					t.Fatalf("valid request failed: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func snsSetAuthorizer(t *testing.T, db *sql.DB, fn func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error { return raw.(driver.Conn).Raw().SetAuthorizer(fn) })
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSNSReadFailuresCannotCommitPartialState(t *testing.T) {
	for _, operation := range []string{"initialize", "create", "subscribe", "publish", "delete", "unsubscribe"} {
		for cutoff := 1; cutoff <= 30; cutoff++ {
			t.Run(fmt.Sprintf("%s/read-%02d", operation, cutoff), func(t *testing.T) {
				db, _ := s3TestState(t)
				g := snsTestGateway(t, db)
				topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
				if err != nil {
					t.Fatal(err)
				}
				sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = g.SNS.Publish(topic, "keep", ""); err != nil {
					t.Fatal(err)
				}
				before := snsRows(t, db.Database())
				reads := 0
				denied := false
				snsSetAuthorizer(t, db.Database(), func(action sqlite3.AuthorizerActionCode, table, _, _, _ string) sqlite3.AuthorizerReturnCode {
					if action == sqlite3.AUTH_READ && strings.HasPrefix(table, "aws_sns_") {
						reads++
						if reads == cutoff {
							denied = true
							return sqlite3.AUTH_DENY
						}
					}
					return sqlite3.AUTH_OK
				})
				switch operation {
				case "initialize":
					_, err = NewSNSStore(db.Database())
				case "create":
					_, _, err = g.SNS.CreateTopic("us-east-1", "new", "", false)
				case "subscribe":
					_, err = g.SNS.Subscribe(topic, "email", "new@example.com")
				case "publish":
					_, _, err = g.SNS.Publish(topic, "new", "")
				case "delete":
					err = g.SNS.DeleteTopic(topic.ARN)
				case "unsubscribe":
					err = g.SNS.Unsubscribe(sub.ARN)
				}
				snsSetAuthorizer(t, db.Database(), nil)
				if denied {
					if err == nil {
						t.Error("denied SQLite read returned success")
					}
					if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
						t.Error("denied read changed committed state")
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSNSCommitFailureRollsBackEveryMutation(t *testing.T) {
	for _, operation := range []string{"initialize", "create", "subscribe", "publish", "delete", "unsubscribe"} {
		t.Run(operation, func(t *testing.T) {
			db, _ := s3TestState(t)
			g := snsTestGateway(t, db)
			topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = g.SNS.Publish(topic, "keep", ""); err != nil {
				t.Fatal(err)
			}
			before := snsRows(t, db.Database())
			denied := false
			snsSetAuthorizer(t, db.Database(), func(action sqlite3.AuthorizerActionCode, op, _, _, _ string) sqlite3.AuthorizerReturnCode {
				if action == sqlite3.AUTH_TRANSACTION && strings.EqualFold(op, "COMMIT") {
					denied = true
					return sqlite3.AUTH_DENY
				}
				return sqlite3.AUTH_OK
			})
			switch operation {
			case "initialize":
				_, err = NewSNSStore(db.Database())
			case "create":
				_, _, err = g.SNS.CreateTopic("us-east-1", "new", "", false)
			case "subscribe":
				_, err = g.SNS.Subscribe(topic, "email", "new@example.com")
			case "publish":
				_, _, err = g.SNS.Publish(topic, "new", "")
			case "delete":
				err = g.SNS.DeleteTopic(topic.ARN)
			case "unsubscribe":
				err = g.SNS.Unsubscribe(sub.ARN)
			}
			snsSetAuthorizer(t, db.Database(), nil)
			if !denied || err == nil {
				t.Fatal("commit rejection was not propagated", err)
			}
			if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
				t.Fatal("failed commit left partial state")
			}
		})
	}
}

func TestSNSAbortDuringCleanupOrPublishPreservesRows(t *testing.T) {
	for _, point := range []struct{ table, event, operation string }{
		{"aws_sns_topics", "INSERT", "create"}, {"aws_sns_subscriptions", "INSERT", "subscribe"},
		{"aws_sns_messages", "INSERT", "publish"}, {"aws_sns_deliveries", "INSERT", "publish"},
		{"aws_sns_deliveries", "DELETE", "delete"}, {"aws_sns_messages", "DELETE", "delete"},
		{"aws_sns_subscriptions", "DELETE", "delete"}, {"aws_sns_topics", "DELETE", "delete"},
		{"aws_sns_subscriptions", "DELETE", "unsubscribe"},
	} {
		t.Run(point.table+"/"+point.operation, func(t *testing.T) {
			db, _ := s3TestState(t)
			g := snsTestGateway(t, db)
			topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = g.SNS.Publish(topic, "keep", ""); err != nil {
				t.Fatal(err)
			}
			before := snsRows(t, db.Database())
			if _, err = db.Database().Exec("CREATE TRIGGER abort_write BEFORE " + point.event + " ON " + point.table + " BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
				t.Fatal(err)
			}
			switch point.operation {
			case "create":
				_, _, err = g.SNS.CreateTopic("us-east-1", "new", "", false)
			case "subscribe":
				_, err = g.SNS.Subscribe(topic, "email", "new@example.com")
			case "publish":
				_, _, err = g.SNS.Publish(topic, "new", "")
			case "delete":
				err = g.SNS.DeleteTopic(topic.ARN)
			case "unsubscribe":
				err = g.SNS.Unsubscribe(sub.ARN)
			}
			if err == nil {
				t.Fatal("SQLite abort returned success")
			}
			if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
				t.Fatal("late SQLite failure changed state")
			}
		})
	}
}

func TestSNSReadErrorsAndMalformedSchemaFailClosed(t *testing.T) {
	t.Run("topic boolean", func(t *testing.T) {
		db, _ := s3TestState(t)
		g := snsTestGateway(t, db)
		topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Database().Exec("PRAGMA ignore_check_constraints=ON; UPDATE aws_sns_topics SET fifo='invalid'"); err != nil {
			t.Fatal(err)
		}
		if _, err = g.SNS.ListTopics(context.Background(), "us-east-1"); err == nil {
			t.Fatal("malformed boolean was returned")
		}
		if _, err = g.SNS.GetTopic(topic.ARN); err == nil {
			t.Fatal("malformed boolean was returned")
		}
		if _, err = g.SNS.Subscribe(topic, "email", "ops@example.com"); err == nil {
			t.Fatal("malformed parent accepted")
		}
		if _, _, err = g.SNS.Publish(topic, "new", ""); err == nil {
			t.Fatal("malformed parent accepted")
		}
	})
	t.Run("subscription NULL", func(t *testing.T) {
		db, _ := s3TestState(t)
		g := snsTestGateway(t, db)
		topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
		if err != nil {
			t.Fatal(err)
		}
		sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Database().Exec("ALTER TABLE aws_sns_subscriptions RENAME TO saved_subscriptions; CREATE VIEW aws_sns_subscriptions AS SELECT arn,topic_arn,protocol,NULL AS endpoint,created FROM saved_subscriptions"); err != nil {
			t.Fatal(err)
		}
		if _, err = g.SNS.GetSubscription(sub.ARN); err == nil {
			t.Fatal("NULL endpoint accepted")
		}
		if _, err = g.SNS.ListSubscriptions(context.Background(), "us-east-1"); err == nil {
			t.Fatal("NULL endpoint returned")
		}
		if _, err = g.SNS.ListSubscriptionsByTopic(context.Background(), topic.ARN); err == nil {
			t.Fatal("NULL endpoint returned")
		}
	})
	t.Run("message timestamp", func(t *testing.T) {
		db, _ := s3TestState(t)
		g := snsTestGateway(t, db)
		topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = g.SNS.Publish(topic, "keep", ""); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Database().Exec("UPDATE aws_sns_messages SET published='invalid'"); err != nil {
			t.Fatal(err)
		}
		if _, err = g.SNS.ListMessages(context.Background(), topic.ARN); err == nil {
			t.Fatal("invalid timestamp returned")
		}
	})
	t.Run("schema startup atomic", func(t *testing.T) {
		db, _ := s3TestState(t)
		if _, err := db.Database().Exec("CREATE VIEW aws_sns_messages AS SELECT 'id' AS id,'topic' AS topic_arn"); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSNSStore(db.Database()); err == nil {
			t.Fatal("invalid schema accepted")
		}
		var n int
		if err := db.Database().QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('aws_sns_topics','aws_sns_subscriptions')").Scan(&n); err != nil || n != 0 {
			t.Fatal("partial initialization left tables", n, err)
		}
	})
}
