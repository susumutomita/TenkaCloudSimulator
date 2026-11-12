package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

type nativeCommandRouteBackend struct{ resolves, dispatches int }

func (b *nativeCommandRouteBackend) ResolveCommandTarget(context.Context, string, string) (aws.EC2GuestLease, error) {
	b.resolves++
	return aws.EC2GuestLease{}, aws.ErrSSMCommandUnknown
}
func (b *nativeCommandRouteBackend) Dispatch(context.Context, guestcommand.DurableIntent) error {
	b.dispatches++
	return errors.New("no guest effect permitted in routing fixture")
}
func (*nativeCommandRouteBackend) Observe(context.Context, guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
	return guestcommand.DurableObservation{}, aws.ErrSSMCommandUnknown
}

// Real Access signature verification and Gateway dispatch, with a deliberately
// refusing provider. This establishes no guest execution or VM readiness.
func TestRunCommandNativeGatewayUsesVerifiedRole(t *testing.T) {
	a, _, valid := terminalFixture(t)
	c, err := a.sdk.Issue(aws.STSSessionRequest{Principal: "operator", RoleARN: "arn:aws:iam::000000000000:role/operator", RoleID: "AROAABCDEFGHIJKLMNOP", SessionName: "disruption", Duration: 15 * time.Minute, Validate: func(context.Context) error {
		if !*valid {
			return errors.New("revoked")
		}
		return nil
	}, Snapshot: func(context.Context) (core.Policy, error) {
		return core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:SendCommand"}, Resource: core.StringList{"*"}}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	backend := &nativeCommandRouteBackend{}
	store, err := aws.NewSSMCommandStore(db.Database(), backend)
	if err != nil {
		t.Fatal(err)
	}
	api, err := aws.NewSSMCommandAPI(store)
	if err != nil {
		t.Fatal(err)
	}
	a.next = &aws.Gateway{Commands: api}
	body := `{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["systemctl stop nginx"]},"TimeoutSeconds":30}`
	request := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	w := request(signedNativeSession(c, "AmazonSSM.SendCommand", body))
	if w.Code != 500 || backend.resolves != 1 || backend.dispatches != 0 {
		t.Fatalf("signed role route: status=%d resolves=%d dispatch=%d body=%s", w.Code, backend.resolves, backend.dispatches, w.Body.String())
	}
	*valid = false
	if w = request(signedNativeSession(c, "AmazonSSM.SendCommand", body)); w.Code != 403 || backend.resolves != 1 {
		t.Fatal("revoked role reached provider")
	}
	*valid = true
	r := signedNativeSession(c, "AmazonSSM.SendCommand", body)
	r.Header.Set("X-Amz-Target", "AmazonSSM.ListCommands")
	if w = request(r); w.Code != 403 || backend.resolves != 1 {
		t.Fatal("unsigned target mutation accepted")
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM.SendCommand")
	r.Header.Set("X-Simulator-Token", strings.Repeat("a", 32))
	if w = request(r); w.Code != 403 || backend.resolves != 1 {
		t.Fatal("operator transport token became native role")
	}
}
