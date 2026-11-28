package aws

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func snsPolicyStatement(arn, effect string) string {
	return fmt.Sprintf(`{"Effect":%q,"Principal":{"Service":["codestar-notifications.amazonaws.com"]},"Action":"SNS:Publish","Resource":%q}`, effect, arn)
}

func snsTestPolicy(statements ...string) string {
	return "{\n  \"Version\":\"2008-10-17\",\"Id\":\"__default_policy_ID\",\"Statement\":[" + strings.Join(statements, ",") + "]}\n"
}

func TestSNSNotificationPolicyGrammar(t *testing.T) {
	arn := snsARN("us-east-1", "events")
	allow, deny := snsPolicyStatement(arn, "Allow"), snsPolicyStatement(arn, "Deny")
	single := strings.ReplaceAll(allow, `["codestar-notifications.amazonaws.com"]`, `"codestar-notifications.amazonaws.com"`)
	single = strings.ReplaceAll(single, `"SNS:Publish"`, `["sNs:pUbLiSh"]`)
	single = strings.ReplaceAll(single, fmt.Sprintf("%q", arn), fmt.Sprintf("[%q]", arn))
	single = strings.Replace(single, `{"Effect"`, `{"Sid":"AWSCodeStarNotifications_publish","Effect"`, 1)
	bound := snsTestPolicy(allow)
	bound += strings.Repeat(" ", 8192-len(bound))
	for policy, want := range map[string]bool{
		snsTestPolicy(allow): true, snsTestPolicy(deny): false, snsTestPolicy(allow, deny): false,
		snsTestPolicy(deny, allow): false, `{"Version":"2012-10-17","Statement":` + single + `}`: true,
		bound: true,
		snsTestPolicy(strings.Split(strings.Repeat(allow+"|", 7)+allow, "|")...): true,
	} {
		if got, err := snsPolicyDecision(policy, arn); err != nil || got != want {
			t.Fatalf("decision %t, %v; want %t", got, err, want)
		}
	}
	for name, policy := range map[string]string{
		"duplicate":           strings.ReplaceAll(snsTestPolicy(allow), `"Allow"`, `"Deny","Effect":"Allow"`),
		"bad service array":   snsTestPolicy(strings.ReplaceAll(allow, `["codestar-notifications.amazonaws.com"]`, `["codestar-notifications.amazonaws.com","another"]`)),
		"principal":           snsTestPolicy(strings.ReplaceAll(allow, `"Service"`, `"AWS"`)),
		"action":              snsTestPolicy(strings.ReplaceAll(allow, `"SNS:Publish"`, `"SNS:*"`)),
		"resource":            snsTestPolicy(strings.ReplaceAll(allow, arn, arn+"2")),
		"condition":           snsTestPolicy(strings.ReplaceAll(allow, `"Effect":`, `"Condition":{},"Effect":`)),
		"duplicate sid":       snsTestPolicy(single, single),
		"empty id":            strings.ReplaceAll(snsTestPolicy(allow), "__default_policy_ID", ""),
		"too long unicode id": strings.ReplaceAll(snsTestPolicy(allow), "__default_policy_ID", strings.Repeat("界", 43)),
		"unpaired surrogate":  strings.ReplaceAll(snsTestPolicy(allow), "__default_policy_ID", `\uD800`),
		"control":             strings.ReplaceAll(snsTestPolicy(allow), "__default_policy_ID", `\u0000`),
		"invalid UTF-8":       strings.ReplaceAll(snsTestPolicy(allow), "__default_policy_ID", string([]byte{0xff})),
		"wrong version":       strings.ReplaceAll(snsTestPolicy(allow), "2008-10-17", "2009-01-01"),
		"effect":              snsTestPolicy(strings.ReplaceAll(allow, `"Allow"`, `{}`)),
		"trailing document":   snsTestPolicy(allow) + "{}",
		"unknown field":       strings.Replace(snsTestPolicy(allow), `"Id":`, `"unused":0,"Id":`, 1),
		"wrong field case":    strings.ReplaceAll(snsTestPolicy(allow), "Statement", "statement"),
		"empty list":          snsTestPolicy(), "null": "null", "bad json": "{", "too big": bound + " ",
		"too many": snsTestPolicy(strings.Split(strings.Repeat(allow+"|", 8)+allow, "|")...),
	} {
		t.Run(name, func(t *testing.T) {
			if allowed, err := snsPolicyDecision(policy, arn); allowed || err == nil {
				t.Fatalf("unsupported policy yielded grant=%t err=%v", allowed, err)
			}
		})
	}
}

func snsPolicySetForm(arn, policy string) url.Values {
	return url.Values{"Action": {"SetTopicAttributes"}, "Version": {"2010-03-31"}, "TopicArn": {arn}, "AttributeName": {"Policy"}, "AttributeValue": {policy}}
}

func snsTestGrant(t *testing.T, g *Gateway, arn string, allowed bool) int64 {
	t.Helper()
	tx, err := g.SNS.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, got, err := snsNotificationTopicGrant(t.Context(), tx, arn)
	if err != nil || got != allowed {
		t.Fatalf("grant on %s = %v, %v (error %v)", arn, id, got, err)
	}
	return id
}

func snsTestPolicyAttribute(t *testing.T, g *Gateway, arn string) (string, bool) {
	t.Helper()
	reply := snsCall(t, g, "", url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {arn}}, 200, "<GetTopicAttributesResult>")
	var doc struct {
		Entries []snsAttributeEntry `xml:"GetTopicAttributesResult>Attributes>entry"`
	}
	if err := xml.Unmarshal([]byte(reply), &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range doc.Entries {
		if e.Key == "Policy" {
			return e.Value, true
		}
	}
	return "", false
}

func TestSNSNotificationPolicyWireAndLifetime(t *testing.T) {
	db := snsReadbackDB(t)
	g := newSNSReadbackGateway(t, db)
	arn := snsARN("us-east-1", "events")
	allow, deny := snsTestPolicy(snsPolicyStatement(arn, "Allow")), snsTestPolicy(snsPolicyStatement(arn, "Deny"))
	create := url.Values{"Action": {"CreateTopic"}, "Name": {"events"}}
	snsCall(t, g, "", create, 200, arn)
	if _, ok := snsTestPolicyAttribute(t, g, arn); ok || snsTestGrant(t, g, arn, false) != 0 {
		t.Fatal("legacy topic acquired a policy")
	}
	grant, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:SetTopicAttributes","Resource":%q}]}`, arn)))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("editor", grant); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{arn, arn + "-missing"} {
		snsCall(t, g, "unprivileged", snsPolicySetForm(resource, "{}"), 403, "AuthorizationError")
		snsCall(t, g, "unprivileged", url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {resource}}, 403, "AuthorizationError")
	}
	missing := arn + "-missing"
	snsCall(t, g, "", snsPolicySetForm(missing, snsTestPolicy(snsPolicyStatement(missing, "Allow"))), 404, "NotFound")
	snsCall(t, g, "", snsPolicySetForm("arn:aws:sns:us-east-1:012345678910:events", allow), 400, "InvalidParameter")
	snsWire(t, g, "POST", "/", snsPolicySetForm(arn, allow).Encode(), map[string]string{"X-Simulator-Region": "us-west-2"}, 404)
	fifo := snsARN("us-east-1", "events.fifo")
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"events.fifo"}, "Attributes.entry.1.key": {"FifoTopic"}, "Attributes.entry.1.value": {"true"}}, 200, fifo)
	snsCall(t, g, "", snsPolicySetForm(fifo, snsTestPolicy(snsPolicyStatement(fifo, "Allow"))), 400, "Standard")
	form := snsPolicySetForm(arn, allow)
	form.Set("AttributeName", "DisplayName")
	snsCall(t, g, "", form, 400, "InvalidParameter")
	snsCall(t, g, "editor", snsPolicySetForm(arn, allow), 200, "SetTopicAttributesResponse")
	id := snsTestGrant(t, g, arn, true)
	if id <= 0 {
		t.Fatal("no persisted identity")
	}
	snsCall(t, g, "editor", url.Values{"Action": {"Publish"}, "TopicArn": {arn}, "Message": {"ordinary request"}}, 403, "AuthorizationError")
	snsCall(t, g, "", snsPolicySetForm(arn, strings.ReplaceAll(allow, "SNS:Publish", "SNS:*")), 400, "InvalidParameter")
	if got, ok := snsTestPolicyAttribute(t, g, arn); !ok || got != allow {
		t.Fatal("policy did not round trip verbatim")
	}
	for _, p := range []string{deny, deny, allow} {
		snsCall(t, g, "editor", snsPolicySetForm(arn, p), 200, "SetTopicAttributesResponse")
		if snsTestGrant(t, g, arn, p == allow) != id {
			t.Fatal("policy edit changed physical identity")
		}
	}
	other := newSNSReadbackGateway(t, snsReadbackDB(t))
	snsCall(t, other, "", create, 200, arn)
	if _, ok := snsTestPolicyAttribute(t, other, arn); ok || snsTestGrant(t, other, arn, false) != 0 {
		t.Fatal("policy crossed team boundary")
	}
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {arn}}, 200, "DeleteTopicResponse")
	snsCall(t, g, "", create, 200, arn)
	if snsTestGrant(t, g, arn, false) != 0 {
		t.Fatal("replacement inherited policy")
	}
	snsCall(t, g, "", snsPolicySetForm(arn, allow), 200, "SetTopicAttributesResponse")
	if snsTestGrant(t, g, arn, true) <= id {
		t.Fatal("old topic identity reused")
	}
}

func TestSNSNotificationPolicyStagedInterrupt(t *testing.T) {
	for _, sqlOp := range []string{"INSERT", "UPDATE"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(sqlOp+"/"+cause.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "sns.db")
				open := func() (*core.Store, *Gateway) {
					db, err := core.New(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { db.Close() })
					return db, newSNSReadbackGateway(t, db)
				}
				db, g := open()
				arn := snsARN("us-east-1", "interrupt")
				snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"interrupt"}}, 200, arn)
				allow, deny := snsTestPolicy(snsPolicyStatement(arn, "Allow")), snsTestPolicy(snsPolicyStatement(arn, "Deny"))
				var prev int64
				if sqlOp == "UPDATE" {
					snsCall(t, g, "", snsPolicySetForm(arn, deny), 200, "SetTopicAttributesResponse")
					prev = snsTestGrant(t, g, arn, false)
				}
				peer := snsARN("us-east-1", "untouched")
				snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"untouched"}}, 200, peer)
				snsCall(t, g, "", snsPolicySetForm(peer, snsTestPolicy(snsPolicyStatement(peer, "Allow"))), 200, "SetTopicAttributesResponse")
				peerID := snsTestGrant(t, g, peer, true)
				before := snsRows(t, db.Database())
				var ctx context.Context
				var cancel context.CancelFunc
				var observed string
				var stagedID int64
				conn, err := db.Database().Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_sns_policy", 2, 0, func(fn sqlite3.Context, args ...sqlite3.Value) {
						stagedID = args[0].Int64()
						observed = args[1].Text()
						if cause == context.Canceled {
							cancel()
						} else {
							<-ctx.Done()
						}
						fn.ResultInt(1)
					})
				})
				if err != nil {
					t.Fatal(err)
				}
				_, err = conn.ExecContext(t.Context(), "CREATE TEMP TRIGGER staged AFTER "+sqlOp+` ON aws_sns_notification_policies BEGIN
					SELECT observe_sns_policy(generation,policy) FROM aws_sns_notification_policies WHERE topic_arn=NEW.topic_arn; END`)
				if err != nil {
					t.Fatal(err)
				}
				if err = conn.Close(); err != nil {
					t.Fatal(err)
				}
				if cause == context.Canceled {
					ctx, cancel = context.WithCancel(t.Context())
				} else {
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				req := httptest.NewRequestWithContext(ctx, "POST", "/", strings.NewReader(snsPolicySetForm(arn, allow).Encode()))
				req.Header.Set("X-Simulator-Service", "sns")
				res := httptest.NewRecorder()
				g.ServeHTTP(res, req)
				if res.Code != 500 || !strings.Contains(res.Body.String(), "<Code>InternalError</Code>") || !errors.Is(ctx.Err(), cause) || observed != allow || stagedID <= 0 {
					t.Fatalf("HTTP=%d physical generation=%d observed=%t err=%v", res.Code, stagedID, observed == allow, ctx.Err())
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				restored, h := open()
				value, present := snsTestPolicyAttribute(t, h, arn)
				if present != (sqlOp == "UPDATE") || present && value != deny || snsTestGrant(t, h, arn, false) != prev || snsTestGrant(t, h, peer, true) != peerID || !reflect.DeepEqual(before, snsRows(t, restored.Database())) {
					t.Fatal("interrupted request changed durable state")
				}
				snsCall(t, h, "", snsPolicySetForm(arn, allow), 200, "SetTopicAttributesResponse")
				if retry := snsTestGrant(t, h, arn, true); retry <= 0 || prev > 0 && retry != prev {
					t.Fatalf("retry returned generation %d", retry)
				}
			})
		}
	}
}

func TestSNSNotificationPolicyStorageFailures(t *testing.T) {
	db := snsReadbackDB(t)
	g := newSNSReadbackGateway(t, db)
	arn := snsARN("us-east-1", "stored")
	snsCall(t, g, "", url.Values{"Action": {"CreateTopic"}, "Name": {"stored"}}, 200, arn)
	allow, deny := snsTestPolicy(snsPolicyStatement(arn, "Allow")), snsTestPolicy(snsPolicyStatement(arn, "Deny"))
	snsCall(t, g, "", snsPolicySetForm(arn, allow), 200, "SetTopicAttributesResponse")
	id := snsTestGrant(t, g, arn, true)
	for name, trigger := range map[string]string{
		"ignore edit":          "BEFORE UPDATE ON aws_sns_notification_policies BEGIN SELECT RAISE(IGNORE); END",
		"change written bytes": "AFTER UPDATE ON aws_sns_notification_policies BEGIN UPDATE aws_sns_notification_policies SET policy='{}' WHERE topic_arn=NEW.topic_arn; END",
		"remove edited row":    "AFTER UPDATE ON aws_sns_notification_policies BEGIN DELETE FROM aws_sns_notification_policies WHERE topic_arn=NEW.topic_arn; END",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Database().Exec("CREATE TEMP TRIGGER disrupt_policy " + trigger); err != nil {
				t.Fatal(err)
			}
			snsCall(t, g, "", snsPolicySetForm(arn, deny), 500, "InternalError")
			if _, err := db.Database().Exec("DROP TRIGGER disrupt_policy"); err != nil {
				t.Fatal(err)
			}
			if value, ok := snsTestPolicyAttribute(t, g, arn); !ok || value != allow || snsTestGrant(t, g, arn, true) != id {
				t.Fatal("failed edit changed saved policy")
			}
		})
	}
	if _, err := db.Database().Exec("UPDATE aws_sns_notification_policies SET policy='{}' WHERE topic_arn=?", arn); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {arn}}, 500, "InternalError")
	tx, err := db.Database().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, allowed, err := snsNotificationTopicGrant(t.Context(), tx, arn)
	if err == nil || allowed || got != 0 {
		t.Fatalf("invalid stored policy produced grant %v/%d", allowed, got)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "", snsPolicySetForm(arn, deny), 200, "SetTopicAttributesResponse")
	if snsTestGrant(t, g, arn, false) != id {
		t.Fatal("repair changed physical identity")
	}
}
