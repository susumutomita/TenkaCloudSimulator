package aws

import (
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSNSModeledWireErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		internal, wire string
		status         int
	}{
		{"InvalidParameterException", "InvalidParameter", 400},
		{"NotFoundException", "NotFound", 404},
		{"AuthorizationErrorException", "AuthorizationError", 403},
		{"InternalError", "InternalError", 500},
	} {
		t.Run(tc.internal, func(t *testing.T) {
			w := httptest.NewRecorder()
			snsError(w, ssmFailure{tc.internal, "test"})
			var result struct{ Error struct{ Code string } }
			if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || result.Error.Code != tc.wire {
				t.Fatalf("want HTTP%d/%s, got HTTP%d/%s", tc.status, tc.wire, w.Code, result.Error.Code)
			}
		})
	}
}

func TestSNSDeleteTopicRetriesAreNoOpsButStillValidateAndAuthorize(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	peer, _, err := g.SNS.CreateTopic("us-east-1", "peer", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.SNS.Subscribe(peer, "email", "peer@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = g.SNS.Publish(peer, "keep", ""); err != nil {
		t.Fatal(err)
	}
	before := snsRows(t, db.Database())
	absent := snsARN("us-east-1", "absent")
	for i := 0; i < 2; i++ {
		snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {absent}}, 200, "DeleteTopicResponse")
	}
	if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
		t.Fatal("absent deletion changed peer state")
	}
	for _, arn := range []string{"", "not-an-arn", "arn:aws:sns:us-east-1:123456789012:absent", absent + ":subscription"} {
		snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {arn}}, 400, "InvalidParameter")
	}
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {snsARN("us-west-2", "absent")}}, 404, "NotFound")
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sns:DeleteTopic","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Identities.Set("deny-delete", policy); err != nil {
		t.Fatal(err)
	}
	snsCall(t, g, "deny-delete", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {absent}}, 403, "AuthorizationError")
	if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
		t.Fatal("refused deletion changed peer state")
	}
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {peer.ARN}}, 200, "")
	snsCall(t, g, "", url.Values{"Action": {"DeleteTopic"}, "TopicArn": {peer.ARN}}, 200, "")
}

func TestSNSStoreAbsentDeleteCommitsNoOpAndSurfacesFailures(t *testing.T) {
	db, _ := s3TestState(t)
	g := snsTestGateway(t, db)
	arn := snsARN("us-east-1", "absent")
	before := snsRows(t, db.Database())
	if err := g.SNS.DeleteTopic(arn); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
		t.Fatal("absent store delete changed state")
	}
	for _, failure := range []string{"read", "commit"} {
		hit := false
		snsSetAuthorizer(t, db.Database(), func(action sqlite3.AuthorizerActionCode, table, op, _, _ string) sqlite3.AuthorizerReturnCode {
			if failure == "read" && action == sqlite3.AUTH_READ && table == "aws_sns_topics" || failure == "commit" && action == sqlite3.AUTH_TRANSACTION && strings.EqualFold(table, "COMMIT") {
				hit = true
				return sqlite3.AUTH_DENY
			}
			return sqlite3.AUTH_OK
		})
		err := g.SNS.DeleteTopic(arn)
		snsSetAuthorizer(t, db.Database(), nil)
		if !hit || err == nil {
			t.Fatalf("%s failure was acknowledged: hit=%t err=%v", failure, hit, err)
		}
		if !reflect.DeepEqual(before, snsRows(t, db.Database())) {
			t.Fatalf("%s failure changed state", failure)
		}
	}
}
