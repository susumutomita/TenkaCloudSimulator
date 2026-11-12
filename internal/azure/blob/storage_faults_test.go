package blob

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"testing"
)

// Composition and every container and blob operation report an injected
// storage fault as a server error, never as success or a client refusal.
func TestAzureBlobReportsEveryStorageFault(t *testing.T) {
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		return func(context.Context) error {
			s, err := New(db, config("accountone", "owner"), permitted)
			if err != nil {
				return err
			}
			etag := ""
			for _, step := range []struct {
				method, path, body string
				headers            map[string]string
				want               int
			}{
				{"PUT", "/accountone/box?restype=container", "", map[string]string{"x-ms-meta-team": "a"}, 201},
				{"HEAD", "/accountone/box?restype=container", "", nil, 200},
				{"PUT", "/accountone/box/hello", "first", map[string]string{"x-ms-blob-type": "BlockBlob", "Content-Type": "text/plain", "If-None-Match": "*"}, 201},
				{"PUT", "/accountone/box/hello", "second", map[string]string{"x-ms-blob-type": "BlockBlob", "Content-Type": "text/plain", "If-Match": "$ETAG"}, 201},
				{"GET", "/accountone/box/hello", "", nil, 200},
				{"HEAD", "/accountone/box/hello", "", nil, 200},
				{"GET", "/accountone?comp=list", "", nil, 200},
				{"GET", "/accountone/box?restype=container&comp=list&include=metadata&maxresults=1", "", nil, 200},
				{"DELETE", "/accountone/box/hello", "", nil, 202},
				{"PUT", "/accountone/box/cascade", "child", map[string]string{"x-ms-blob-type": "BlockBlob"}, 201},
				{"DELETE", "/accountone/box?restype=container", "", nil, 202},
			} {
				r := httptest.NewRequest(step.method, step.path, bytes.NewReader([]byte(step.body)))
				r.Header.Set("x-ms-version", APIVersion)
				for k, v := range step.headers {
					if v == "$ETAG" {
						v = etag
					}
					r.Header.Set(k, v)
				}
				r = r.WithContext(WithActor(r.Context(), *learner()))
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				switch {
				case w.Code == step.want:
				case w.Code >= 500:
					return fmt.Errorf("%s %s: %d %s", step.method, step.path, w.Code, w.Body.String())
				default:
					t.Fatalf("storage fault misreported by %s %s: %d %s", step.method, step.path, w.Code, w.Body.String())
				}
				if tag := w.Header().Get("ETag"); tag != "" {
					etag = tag
				}
			}
			return nil
		}
	})
}
