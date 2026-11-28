package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

func TestCognitoHostedTrustedPreparationFailureClosesStateForRetry(t *testing.T) {
	const origin = "https://127.0.0.1:19443"
	path := filepath.Join(t.TempDir(), "local-cognito.sqlite")
	cfg := DefaultProviderConfig()
	cfg.CognitoLocalFederationConfig = true
	refusal := errors.New("fixture stopped after initializing hosted state")
	var shared *cognitoidp.Store
	prepared := 0

	handler, stop, err := newWithProviders(path, cfg, func(store *cognitoidp.Store) error {
		prepared++
		shared = store
		browser, err := cognitoidp.NewHostedBrowser(store, localCognitoAccount, origin)
		if err != nil {
			return err
		}
		if browser == nil {
			return errors.New("fixture did not receive the live local Cognito store")
		}
		return refusal
	})
	if stop != nil {
		t.Cleanup(func() { _ = stop() })
	}
	if prepared != 1 || !errors.Is(err, refusal) || handler != nil || stop != nil || shared == nil {
		t.Fatalf("failed trusted preparation did not abort the composition: callbacks=%d, error=%v", prepared, err)
	}
	if browser, err := cognitoidp.NewHostedBrowser(shared, localCognitoAccount, origin); err == nil || browser != nil {
		t.Fatal("the rejected composition left its previously open backing store usable")
	}

	pair, closeState, err := NewWithLocalCognitoHosted(path, cfg, origin)
	if err != nil {
		t.Fatal("could not retry against the same initialized SQLite file", err)
	}
	t.Cleanup(func() {
		if err := closeState(); err != nil {
			t.Error("could not close the recovered composition", err)
		}
	})
	native, err := NewAccess(pair.AWS, federationTestKey, false)
	if err != nil {
		t.Fatal(err)
	}
	call := func(op, body string) *http.Response {
		t.Helper()
		req := federationSignedRequest(op, body, "", federationRegion, federationService, "127.0.0.1:34017", nil)
		return federationAssertResponse(t, native, req, http.StatusOK, "").Result()
	}
	created := call("CreateUserPool", `{"PoolName":"synthetic retry after failed setup"}`)
	defer created.Body.Close()
	var pool struct{ UserPool struct{ ID, ARN string } }
	if json.NewDecoder(created.Body).Decode(&pool) != nil || pool.UserPool.ID == "" ||
		pool.UserPool.ARN != "arn:aws:cognito-idp:"+federationRegion+":"+localCognitoAccount+":userpool/"+pool.UserPool.ID {
		t.Fatal("retry did not create a canonical signed native pool")
	}
	selector := federationWireBody(t, map[string]any{"UserPoolId": pool.UserPool.ID})
	sp, err := pair.Browser.ServiceProvider(t.Context(), pool.UserPool.ID)
	if err != nil || sp.EntityID != "urn:amazon:cognito:sp:"+pool.UserPool.ID ||
		sp.ACS != origin+"/_simulator/cognito/"+pool.UserPool.ID+"/saml2/idpresponse" {
		t.Fatal("recovered browser listener did not use the pool created via the signed native handler", err)
	}
	deleted := call("DeleteUserPool", selector)
	_ = deleted.Body.Close()
	if _, err := pair.Browser.ServiceProvider(t.Context(), pool.UserPool.ID); err == nil {
		t.Fatal("recovered browser listener retained a pool deleted via the native handler")
	}
}

func TestCognitoHostedDatabaseOpenFailureNeverReturnsHandlers(t *testing.T) {
	const origin = "https://127.0.0.1:19443"
	path := t.TempDir() // a deliberately unusable database path, also for a privileged test runner
	cfg := DefaultProviderConfig()
	cfg.CognitoLocalFederationConfig = true

	pair, closeState, err := NewWithLocalCognitoHosted(path, cfg, origin)
	if closeState != nil {
		t.Cleanup(func() { _ = closeState() })
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != path || pair.AWS != nil || pair.Browser != nil || closeState != nil {
		t.Fatalf("failed native database open unexpectedly published a handler or lost the exact failure: %v", err)
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Fatal("failed native open touched the directory instead of rejecting it")
	}
}
