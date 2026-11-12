package aws

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestStackMutationOutlivesServerWriteTimeout(t *testing.T) {
	for _, tc := range []struct {
		action string
		ok     bool
	}{{"CreateStack", true}, {"DeleteStack", true}, {"DescribeStacks", false}} {
		t.Run(tc.action, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{WriteTimeout: 100 * time.Millisecond, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				extendStackMutationWriteDeadline(w, tc.action)
				time.Sleep(300 * time.Millisecond)
				_, _ = io.WriteString(w, "done")
			})}
			go func() { _ = srv.Serve(l) }()
			defer srv.Close()
			resp, err := http.Get("http://" + l.Addr().String())
			if !tc.ok {
				if err == nil {
					resp.Body.Close()
					t.Fatal("read-only action unexpectedly outlived the server write timeout")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != "done" {
				t.Fatalf("body=%q err=%v", body, err)
			}
		})
	}
}
