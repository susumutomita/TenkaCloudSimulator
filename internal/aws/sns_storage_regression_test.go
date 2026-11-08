package aws

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestSNSDeletedTopicSnapshotCannotCreateOrphanState(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "removed", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.SNS.DeleteTopic(topic.ARN); err != nil {
		t.Fatal(err)
	}
	if _, err = g.SNS.Subscribe(topic, "email", "ops@example.com"); iamCode(err) != "NotFoundException" {
		t.Errorf("stale topic subscription accepted: %v", err)
	}
	if _, _, err = g.SNS.Publish(topic, "late message", ""); iamCode(err) != "NotFoundException" {
		t.Errorf("stale topic publish accepted: %v", err)
	}
	for _, table := range []string{"aws_sns_subscriptions", "aws_sns_messages", "aws_sns_deliveries"} {
		var n int
		if err = db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("orphan %s rows=%d: %v", table, n, err)
		}
	}
}

func TestSNSBudgetsAreGlobalAndTopicCleanupReleasesThem(t *testing.T) {
	for _, budget := range []string{"messages", "bytes", "deliveries"} {
		t.Run(budget, func(t *testing.T) {
			db, _ := s3TestState(t)
			g := snsTestGateway(t, db)
			topic, _, err := g.SNS.CreateTopic("us-east-1", "budget", "", false)
			if err != nil {
				t.Fatal(err)
			}
			peer, _, err := g.SNS.CreateTopic("us-west-2", "peer", "", false)
			if err != nil {
				t.Fatal(err)
			}
			switch budget {
			case "messages":
				_, err = db.Database().Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO aws_sns_messages SELECT printf('%032d',x),?,'x','',0 FROM n", topic.ARN)
			case "bytes":
				// The final two bytes belong to one Unicode subject character.
				_, err = db.Database().Exec("INSERT INTO aws_sns_messages VALUES('large',?,zeroblob(67108862),'é',0)", topic.ARN)
			case "deliveries":
				_, err = db.Database().Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<5000) INSERT INTO aws_sns_messages SELECT printf('%032d',x),?,'x','',0 FROM n", topic.ARN)
				if err == nil {
					_, err = db.Database().Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<20) INSERT INTO aws_sns_subscriptions SELECT printf('sub%d',x),?,'email',printf('mail%d@example.com',x),0 FROM n", topic.ARN)
				}
				if err == nil {
					_, err = db.Database().Exec("INSERT INTO aws_sns_deliveries SELECT m.id,s.arn FROM aws_sns_messages m JOIN aws_sns_subscriptions s ON m.topic_arn=s.topic_arn")
				}
				if err == nil {
					_, err = g.SNS.Subscribe(peer, "email", "peer@example.com")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			var before int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_sns_messages").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, _, err = g.SNS.Publish(peer, "x", ""); iamCode(err) != "LimitExceededException" {
				t.Errorf("global %s budget ignored: %v", budget, err)
			}
			var after int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_sns_messages").Scan(&after); err != nil || after != before {
				t.Fatalf("quota refusal persisted message: before=%d after=%d err=%v", before, after, err)
			}
			if err = g.SNS.DeleteTopic(topic.ARN); err != nil {
				t.Fatal(err)
			}
			if _, _, err = g.SNS.Publish(peer, "x", ""); err != nil {
				t.Fatalf("topic cleanup did not release %s budget: %v", budget, err)
			}
		})
	}
}

func TestSNSLateCleanupReinsertionCannotReturnSuccess(t *testing.T) {
	for _, row := range []string{"topic", "message", "subscription", "delivery"} {
		t.Run(row, func(t *testing.T) {
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
			id, _, err := g.SNS.Publish(topic, "keep", "")
			if err != nil {
				t.Fatal(err)
			}
			statement := map[string]string{
				"topic":        "INSERT INTO aws_sns_topics VALUES('us-east-1','base','" + topic.ARN + "','',0,0)",
				"message":      "INSERT INTO aws_sns_messages VALUES('" + id + "','" + topic.ARN + "','changed','',0)",
				"subscription": "INSERT INTO aws_sns_subscriptions VALUES('" + sub.ARN + "','" + topic.ARN + "','email','changed@example.com',0)",
				"delivery":     "INSERT INTO aws_sns_deliveries VALUES('" + id + "','" + sub.ARN + "')",
			}[row]
			if _, err = db.Database().Exec("CREATE TRIGGER late_reinsert AFTER DELETE ON aws_sns_topics BEGIN " + statement + "; END"); err != nil {
				t.Fatal(err)
			}
			if err = g.SNS.DeleteTopic(topic.ARN); err == nil {
				t.Error("late " + row + " reinsertion was acknowledged")
			}
			if _, err = g.SNS.GetTopic(topic.ARN); err != nil {
				t.Fatal(err)
			}
			messages, err := g.SNS.ListMessages(context.Background(), topic.ARN)
			if err != nil || len(messages) != 1 || messages[0].Message != "keep" {
				t.Fatal(messages, err)
			}
		})
	}
}

func TestSNSStartupRejectsOrphanStateWithoutDestroyingIt(t *testing.T) {
	for _, kind := range []string{"subscription", "message", "delivery"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := s3TestState(t)
			g := snsTestGateway(t, db)
			query := map[string]string{
				"subscription": "INSERT INTO aws_sns_subscriptions VALUES('orphan','missing','email','ops@example.com',0)",
				"message":      "INSERT INTO aws_sns_messages VALUES('orphan','missing','message','',0)",
				"delivery":     "INSERT INTO aws_sns_deliveries VALUES('missing','orphan')",
			}[kind]
			if _, err := db.Database().Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, err := NewSNSStore(db.Database()); err == nil || !strings.Contains(err.Error(), "orphan") {
				t.Fatal("corrupt state opened", err)
			}
			var n int
			table := map[string]string{"subscription": "aws_sns_subscriptions", "message": "aws_sns_messages", "delivery": "aws_sns_deliveries"}[kind]
			if err := g.SNS.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 1 {
				t.Fatal("startup destroyed existing state", n, err)
			}
		})
	}
}

func TestSNSUnavailableDatabaseFailsClosed(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	checks := []func() error{
		func() error { _, err := NewSNSStore(g.SNS.db); return err },
		func() error { _, _, err := g.SNS.CreateTopic("us-east-1", "late", "", false); return err },
		func() error { _, err := g.SNS.GetTopic(topic.ARN); return err },
		func() error { _, err := g.SNS.ListTopics(context.Background(), "us-east-1"); return err },
		func() error { return g.SNS.DeleteTopic(topic.ARN) },
		func() error { _, err := g.SNS.Subscribe(topic, "email", "ops@example.com"); return err },
		func() error { _, err := g.SNS.GetSubscription("missing"); return err },
		func() error { return g.SNS.Unsubscribe("missing") },
		func() error { _, err := g.SNS.ListSubscriptions(context.Background(), "us-east-1"); return err },
		func() error { _, err := g.SNS.ListSubscriptionsByTopic(context.Background(), topic.ARN); return err },
		func() error { _, _, err := g.SNS.Publish(topic, "late", ""); return err },
		func() error { _, err := g.SNS.ListMessages(context.Background(), topic.ARN); return err },
		func() error { _, err := g.SNS.DeliveryCount("missing"); return err },
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Errorf("closed database operation %d returned success", i)
		}
	}
}

func TestSNSTopicDeleteRemovesUnsubscribedDeliveryAndMessageState(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	topic, _, err := g.SNS.CreateTopic("us-east-1", "removed", "", false)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := g.SNS.Subscribe(topic, "email", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = g.SNS.Publish(topic, "audit", ""); err != nil {
		t.Fatal(err)
	}
	if err = g.SNS.Unsubscribe(sub.ARN); err != nil {
		t.Fatal(err)
	}
	if err = g.SNS.DeleteTopic(topic.ARN); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"aws_sns_topics", "aws_sns_subscriptions", "aws_sns_messages", "aws_sns_deliveries"} {
		var n int
		if err = db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("delete left %s rows=%d: %v", table, n, err)
		}
	}
}

func TestSNSIgnoredWritesCannotAcknowledgeSuccess(t *testing.T) {
	for _, kind := range []string{"topic", "subscription", "message", "delivery"} {
		t.Run(kind, func(t *testing.T) {
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
			table := map[string]string{"topic": "aws_sns_topics", "subscription": "aws_sns_subscriptions", "message": "aws_sns_messages", "delivery": "aws_sns_deliveries"}[kind]
			if _, err = db.Database().Exec("CREATE TRIGGER ignore_write BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(IGNORE); END"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "topic":
				_, _, err = g.SNS.CreateTopic("us-east-1", "phantom", "", false)
			case "subscription":
				_, err = g.SNS.Subscribe(topic, "email", "other@example.com")
			default:
				_, _, err = g.SNS.Publish(topic, "lost", "")
			}
			if err == nil {
				t.Error("ignored " + kind + " write acknowledged success")
			}
			var n int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_sns_messages").Scan(&n); err != nil || n != 0 {
				t.Errorf("failed write retained message: %d %v", n, err)
			}
			if n, err = g.SNS.DeliveryCount(sub.ARN); err != nil || n != 0 {
				t.Errorf("failed write retained delivery: %d %v", n, err)
			}
		})
	}
}

func TestSNSPersistedPostconditionsRejectVanishingWrites(t *testing.T) {
	for _, kind := range []string{"topic", "subscription", "message"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := s3TestState(t)
			g := snsTestGateway(t, db)
			topic, _, err := g.SNS.CreateTopic("us-east-1", "base", "", false)
			if err != nil {
				t.Fatal(err)
			}
			table, key := map[string]string{"topic": "aws_sns_topics", "subscription": "aws_sns_subscriptions", "message": "aws_sns_messages"}[kind], "arn"
			if kind == "message" {
				key = "id"
			}
			if _, err = db.Database().Exec(fmt.Sprintf("CREATE TRIGGER erase_write AFTER INSERT ON %s BEGIN DELETE FROM %s WHERE %s=NEW.%s; END", table, table, key, key)); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "topic":
				_, _, err = g.SNS.CreateTopic("us-east-1", "phantom", "", false)
			case "subscription":
				_, err = g.SNS.Subscribe(topic, "email", "ops@example.com")
			default:
				_, _, err = g.SNS.Publish(topic, "lost", "")
			}
			if err == nil {
				t.Errorf("vanishing %s write returned success", kind)
			}
		})
	}
}

func TestSNSDeleteRollbackPreservesCompleteState(t *testing.T) {
	for _, table := range []string{"aws_sns_topics", "aws_sns_messages", "aws_sns_subscriptions", "aws_sns_deliveries"} {
		t.Run(table, func(t *testing.T) {
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
			if _, err = db.Database().Exec("CREATE TRIGGER ignore_delete BEFORE DELETE ON " + table + " BEGIN SELECT RAISE(IGNORE); END"); err != nil {
				t.Fatal(err)
			}
			if err = g.SNS.DeleteTopic(topic.ARN); err == nil {
				t.Error("incomplete delete returned success")
			}
			if _, err = g.SNS.GetTopic(topic.ARN); err != nil {
				t.Error(err)
			}
			if _, err = g.SNS.GetSubscription(sub.ARN); err != nil {
				t.Error(err)
			}
			messages, err := g.SNS.ListMessages(context.Background(), topic.ARN)
			if err != nil || len(messages) != 1 {
				t.Errorf("delete failure changed messages: %+v %v", messages, err)
			}
			if n, err := g.SNS.DeliveryCount(sub.ARN); err != nil || n != 1 {
				t.Errorf("delete failure changed deliveries: %d %v", n, err)
			}
		})
	}
}
