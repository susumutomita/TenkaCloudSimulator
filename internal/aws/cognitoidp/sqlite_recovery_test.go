package cognitoidp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewjam/saml"
	"github.com/ncruces/go-sqlite3"
)

type cognitoRecoveryFixture struct {
	path   string
	scope  Scope
	poolID string
	idp    hostedSignedIDP
}

func newCognitoRecoveryFixture(t *testing.T) (cognitoRecoveryFixture, *Store) {
	t.Helper()
	f := cognitoRecoveryFixture{
		path:  filepath.Join(t.TempDir(), "recovery.sqlite"),
		scope: Scope{Account: "012345678901", Region: "us-east-1"}, idp: newHostedSignedIDP(t),
	}
	s := f.openWritable(t)
	row, err := s.create(t.Context(), f.scope, "sqlite recovery pool")
	if err != nil {
		t.Fatal(err)
	}
	f.poolID = row.ID
	return f, s
}

func (f cognitoRecoveryFixture) openWritable(t *testing.T) *Store {
	t.Helper()
	_, core := federationTestPool(t, f.path)
	s, err := NewStoreWithLocalFederationConfig(core.Database())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f cognitoRecoveryFixture) openReadonly(t *testing.T) *sql.DB {
	t.Helper()
	uri := (&url.URL{Scheme: "file", Path: f.path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (f cognitoRecoveryFixture) dismiss(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f cognitoRecoveryFixture) native(t *testing.T, s *Store, op string, input map[string]any, status int) *httptest.ResponseRecorder {
	t.Helper()
	if input == nil {
		input = map[string]any{}
	}
	input["UserPoolId"] = f.poolID
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	admissions := 0
	handler := NewHTTPHandler(s, f.scope, Actor{"fixture-operator", "test-exercise"}, func(_ context.Context, action, arn string) bool {
		admissions++
		return action == "cognito-idp:"+op && arn == f.scope.arn(f.poolID)
	})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4566/", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("Content-Type", JSONMediaType)
	req.Header.Set("X-Amz-Target", TargetPrefix+op)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if admissions != 1 || w.Code != status || w.Header().Get("Content-Type") != JSONMediaType {
		t.Fatalf("%s: scoped native operation returned %d, wanted %d", op, w.Code, status)
	}
	if status == http.StatusInternalServerError {
		var result map[string]string
		if w.Header().Get("X-Amzn-Errortype") != "InternalErrorException" || json.Unmarshal(w.Body.Bytes(), &result) != nil ||
			len(result) != 2 || result["__type"] != "InternalErrorException" || result["message"] != "Local user pool state operation failed." {
			t.Fatal("unwritable native storage reported a success or exposed its failure details")
		}
	}
	return w
}

func (f cognitoRecoveryFixture) addProvider(t *testing.T, s *Store, status int) {
	t.Helper()
	f.native(t, s, nativeCreateIdentityProvider, map[string]any{
		"ProviderName": "Entra", "ProviderType": "SAML", "ProviderDetails": map[string]string{"MetadataFile": f.idp.meta},
	}, status)
}

func (f cognitoRecoveryFixture) addClient(t *testing.T, s *Store, name string, status int) string {
	t.Helper()
	w := f.native(t, s, nativeCreateUserPoolClient, map[string]any{
		"ClientName": name, "AllowedOAuthFlowsUserPoolClient": true, "AllowedOAuthFlows": []string{"code"},
		"CallbackURLs": []string{hostedFixtureCallback}, "SupportedIdentityProviders": []string{"Entra"}, "AllowedOAuthScopes": []string{"openid"},
	}, status)
	if status != http.StatusOK {
		return ""
	}
	var result struct{ UserPoolClient struct{ ClientID string } }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || !validFederationClientID(result.UserPoolClient.ClientID) {
		t.Fatal("successful native configuration did not return a usable client")
	}
	return result.UserPoolClient.ClientID
}

func (f cognitoRecoveryFixture) browser(t *testing.T, s *Store) *hostedBrowser {
	t.Helper()
	b, err := NewHostedBrowser(s, f.scope.Account, hostedFixtureOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return b.(*hostedBrowser)
}

func (f cognitoRecoveryFixture) complete(t *testing.T, b *hostedBrowser, client string) (hostedLogin, *httptest.ResponseRecorder) {
	t.Helper()
	l := beginHostedSignedLogin(t, b, f.poolID, client, "Entra")
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "fixture@example.invalid"}
	return l, f.idp.post(t, b, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
}

func (f cognitoRecoveryFixture) requireNoNativeMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_table_info('aws_cognito_user_pools')
		WHERE name IN ('federation_state','federation_hosted_state')`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("refused optional migration changed the canonical schema", err)
	}
}

func (f cognitoRecoveryFixture) slots(t *testing.T, db *sql.DB) [2][]byte {
	t.Helper()
	var raw [2][]byte
	err := db.QueryRowContext(t.Context(), `SELECT federation_state,federation_hosted_state FROM aws_cognito_user_pools
		WHERE account=? AND region=? AND pool_id=?`, f.scope.Account, f.scope.Region, f.poolID).Scan(&raw[0], &raw[1])
	if err != nil || len(raw[0]) <= federationRevisionBytes || len(raw[1]) == 0 {
		t.Fatal("configured pool with real Hosted state was not readable", err)
	}
	return raw
}

func TestCognitoReadOnlyRemountPreservesScopedNativeAndHostedState(t *testing.T) {
	f, s := newCognitoRecoveryFixture(t)
	f.dismiss(t, s.db)
	ro := f.openReadonly(t)
	readable, err := NewStoreWithLocalFederationConfig(ro)
	if err != nil {
		t.Fatal(err)
	}
	f.native(t, readable, "DescribeUserPool", nil, http.StatusOK)
	f.addProvider(t, readable, http.StatusInternalServerError)
	f.requireNoNativeMigration(t, ro)
	f.dismiss(t, ro)

	s = f.openWritable(t)
	f.addProvider(t, s, http.StatusOK)
	client := f.addClient(t, s, "retained app", http.StatusOK)
	b := f.browser(t, s)
	l, completed := f.complete(t, b, client)
	redeemHostedSignedLogin(t, b, l, completed)
	before := f.slots(t, s.db)
	f.dismiss(t, s.db)

	// A configured database can initialize its read-only projections, but
	// neither a new SDK client nor a browser sign-in may report an uncommitted write.
	ro = f.openReadonly(t)
	readable, err = NewStoreWithLocalFederationConfig(ro)
	if err != nil {
		t.Fatal(err)
	}
	b = f.browser(t, readable)
	if w := f.native(t, readable, nativeDescribeIdentityProvider, map[string]any{"ProviderName": "Entra"}, http.StatusOK); !strings.Contains(w.Body.String(), fixtureHostedIDP) {
		t.Fatal("read-only mode lost the registered IdP")
	}
	if w := f.native(t, readable, nativeDescribeUserPoolClient, map[string]any{"ClientId": client}, http.StatusOK); !strings.Contains(w.Body.String(), client) {
		t.Fatal("read-only mode lost the registered client")
	}
	hostedFixturePublicKeys(t, b, f.poolID)
	f.addClient(t, readable, "retryable app", http.StatusInternalServerError)
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
		"scope": {"openid"}, "state": {fixtureHostedState}, "nonce": {fixtureHostedNonce},
		"code_challenge": {hostedDigest(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}, "identity_provider": {"Entra"}}
	requireNoHostedResult(t, hostedServe(b, hostedFixtureRequest(http.MethodGet, hostedPrefix+f.poolID+"/"+hostedAuthorize+"?"+q.Encode(), nil)))
	f.dismiss(t, ro)

	s = f.openWritable(t)
	after := f.slots(t, s.db)
	if !bytes.Equal(after[0], before[0]) || !bytes.Equal(after[1], before[1]) {
		t.Fatal("read-only attempts altered committed native or browser state")
	}
	retry := f.addClient(t, s, "retryable app", http.StatusOK)
	b = f.browser(t, s)
	l, completed = f.complete(t, b, retry)
	redeemHostedSignedLogin(t, b, l, completed)
}

func TestCognitoSQLiteCommitContentionDoesNotPublishOrConsumeResults(t *testing.T) {
	f, s := newCognitoRecoveryFixture(t)
	ctx := t.Context()
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatal("fixture requires the production rollback journal", err)
	}
	// Reduce this fixture's busy timeout to avoid sleep or timing races. A
	// different real SQLite connection holds the normal shared read lock.
	if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	reader := f.openReadonly(t)
	holdRead := func() *sql.Tx {
		t.Helper()
		tx, err := reader.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM aws_cognito_user_pools WHERE account=? AND region=? AND pool_id=?`,
			f.scope.Account, f.scope.Region, f.poolID).Scan(&name); err != nil || name != "sqlite recovery pool" {
			t.Fatal("reader did not hold the owned row's snapshot", err)
		}
		return tx
	}
	tx := holdRead()
	f.addProvider(t, s, http.StatusInternalServerError)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.requireNoNativeMigration(t, s.db)
	f.addProvider(t, s, http.StatusOK)
	client := f.addClient(t, s, "checkout app", http.StatusOK)
	b := f.browser(t, s)
	l, completed := f.complete(t, b, client)
	cb, _ := assertHostedCompletion(t, l, completed)
	exchange := func() *http.Request { return hostedExchangeRequest(l, cb.Query().Get("code"), hostedOrigin(cb)) }
	before := f.slots(t, s.db)

	tx = holdRead()
	observed := httptest.NewRecorder()
	if err := b.token(observed, exchange(), f.scope, f.poolID); !errors.Is(err, sqlite3.BUSY) || observed.Body.Len() != 0 ||
		observed.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("blocked SQLite commit was not an unpublished retryable failure", err)
	}
	requireNoHostedResult(t, hostedServe(b, exchange()))
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after := f.slots(t, s.db)
	if !bytes.Equal(after[0], before[0]) || !bytes.Equal(after[1], before[1]) {
		t.Fatal("blocked commit changed the native enrollment or burned the issued code")
	}
	redeemHostedSignedLogin(t, b, l, completed)
	requireNoHostedResult(t, hostedServe(b, exchange()))
}
