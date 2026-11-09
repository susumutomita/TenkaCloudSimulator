package aws

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func logTailRequest(g *Gateway, kind, logType, clientContext, principal string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/2015-03-31/functions/log-tail/invocations", strings.NewReader(`{}`))
	r.Header.Set("X-Simulator-Service", "lambda")
	r.Header.Set("X-Amz-Invocation-Type", kind)
	r.Header.Set("X-Amz-Log-Type", logType)
	r.Header.Set("X-Amz-Client-Context", clientContext)
	r.Header.Set("X-Simulator-Principal", principal)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestLambdaLogTailValidationAndAuthorization(t *testing.T) {
	db, store, identities := lambdaState(t)
	g := &Gateway{Store: db, Identities: identities, Lambdas: store}
	f := LambdaFunction{Name: "log-tail", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Code: []byte("not invoked")}
	if err := store.save(&f, true, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, logs, client string }{
		{"Event", "Tail", ""}, {"Event", "None", ""}, {"DryRun", "Tail", ""}, {"", "All", ""}, {"", "Tail", "e30="},
	} {
		w := logTailRequest(g, tc.kind, tc.logs, tc.client, "")
		if w.Code != 501 || w.Header().Get("X-Amzn-Errortype") != "NotImplementedException" || len(w.Header().Values("X-Amz-Log-Result")) != 0 {
			t.Fatalf("unsupported %#v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	for _, logs := range []string{"", "None"} {
		w := logTailRequest(g, "DryRun", logs, "", "")
		if w.Code != 204 || len(w.Header().Values("X-Amz-Log-Result")) != 0 {
			t.Fatalf("DryRun: %d %s", w.Code, w.Body.String())
		}
	}
	w := logTailRequest(g, "", "Tail", "", "executor") // SSM-only identity cannot invoke.
	if w.Code != 403 || len(w.Header().Values("X-Amz-Log-Result")) != 0 {
		t.Fatalf("denied invoke: %d %s", w.Code, w.Body.String())
	}
	if store.runtime != nil || store.initialized != nil {
		t.Fatal("validation or denied request started guest")
	}
}

func TestLambdaNativeLogTailFromGuest(t *testing.T) {
	db, store, identities := lambdaState(t)
	g := &Gateway{Store: db, Identities: identities, Lambdas: store}
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:000000000000:function:log-tail"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("invoker", policy); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name, source, suffix string
		failure              bool
	}{
		{"success", "print('a' * 70000)\n    print('actual final 猫')\n    return 'payload-only'", "actual final 猫\n\n", false},
		{"error", "print('before error', flush=True)\n    raise ValueError('payload-only')", "before error\n\n", true},
		{"timeout", "print('before timeout',end='',flush=True)\n    while True: pass", "before timeout", true},
		{"quiet", "return None", "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var code bytes.Buffer
			z := zip.NewWriter(&code)
			file, err := z.Create("index.py")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write([]byte("def handler(event, context):\n    " + tc.source + "\n")); err != nil {
				t.Fatal(err)
			}
			if err = z.Close(); err != nil {
				t.Fatal(err)
			}
			f := LambdaFunction{Name: "log-tail", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Handler: "index.handler", Timeout: 3, Code: code.Bytes()}
			if err = store.save(&f, i == 0, ""); err != nil {
				t.Fatal(err)
			}
			if tc.name == "success" {
				w := logTailRequest(g, "", "Tail", "", "invoker")
				if w.Code != 200 || w.Header().Get("X-Amz-Log-Result") == "" {
					t.Fatalf("default synchronous Tail: %d %s", w.Code, w.Body.String())
				}
			}
			for _, logs := range []string{"", "None", "Tail"} {
				w := logTailRequest(g, "RequestResponse", logs, "", "invoker")
				if w.Code != 200 || (w.Header().Get("X-Amz-Function-Error") != "") != tc.failure {
					t.Fatalf("invoke: %d %s", w.Code, w.Body.String())
				}
				if logs != "Tail" {
					if len(w.Header().Values("X-Amz-Log-Result")) != 0 {
						t.Fatal("unsolicited logs")
					}
					continue
				}
				tail, err := base64.StdEncoding.DecodeString(w.Header().Get("X-Amz-Log-Result"))
				if err != nil || len(tail) > 4096 || !bytes.HasSuffix(tail, []byte(tc.suffix)) || bytes.Contains(tail, []byte("TCSIM_")) || bytes.Contains(tail, []byte("payload-only")) {
					t.Fatalf("invalid tail len=%d err=%v tail=%q", len(tail), err, tail)
				}
				if tc.name == "success" && len(tail) != 4096 {
					t.Fatal("incorrect truncation")
				}
			}
		})
	}
}
