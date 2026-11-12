package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Construction and the bucket/object lifecycle report every injected storage
// fault as an opaque 500, never as success or a client error, and never leak
// driver detail to the caller.
func TestStorageLifecycleReportsEveryStorageFault(t *testing.T) {
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		return func(context.Context) error {
			s, err := New(db, Config{Environment: "sweep", Projects: []Project{{"project-one", "1001"}}, AuthorizeTx: projectPolicy})
			if err != nil {
				return err
			}
			for _, step := range []struct{ method, path, body, contentType string }{
				{"POST", "/storage/v1/b?project=project-one", `{"name":"sweep-bucket"}`, "application/json"},
				{"GET", "/storage/v1/b?project=project-one", "", ""},
				{"GET", "/storage/v1/b/sweep-bucket", "", ""},
				{"POST", "/upload/storage/v1/b/sweep-bucket/o?uploadType=media&name=key&ifGenerationMatch=0", "first", "text/plain"},
				{"POST", "/upload/storage/v1/b/sweep-bucket/o?uploadType=media&name=key", "second", "text/plain"},
				{"GET", objectPath("sweep-bucket", "key"), "", ""},
				{"GET", objectPath("sweep-bucket", "key") + "?alt=media", "", ""},
				{"GET", "/storage/v1/b/sweep-bucket/o?maxResults=1", "", ""},
				{"DELETE", objectPath("sweep-bucket", "key"), "", ""},
				{"DELETE", "/storage/v1/b/sweep-bucket", "", ""},
			} {
				w := request(s, "project-one", step.method, step.path, step.body, step.contentType)
				switch {
				case w.Code == 200 || w.Code == 204:
				case w.Code == 500:
					if strings.Contains(w.Body.String(), errInjectedStorage.Error()) {
						t.Fatalf("driver detail leaked: %s", w.Body.String())
					}
					return fmt.Errorf("%s %s: %s", step.method, step.path, w.Body.String())
				default:
					t.Fatalf("storage fault misreported by %s %s: %d %s", step.method, step.path, w.Code, w.Body.String())
				}
			}
			return nil
		}
	})
}
