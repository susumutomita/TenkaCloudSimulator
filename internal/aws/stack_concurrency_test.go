package aws

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestDelayedDeleteCannotRemoveReclaimedResources(t *testing.T) {
	path := t.TempDir() + "/state.sqlite"
	first, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	p, _ := NewParameterStore(first.Database())
	s, _ := NewStackStore(first.Database())
	other, _ := NewStackStore(second.Database())
	original := StackRecord{Name: "old", ID: "old-id", Region: "us-east-1", Resources: []StackResource{{Type: "AWS::SSM::Parameter", PhysicalID: "/shared/name", Properties: map[string]any{"Type": "String", "Value": "old"}}}}
	if err = s.Create(original); err != nil {
		t.Fatal(err)
	}
	// Pause precisely BEFORE the delayed request acquires its write transaction.
	// The pre-fix implementation had already read the old record at this point.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	connection, err := first.Database().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error {
		return raw.(driver.Conn).Raw().SetAuthorizer(func(action sqlite3.AuthorizerActionCode, operation, _, _, _ string) sqlite3.AuthorizerReturnCode {
			if action == sqlite3.AUTH_TRANSACTION && strings.EqualFold(operation, "BEGIN") {
				once.Do(func() { close(entered); <-release })
			}
			return sqlite3.AUTH_OK
		})
	})
	connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Delete("us-east-1", original.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("delete did not reach transaction")
	}
	// The other connection completes a deletion, then reuses physical names.
	if err = other.Delete("us-east-1", original.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	replacement := original
	replacement.Name = "replacement"
	replacement.ID = "replacement-id"
	replacement.Resources = []StackResource{{Type: "AWS::SSM::Parameter", PhysicalID: "/shared/name", Properties: map[string]any{"Type": "String", "Value": "replacement sentinel"}}}
	if err = other.Create(replacement); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("stale old stack deletion reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed deletion did not finish")
	}
	got, err := p.Get("us-east-1", "/shared/name")
	if err != nil || got.Value != "replacement sentinel" {
		t.Fatal("stale delete damaged replacement", got, err)
	}
	records, err := other.Records("us-east-1", replacement.ID)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
}
