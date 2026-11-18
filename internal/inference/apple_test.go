package inference

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These executables are synthetic process-boundary doubles, not Swift/model
// verification. They run without a shell in production; the fixture itself is
// a script so errors, signals and pipe bounds can be exercised offline.
func appleFixture(t *testing.T, check, generate string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix process fixture; Apple hardware refusal is tested separately")
	}
	path := filepath.Join(t.TempDir(), "helper")
	script := "#!/bin/sh\nif [ \"$1\" = --check ]; then\n" + check + "\nelse\n" + generate + "\nfi\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func applePrint(value string) string { return "printf '%s' '" + value + "'" }

const appleSuccess = `{"version":1,"result":{"Text":"synthetic reply","Stop":"end","InputTokens":9,"OutputTokens":3}}`

func appleSynthetic() Request {
	return Request{Model: "system", System: "synthetic instructions", Messages: []Message{{RoleUser, "synthetic first"}, {RoleAssistant, "synthetic answer"}, {RoleUser, "synthetic last"}}, MaxTokens: 64}
}

func TestAppleNativeProcessTranslation(t *testing.T) {
	request := appleSynthetic()
	temperature, topP, topK := 0.4, 0.8, 4
	request.Temperature, request.TopP = &temperature, &topP
	path := appleFixture(t, applePrint(`{"version":1}`), `IFS= read -r input
case "$input" in
 *'"version":1,"model":"system","system":"synthetic instructions","messages":[{"role":"user","text":"synthetic first"},{"role":"assistant","text":"synthetic answer"},{"role":"user","text":"synthetic last"}],"maxTokens":64,"temperature":0.4,"topP":0.8'*) ;;
 *) exit 2;;
esac
`+applePrint(appleSuccess))
	a := &Apple{helper: path}
	if a.Name() != "apple" || !a.ValidModel("system") || a.ValidModel("https://cloud") || a.ValidModel("pcc") {
		t.Fatal("model selection")
	}
	if _, err := a.call(context.Background(), "--check", nil); err != nil {
		t.Fatal(err)
	}
	result, err := a.Generate(context.Background(), request)
	if err != nil || result.Text != "synthetic reply" || result.InputTokens != 9 || result.OutputTokens != 3 || result.Stop != StopEnd {
		t.Fatalf("result %v %v", result, err)
	}
	// A distinct request checks topK, and each call starts a fresh process.
	request.TopP, request.TopK = nil, &topK
	a.helper = appleFixture(t, applePrint(`{"version":1}`), `IFS= read -r input
case "$input" in *'"topK":4'*) ;; *) exit 2;; esac
`+applePrint(appleSuccess))
	if _, err = a.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestAppleRejectsUnsupportedRequestsBeforeProcess(t *testing.T) {
	a := &Apple{helper: "/missing-helper-must-not-run"}
	for name, alter := range map[string]func(*Request){
		"model":        func(r *Request) { r.Model = "cloud" },
		"empty":        func(r *Request) { r.Messages = nil },
		"maxZero":      func(r *Request) { r.MaxTokens = 0 },
		"maxLarge":     func(r *Request) { r.MaxTokens = 8193 },
		"assistantEnd": func(r *Request) { r.Messages = r.Messages[:2] },
		"role":         func(r *Request) { r.Messages[1].Role = RoleUser },
		"systemUTF8":   func(r *Request) { r.System = string([]byte{0xff}) },
		"optionsNaN":   func(r *Request) { value := math.NaN(); r.Temperature = &value },
		"utf8":         func(r *Request) { r.Messages[0].Text = string([]byte{0xff}) },
		"bothSampling": func(r *Request) { p, k := 0.5, 2; r.TopP = &p; r.TopK = &k },
	} {
		t.Run(name, func(t *testing.T) {
			r := appleSynthetic()
			alter(&r)
			_, err := a.Generate(context.Background(), r)
			if KindOf(err) != Unrepresentable {
				t.Fatalf("%v", err)
			}
		})
	}
	r := appleSynthetic()
	r.Messages[0].Text = strings.Repeat("x", MaxBackendRequestBytes)
	if _, err := a.Generate(context.Background(), r); KindOf(err) != TooLarge {
		t.Fatal(err)
	}
}

func TestAppleHelperFailureAndPrivacy(t *testing.T) {
	tests := []struct {
		name, body string
		kind       Kind
	}{
		{"missing", "exit 1", Unavailable},
		{"protocol", applePrint(`{"version":2}`), BackendFailure},
		{"malformed", applePrint(`synthetic-private`), BackendFailure},
		{"trailing", applePrint(`{"version":1} {}`), BackendFailure},
		{"unknownField", applePrint(`{"version":1,"unexpected":1}`), BackendFailure},
		{"resultMissing", applePrint(`{"version":1}`), Unrepresentable},
		{"wrongStop", applePrint(strings.ReplaceAll(appleSuccess, `"end"`, `"length"`)), Unrepresentable},
		{"negativeCount", applePrint(strings.ReplaceAll(appleSuccess, `:9`, `:-1`)), Unrepresentable},
		{"atLimit", applePrint(strings.ReplaceAll(appleSuccess, `:3`, `:64`)), Unrepresentable},
		{"overflow", "exec /bin/dd if=/dev/zero bs=1048577 count=2 2>/dev/null", BackendFailure},
	}
	for _, code := range []string{"deviceNotEligible", "appleIntelligenceNotEnabled", "modelNotReady", "unsupportedOS", "busy", "timeout", "unsupported", "tokenLimit", "contextLimit", "synthetic-private"} {
		kind := BackendFailure
		switch code {
		case "deviceNotEligible", "appleIntelligenceNotEnabled", "modelNotReady", "unsupportedOS":
			kind = Unavailable
		case "busy":
			kind = Busy
		case "timeout":
			kind = Timeout
		case "unsupported", "tokenLimit", "contextLimit":
			kind = Unrepresentable
		}
		data, _ := json.Marshal(appleReply{Version: 1, Error: code})
		tests = append(tests, struct {
			name, body string
			kind       Kind
		}{code, applePrint(string(data)), kind})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := &Apple{helper: appleFixture(t, applePrint(`{"version":1}`), "printf '%s' synthetic-private >&2\n"+test.body)}
			start := time.Now()
			_, err := a.Generate(context.Background(), appleSynthetic())
			if err == nil || KindOf(err) != test.kind || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatalf("%v", err)
			}
			if test.name == "overflow" && !strings.Contains(err.Error(), "output bound") {
				t.Fatalf("output was parsed instead of bounded: %v", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("process bound failed")
			}
		})
	}
	a := &Apple{helper: appleFixture(t, applePrint(appleSuccess), "exit 1")}
	if _, err := a.call(context.Background(), "--check", nil); KindOf(err) != BackendFailure {
		t.Fatal(err)
	}
}

func TestAppleCancellationKillsHelper(t *testing.T) {
	a := &Apple{helper: appleFixture(t, applePrint(`{"version":1}`), "exec /bin/sleep 20")}
	for _, deadline := range []bool{true, false} {
		var ctx context.Context
		var cancel context.CancelFunc
		want := Canceled
		if deadline {
			ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
			want = Timeout
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		start := time.Now()
		_, err := a.Generate(ctx, appleSynthetic())
		cancel()
		if KindOf(err) != want || time.Since(start) > time.Second {
			t.Fatalf("cancellation: %v", err)
		}
	}
}

func TestAppleConstructorProbesExplicitHelper(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if a, err := NewAppleFoundationModels("/synthetic-helper"); a != nil || KindOf(err) != Unavailable {
			t.Fatal(err)
		}
		return
	}
	for _, path := range []string{"", "relative", t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		if a, err := NewAppleFoundationModels(path); a != nil || KindOf(err) != Unavailable {
			t.Fatalf("%q: %v", path, err)
		}
	}
	path := appleFixture(t, applePrint(`{"version":1}`), applePrint(appleSuccess))
	a, err := NewAppleFoundationModels(path)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if a, err = NewAppleFoundationModels(path); a != nil || KindOf(err) != Unavailable {
		t.Fatal(err)
	}
	path = appleFixture(t, applePrint(`{"version":1,"error":"modelNotReady"}`), applePrint(appleSuccess))
	if a, err = NewAppleFoundationModels(path); a != nil || KindOf(err) != Unavailable {
		t.Fatal(err)
	}
}
