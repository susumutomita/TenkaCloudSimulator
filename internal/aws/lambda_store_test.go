package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

func lambdaState(t *testing.T) (*core.Store, *LambdaStore, *core.Identities) {
	t.Helper()
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewLambdaStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	identities := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"executor", "replacement"} {
		if err = identities.Set(name, policy); err != nil {
			t.Fatal(err)
		}
	}
	return db, store, identities
}
func TestLambdaUpdateCannotRestoreStaleConfiguration(t *testing.T) {
	db, store, _ := lambdaState(t)
	old := LambdaFunction{Name: "same-name", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Handler: "index.handler", Revision: "old-id", Code: []byte("old code"), Environment: map[string]string{"SOURCE": "old"}}
	if err := store.save(&old, true, ""); err != nil {
		t.Fatal(err)
	}
	stale := old
	stale.Code = []byte("new uploaded code")
	stale.Revision = "upload-revision"
	if _, err := db.Database().Exec("DELETE FROM aws_lambda_functions"); err != nil {
		t.Fatal(err)
	}
	replacement := old
	replacement.Revision = "replacement-revision"
	replacement.Role = "arn:aws:iam::000000000000:role/replacement"
	replacement.Environment = map[string]string{"SOURCE": "replacement"}
	if err := store.save(&replacement, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.save(&stale, false, "old-id"); err == nil {
		t.Fatal("stale optimistic update accepted")
	}
	if err := store.save(&stale, false, ""); err != nil {
		t.Fatal(err)
	}
	actual, err := store.get(old.Region, old.Name)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Role != replacement.Role || actual.Environment["SOURCE"] != "replacement" || string(actual.Code) != "new uploaded code" {
		t.Fatalf("stale configuration restored: %+v", actual)
	}
}
func TestLambdaWriteFailureRollsBackAndCorruptionFailsClosed(t *testing.T) {
	db, store, identities := lambdaState(t)
	f := LambdaFunction{Name: "transactional", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Handler: "index.handler", Revision: "initial", Code: []byte("original")}
	if _, err := db.Database().Exec(`CREATE TRIGGER reject_function BEFORE INSERT ON aws_lambda_functions BEGIN SELECT RAISE(ABORT,'local write rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := store.save(&f, true, ""); err == nil {
		t.Fatal("failed SQL insert accepted")
	}
	var count int
	if err := db.Database().QueryRow("SELECT count(*) FROM aws_lambda_functions").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial function persisted", count, err)
	}
	if _, err := db.Database().Exec("DROP TRIGGER reject_function"); err != nil {
		t.Fatal(err)
	}
	if err := store.save(&f, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Database().Exec(`CREATE TRIGGER reject_update BEFORE UPDATE ON aws_lambda_functions BEGIN SELECT RAISE(ABORT,'local write rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	f.Code = []byte("must not persist")
	if err := store.save(&f, false, ""); err == nil {
		t.Fatal("failed SQL update accepted")
	}
	actual, err := store.get(f.Region, f.Name)
	if err != nil || string(actual.Code) != "original" {
		t.Fatal("failed update changed bytes", actual, err)
	}
	if _, err := db.Database().Exec("DROP TRIGGER reject_update; UPDATE aws_lambda_functions SET document='not valid JSON'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.get(f.Region, f.Name); err == nil {
		t.Fatal("corrupt function accepted")
	}
	if err := store.save(&f, false, ""); err == nil {
		t.Fatal("corrupt old function overwritten")
	}
	g := &Gateway{Store: db, Identities: identities, Lambdas: store}
	for _, path := range []string{"/2015-03-31/functions", "/2015-03-31/functions/transactional/configuration"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Simulator-Service", "lambda")
		g.ServeHTTP(w, r)
		if w.Code != 500 || strings.Contains(w.Body.String(), "not valid JSON") {
			t.Fatalf("corruption leaked or succeeded: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestLambdaUnavailableStateNeverReportsSuccess(t *testing.T) {
	db, store, identities := lambdaState(t)
	g := &Gateway{Store: db, Identities: identities, Lambdas: store}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ method, path, body string }{{"GET", "/2015-03-31/functions", ""}, {"GET", "/2015-03-31/functions/missing/configuration", ""}, {"DELETE", "/2015-03-31/functions/missing", ""}, {"POST", "/2015-03-31/functions/missing/invocations", "{}"}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(entry.method, entry.path, strings.NewReader(entry.body))
		r.Header.Set("X-Simulator-Service", "lambda")
		g.ServeHTTP(w, r)
		if w.Code != 500 || strings.Contains(w.Body.String(), "database is closed") {
			t.Fatalf("state failure leaked or succeeded: %d %s", w.Code, w.Body.String())
		}
	}
	if _, err := NewLambdaStore(db.Database()); err == nil {
		t.Fatal("closed DB initialized")
	}
	if err := store.save(&LambdaFunction{Name: "missing"}, true, ""); err == nil {
		t.Fatal("closed DB write accepted")
	}
	_ = store.Close()
	if _, err := store.runner(context.Background()); err == nil {
		t.Fatal("closed execution runtime reopened")
	}
	missing := &Gateway{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/2015-03-31/functions", nil)
	r.Header.Set("X-Simulator-Service", "lambda")
	missing.ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/2015-03-31/functions", nil)
	r.URL.RawQuery = "invalid=%zz"
	r.Header.Set("X-Simulator-Service", "lambda")
	g.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A nonadministrator policy lookup also fails closed when state is unavailable.
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/2015-03-31/functions", bytes.NewReader(nil))
	r.Header.Set("X-Simulator-Service", "lambda")
	r.Header.Set("X-Simulator-Principal", "reader")
	g.ServeHTTP(w, r)
	var response map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 500 || response["message"] != "Simulator function operation failed." {
		t.Fatal(w.Code, response)
	}
}

func TestClosingLambdaStoreCancelsColdInitialization(t *testing.T) {
	_, store, _ := lambdaState(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	allow := func() { releaseOnce.Do(func() { close(release) }) }
	defer allow()
	store.context = experimental.WithFunctionListenerFactory(store.context, experimental.FunctionListenerFactoryFunc(func(def api.FunctionDefinition) experimental.FunctionListener {
		if def.GoFunction() == nil {
			enteredOnce.Do(func() { close(entered); <-release })
		}
		return nil
	}))
	done := make(chan error, 1)
	request, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	go func() { _, err := store.runner(request); done <- err }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("guest compiler phase was not reached")
	}
	cancelRequest()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled client initialized runtime")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled client waited for paused compilation")
	}
	go func() { _, err := store.runner(context.Background()); done <- err }()
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		allow()
		t.Fatal("shutdown waited for a paused cold compile")
	}
	allow()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("initialization worker leaked")
	}
	if _, err := store.runner(context.Background()); err == nil {
		t.Fatal("closed runtime accepted a new invocation")
	}
	// The trusted fixed-module compiler may finish asynchronously; it must
	// release its runtime instead of publishing it into the closed store.
	select {
	case <-store.initialized:
	case <-time.After(2 * time.Minute):
		t.Fatal("cold compilation cleanup did not finish")
	}
	store.mu.Lock()
	published := store.runtime != nil
	store.mu.Unlock()
	if published {
		t.Fatal("runtime published after store shutdown")
	}
}
