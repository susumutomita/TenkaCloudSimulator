package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

func TestEC2InstanceStackLaunchDiscoveryRequiresLocalUnscopedOperator(t *testing.T) {
	operator := provider.Actor{CredentialRole: provider.CredentialOperator, CredentialIdentity: "simulator-admin", ExerciseIdentity: "simulator-admin"}
	participant := provider.Actor{CredentialRole: provider.CredentialParticipant, CredentialIdentity: "reader", ExerciseIdentity: "reader"}
	selected := operator
	selected.ExerciseIdentity = "reader"
	for _, tc := range []struct {
		name                             string
		actor                            *provider.Actor
		remote                           string
		auth, scoped, configured, expect bool
	}{
		{"configured local operator", &operator, "127.0.0.1:7111", true, false, true, true},
		{"configured IPv6 local operator", &operator, "[::1]:7111", true, false, true, true},
		{"default runtime", &operator, "127.0.0.1:7111", true, false, false, false},
		{"absent trusted actor", nil, "127.0.0.1:7111", true, false, true, false},
		{"no authentication wrapper", &operator, "127.0.0.1:7111", false, false, true, false},
		{"operator selected reader", &selected, "127.0.0.1:7111", true, false, true, false},
		{"participant", &participant, "127.0.0.1:7111", true, false, true, false},
		{"single-instance handoff", &operator, "127.0.0.1:7111", true, true, true, false},
		{"nonlocal connection", &operator, "192.0.2.1:7111", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/_simulator/v1/capabilities", nil)
			r.RemoteAddr = tc.remote
			w := httptest.NewRecorder()
			if tc.auth {
				w.Header().Set("X-Simulator-Authentication", "required")
			}
			if tc.actor != nil {
				r = r.WithContext(provider.WithActor(r.Context(), *tc.actor))
			}
			if tc.scoped {
				r = r.WithContext(context.WithValue(r.Context(), consoleScopeKey{}, consoleScope{InstanceID: "i-0123456789abcdef0"}))
			}
			ec2 := map[string]any{}
			caps := map[string]any{"services": map[string]any{"ec2": ec2}}
			var runtime func() bool
			if tc.configured {
				runtime = func() bool { return true }
			}
			addEC2InstanceStackLaunchCapability(w, r, caps, runtime)
			if got := ec2["instanceStackLaunch"]; tc.expect && got != "CloudFormation/CreateStack" || !tc.expect && got != nil {
				t.Fatalf("instance stack capability = %#v, expected availability %v", got, tc.expect)
			}
		})
	}
}
