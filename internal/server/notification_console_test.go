package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestNotificationConsoleCapability(t *testing.T) {
	handler, closeTeam, err := New(filepath.Join(t.TempDir(), "notifications.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTeam()
	for _, tc := range []struct {
		name                                 string
		actor, authenticated, bound, visible bool
	}{
		{name: "bare request"},
		{name: "trusted actor", actor: true},
		{name: "authenticated marker", authenticated: true},
		{name: "scoped handoff", actor: true, authenticated: true, bound: true},
		{name: "ordinary session", actor: true, authenticated: true, visible: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/_simulator/v1/capabilities", nil)
			req.Header.Set("X-Simulator-Authentication", "required")
			ctx := req.Context()
			if tc.actor {
				ctx = provider.WithActor(ctx, provider.Actor{CredentialRole: provider.CredentialParticipant, CredentialIdentity: "reader", ExerciseIdentity: "reader"})
			}
			if tc.bound {
				ctx = context.WithValue(ctx, consoleScopeKey{}, consoleScope{InstanceID: "i-0123456789abcdef0", Region: "us-east-1"})
			}
			w := httptest.NewRecorder()
			if tc.authenticated {
				w.Header().Set("X-Simulator-Authentication", "required")
			}
			handler.ServeHTTP(w, req.WithContext(ctx))
			var caps struct {
				Services map[string]struct {
					Console             bool
					Operations          []string
					Reference, Protocol string
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil || w.Code != http.StatusOK {
				t.Fatal(w.Code, err)
			}
			entry, exists := caps.Services["codestar-notifications"]
			expected := []string{"CreateNotificationRule", "DescribeNotificationRule", "ListNotificationRules", "UpdateNotificationRule", "DeleteNotificationRule"}
			if !exists || entry.Console != tc.visible || entry.Reference != "/_simulator/docs/codestar-notifications.html" || entry.Protocol != "REST JSON" || !slices.Equal(expected, entry.Operations) {
				t.Fatal(entry)
			}
		})
	}
}
