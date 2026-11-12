package aws

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestSSMDocumentStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	second := strings.Replace(documentTestContent, "echo first", "echo second", 1)
	store := func(t *testing.T, db *sql.DB, seeded bool) *SSMDocumentStore {
		t.Helper()
		s, err := NewSSMDocumentStore(db)
		if err != nil {
			t.Fatal(err)
		}
		if seeded {
			if _, err = s.Save(context.Background(), "us-east-1", "local-doc", documentTestContent, ""); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	t.Run("create", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := store(t, db, false)
			return func(ctx context.Context) error {
				_, err := s.Save(ctx, "us-east-1", "local-doc", documentTestContent, "")
				return err
			}
		})
	})
	t.Run("new version", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := store(t, db, true)
			return func(ctx context.Context) error {
				d, err := s.Save(ctx, "us-east-1", "local-doc", second, "$LATEST")
				if err == nil && d.LatestVersion != "2" {
					t.Fatalf("unexpected version %+v", d)
				}
				return err
			}
		})
	})
	t.Run("delete", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := store(t, db, true)
			return func(ctx context.Context) error { return s.Delete(ctx, "us-east-1", "local-doc") }
		})
	})
	t.Run("reads", func(t *testing.T) {
		sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
			s := store(t, db, true)
			return func(ctx context.Context) error {
				if d, err := s.Get(ctx, "us-east-1", "local-doc", "$LATEST"); err != nil {
					return err
				} else if d.Content != documentTestContent {
					t.Fatalf("read returned different content %q", d.Content)
				}
				list, err := s.List(ctx, "us-east-1")
				if err == nil && len(list) != 1 {
					t.Fatalf("list returned %d documents", len(list))
				}
				return err
			}
		})
	})
}
