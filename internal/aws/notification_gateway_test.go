package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/codestarnotifications"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestNotificationRulesOnExistingNativeRepository(t *testing.T) {
	team, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	identities := core.NewIdentities(team)
	engine, err := catalog.New(team.Database(), catalog.Config{Regions: Regions, Account: "000000000000", DefaultPrincipal: core.Administrator, Authorize: identities.DecisionContext}, catalog.Services())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewSNSStore(team.Database()); err != nil {
		t.Fatal(err)
	}
	if _, err = NewSQSStore(team.Database()); err != nil {
		t.Fatal(err)
	}
	store, err := codestarnotifications.New(team.Database(), func(context.Context, *sql.Tx, string) (int64, bool, error) {
		t.Fatal("an empty target set must not gain any service grant")
		return 0, false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Notifications: store, Identities: identities}
	post := func(h http.Handler, path, op, actor string, input map[string]any, want int) map[string]any {
		t.Helper()
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		a := provider.Actor{CredentialRole: provider.CredentialParticipant, CredentialIdentity: actor, ExerciseIdentity: actor}
		if actor == core.Administrator {
			a.CredentialRole = provider.CredentialOperator
		}
		req = req.WithContext(provider.WithActor(req.Context(), a))
		req.Header.Set("X-Simulator-Region", "us-east-1")
		req.Header.Set("Content-Type", "application/json")
		if op != "" {
			req.Header.Set("X-Amz-Target", "CodeCommit_20150413."+op)
			req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		} else {
			req.Header.Set("X-Simulator-Service", notificationService)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: %d, want %d: %s", path, op, rec.Code, want, rec.Body.String())
		}
		m := map[string]any{}
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	repo := post(engine, "/", "CreateRepository", core.Administrator, map[string]any{"repositoryName": "practice"}, 200)["repositoryMetadata"].(map[string]any)
	source := repo["Arn"]
	if source == nil {
		t.Fatalf("repository ARN missing: %v", repo)
	}
	request := map[string]any{"Name": "Branch practice", "Resource": source, "EventTypeIds": []string{codestarnotifications.BranchCreated}, "Targets": []any{}, "DetailType": "BASIC", "ClientRequestToken": "repeat"}
	post(g, "/createNotificationRule", "", "reader", request, 403)
	arn := post(g, "/createNotificationRule", "", core.Administrator, request, 200)["Arn"].(string)
	if got := post(g, "/createNotificationRule", "", core.Administrator, request, 200)["Arn"]; got != arn {
		t.Fatal("different token retry ARN", got)
	}
	request["Name"] = "Changed retry"
	post(g, "/createNotificationRule", "", core.Administrator, request, 400)
	ref := map[string]any{"Arn": arn}
	d := post(g, "/describeNotificationRule", "", core.Administrator, ref, 200)
	if d["Resource"] != source || d["Status"] != "ENABLED" || d["DetailType"] != "BASIC" || len(d["Targets"].([]any)) != 0 {
		t.Fatal(d)
	}
	if got := post(g, "/listNotificationRules", "", core.Administrator, map[string]any{}, 200)["NotificationRules"].([]any); len(got) != 1 || got[0].(map[string]any)["Arn"] != arn {
		t.Fatal(got)
	}
	post(g, "/updateNotificationRule", "", core.Administrator, map[string]any{"Arn": arn, "Status": "DISABLED"}, 200)
	if got := post(g, "/describeNotificationRule", "", core.Administrator, ref, 200)["Status"]; got != "DISABLED" {
		t.Fatal(got)
	}
	post(g, "/deleteNotificationRule", "", "reader", ref, 403)
	if got := post(g, "/deleteNotificationRule", "", core.Administrator, ref, 200)["Arn"]; got != arn {
		t.Fatal(got)
	}
	post(g, "/describeNotificationRule", "", core.Administrator, ref, 404)
}
