package server

import (
	"errors"
	"slices"
	"testing"
	"testing/synctest"
)

func TestOwnedPostgresShutdownDrainsIndependentProvidersBeforeReleasingSQLite(t *testing.T) {
	debt := errors.New("unconfirmed PostgreSQL cleanup")
	lambdaErr := errors.New("Lambda cleanup failed")
	appEngineErr := errors.New("App Engine cleanup failed")
	stateErr := errors.New("SQLite close failed")
	for _, tc := range []struct {
		name                     string
		owner, lambda, appEngine error
		keepSQLite               bool
		want                     []error
	}{
		{"owner debt", debt, nil, nil, true, []error{debt}},
		{"independent errors do not hide owner debt", debt, lambdaErr, appEngineErr, true, []error{debt, lambdaErr, appEngineErr}},
		{"clean owner releases SQLite after draining providers", nil, nil, nil, false, []error{stateErr}},
		{"ordinary provider errors keep historical precedence", nil, lambdaErr, appEngineErr, false, []error{lambdaErr, appEngineErr}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				healthDone := make(chan struct{})
				result := make(chan error, 1)
				var called []string
				closer := func(name string, err error) func() error {
					return func() error {
						called = append(called, name)
						return err
					}
				}
				go func() {
					result <- closeProviderState(healthDone, closer("postgres", tc.owner),
						closer("lambda", tc.lambda), closer("appengine", tc.appEngine), closer("sqlite", stateErr))
				}()
				synctest.Wait()
				want := []string{"postgres", "lambda", "appengine"}
				if !slices.Equal(called, want) {
					t.Errorf("before the health loop joins: closures = %q, want %q", called, want)
				}
				select {
				case <-result:
					t.Fatal("shutdown returned before the health loop joined")
				default:
				}
				close(healthDone)
				err := <-result
				for _, expected := range []error{debt, lambdaErr, appEngineErr, stateErr} {
					if errors.Is(err, expected) != slices.Contains(tc.want, expected) {
						t.Errorf("shutdown cause %v missing or unexpected in: %v", expected, err)
					}
				}
				if !tc.keepSQLite {
					want = append(want, "sqlite")
				}
				if !slices.Equal(called, want) {
					t.Errorf("after shutdown: closures = %q, want %q", called, want)
				}
			})
		})
	}
}
