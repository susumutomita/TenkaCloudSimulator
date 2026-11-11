package storage

import (
	"context"
	"errors"
	"testing"
)

func TestReadProjectObjectReadsOnlyObjectsOfTheNamedProjectWithinTheLimit(t *testing.T) {
	db, s := fixture(t, ":memory:", "source", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "one-bucket")
	create(t, s, "project-two", "two-bucket")
	upload(t, s, "project-one", "one-bucket", "dir/app.zip", "zip-bytes", "")
	upload(t, s, "project-two", "two-bucket", "app.zip", "other", "")
	ctx := context.Background()
	data, err := ReadProjectObject(ctx, db.Database(), "source", "project-one", "one-bucket", "dir/app.zip", 9)
	if err != nil || string(data) != "zip-bytes" {
		t.Fatalf("read: %q %v", data, err)
	}
	for name, c := range map[string]struct {
		environment, project, bucket, object string
		limit                                int64
		want                                 error
	}{
		"other project":     {"source", "project-one", "two-bucket", "app.zip", 100, ErrObjectNotFound},
		"missing object":    {"source", "project-one", "one-bucket", "missing", 100, ErrObjectNotFound},
		"missing bucket":    {"source", "project-one", "nobody", "app.zip", 100, ErrObjectNotFound},
		"other environment": {"elsewhere", "project-one", "one-bucket", "dir/app.zip", 100, ErrObjectNotFound},
		"too large":         {"source", "project-one", "one-bucket", "dir/app.zip", 8, ErrObjectTooLarge},
	} {
		if _, err := ReadProjectObject(ctx, db.Database(), c.environment, c.project, c.bucket, c.object, c.limit); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadProjectObject(cancelled, db.Database(), "source", "project-one", "one-bucket", "dir/app.zip", 100); err == nil {
		t.Fatal("a cancelled read succeeded")
	}
	sqlExec(t, db, "DROP TABLE gcp_storage_objects")
	if _, err := ReadProjectObject(ctx, db.Database(), "source", "project-one", "one-bucket", "dir/app.zip", 100); err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("a storage failure was reported as absence: %v", err)
	}
}
