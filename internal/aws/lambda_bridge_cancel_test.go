package aws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestSDKCancellationDoesNotWaitForBusyDatabaseOrStartLateMutation(t *testing.T) {
	db, s3 := s3TestState(t)
	parameters, err := NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	identities := core.NewIdentities(db)
	policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.Set("executor", policy); err != nil {
		t.Fatal(err)
	}
	if _, err = parameters.Put("us-east-1", "/own/value", "String", "sentinel", nil, false); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: db, S3: s3, Parameters: parameters, Identities: identities}
	invoke := gateway.lambdaExchange(LambdaFunction{Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor"}, "us-east-1")
	requests := []string{
		`{"service":"ssm","operation":"GetParameter","parameters":{"Name":"/own/value"}}`,
		`{"service":"ssm","operation":"GetParameters","parameters":{"Names":["/own/value"]}}`,
		`{"service":"s3","operation":"GetObject","parameters":{"Bucket":"versions-test","Key":"record"}}`,
		`{"service":"s3","operation":"PutObject","parameters":{"Bucket":"versions-test","Key":"record","Body":"bGF0ZS13cml0ZQ=="}}`,
		`{"service":"s3","operation":"DeleteObject","parameters":{"Bucket":"versions-test","Key":"record"}}`,
	}
	for _, request := range requests {
		t.Run(request, func(t *testing.T) {
			if _, err := s3.Put("versions-test", "record", "text/plain", []byte("sentinel"), "", ""); err != nil {
				t.Fatal(err)
			}
			held, err := db.Database().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			before := db.Database().Stats().WaitCount
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan json.RawMessage, 1)
			go func() { reply, _ := invoke(ctx, json.RawMessage(request)); done <- reply }()
			until := time.Now().Add(time.Second)
			for db.Database().Stats().WaitCount == before && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if db.Database().Stats().WaitCount == before {
				held.Close()
				t.Fatal("native callback never reached queued DB access")
			}
			cancel()
			var reply json.RawMessage
			returnedBeforeRelease := false
			select {
			case reply = <-done:
				returnedBeforeRelease = true
			case <-time.After(time.Second):
			}
			held.Close()
			if !returnedBeforeRelease {
				select {
				case reply = <-done:
				case <-time.After(time.Second):
					t.Fatal("callback leaked after DB release")
				}
				t.Error("canceled SDK callback waited for another DB connection")
			}
			if !strings.Contains(string(reply), `"error"`) {
				t.Errorf("canceled callback returned a successful service result: %s", reply)
			}
			value, err := s3.Get("versions-test", "record", "")
			if err != nil || string(value.Data) != "sentinel" {
				t.Error("mutation started after cancellation", err)
			}
		})
	}
}

func TestSDKStoresCancelQueuedWorkBeforeAcquiringConnection(t *testing.T) {
	db, s3 := s3TestState(t)
	parameters, err := NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{"parameter", func(ctx context.Context) error {
			_, err := parameters.GetContext(ctx, "us-east-1", "/own/value")
			return err
		}},
		{"bucket metadata", func(ctx context.Context) error { _, err := db.BucketContext(ctx, "versions-test"); return err }},
		{"put", func(ctx context.Context) error {
			_, err := s3.PutContext(ctx, "versions-test", "record", "text/plain", []byte("late"), "", "")
			return err
		}},
		{"get", func(ctx context.Context) error {
			_, err := s3.GetContext(ctx, "versions-test", "record", "")
			return err
		}},
		{"delete", func(ctx context.Context) error {
			_, err := s3.DeleteContext(ctx, "versions-test", "record", "")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			held, err := db.Database().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			done := make(chan error, 1)
			go func() { done <- test.run(ctx) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled store operation succeeded")
				}
			case <-time.After(time.Second):
				held.Close()
				<-done
				t.Fatal("canceled store waited for connection")
			}
		})
	}
}
