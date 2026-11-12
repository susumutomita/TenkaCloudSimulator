package iaas

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// Opening the scope and every disk operation surface an injected storage
// fault, and a fault-free retry on the same state then succeeds: no fault
// leaves a half-claimed scope or a disk the scope does not account for.
func TestDiskStoreReportsAndRecoversFromStorageFaults(t *testing.T) {
	sweepFaultsMode(t, faultRecover, func(t *testing.T, db *sql.DB) func(context.Context) error {
		return func(ctx context.Context) error {
			s, err := NewDiskStore(db, testConfig)
			if err != nil {
				return err
			}
			d, err := s.Create(ctx, input("data"))
			if err != nil {
				return err
			}
			got, err := s.Get(ctx, d.ID)
			if err != nil {
				return err
			}
			if got.Generation != d.Generation {
				t.Fatalf("read back %+v, created %+v", got, d)
			}
			if _, err = s.ListFromCount(ctx, 0, 10); err != nil {
				return err
			}
			name := "renamed"
			updated, err := s.UpdateNameDesc(ctx, d.ID, DiskPatch{Name: &name}, d.Generation)
			if err != nil {
				return err
			}
			if err = s.Delete(ctx, d.ID, updated.Generation); err != nil {
				return err
			}
			if _, err = s.Get(ctx, d.ID); err == nil || errors.Is(err, errInjectedStorage) {
				if err == nil {
					t.Fatal("deleted disk still readable")
				}
				return err
			}
			return nil
		}
	})
}
