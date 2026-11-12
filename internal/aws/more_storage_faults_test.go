package aws

import (
	"context"
	"database/sql"
	"testing"
)

// sweepSteps runs one storage fault sweep per step on a freshly built state.
func sweepSteps[S any](t *testing.T, build func(t *testing.T, db *sql.DB, seeded bool) S, steps []struct {
	name   string
	seeded bool
	op     func(ctx context.Context, s S) error
}) {
	t.Helper()
	for _, step := range steps {
		step := step
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			sweepStorageFaults(t, true, func(t *testing.T, db *sql.DB) func(context.Context) error {
				s := build(t, db, step.seeded)
				return func(ctx context.Context) error { return step.op(ctx, s) }
			})
		})
	}
}

type apiFaultState struct {
	s     *APIGatewayStore
	api   RESTAPI
	child RESTResource
}

func TestAPIGatewayStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	build := func(t *testing.T, db *sql.DB, seeded bool) apiFaultState {
		t.Helper()
		s, err := NewAPIGatewayStore(db)
		if err != nil {
			t.Fatal(err)
		}
		st := apiFaultState{s: s}
		if seeded {
			ctx := context.Background()
			if st.api, err = s.CreateAPI(ctx, "us-east-1", "faults", "d", map[string]string{"team": "a"}); err != nil {
				t.Fatal(err)
			}
			if st.child, err = s.CreateResource(ctx, "us-east-1", st.api.ID, st.api.RootResourceID, "child"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.PutMethod(ctx, "us-east-1", st.api.ID, st.child.ID, "GET", nil); err != nil {
				t.Fatal(err)
			}
		}
		return st
	}
	sweepSteps(t, build, []struct {
		name   string
		seeded bool
		op     func(ctx context.Context, s apiFaultState) error
	}{
		{"create api", false, func(ctx context.Context, st apiFaultState) error {
			_, err := st.s.CreateAPI(ctx, "us-east-1", "fresh", "d", map[string]string{"team": "a"})
			return err
		}},
		{"create resource", true, func(ctx context.Context, st apiFaultState) error {
			_, err := st.s.CreateResource(ctx, "us-east-1", st.api.ID, st.child.ID, "{id}")
			return err
		}},
		{"put method", true, func(ctx context.Context, st apiFaultState) error {
			_, err := st.s.PutMethod(ctx, "us-east-1", st.api.ID, st.api.RootResourceID, "POST", nil)
			return err
		}},
		{"reads", true, func(ctx context.Context, st apiFaultState) error {
			if _, err := st.s.API(ctx, "us-east-1", st.api.ID); err != nil {
				return err
			}
			if apis, err := st.s.APIs(ctx, "us-east-1", "", 10); err != nil {
				return err
			} else if len(apis) != 1 {
				t.Fatalf("apis %v", apis)
			}
			if _, err := st.s.Resource(ctx, "us-east-1", st.api.ID, st.child.ID); err != nil {
				return err
			}
			resources, err := st.s.Resources(ctx, "us-east-1", st.api.ID, "", 10)
			if err == nil && len(resources) != 2 {
				t.Fatalf("resources %v", resources)
			}
			return err
		}},
		{"delete resource", true, func(ctx context.Context, st apiFaultState) error {
			return st.s.DeleteResource(ctx, "us-east-1", st.api.ID, st.child.ID)
		}},
		{"delete api", true, func(ctx context.Context, st apiFaultState) error { return st.s.DeleteAPI(ctx, "us-east-1", st.api.ID) }},
	})
}

type s3FaultState struct {
	s      *S3Store
	upload S3Upload
	part   S3Part
}

func TestS3StoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	build := func(t *testing.T, db *sql.DB, seeded bool) s3FaultState {
		t.Helper()
		s, err := NewS3Store(db)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Create("faults-bucket", "us-east-1"); err != nil {
			t.Fatal(err)
		}
		st := s3FaultState{s: s}
		if seeded {
			ctx := context.Background()
			if _, err = s.PutContext(ctx, "faults-bucket", "object", "text/plain", []byte("v1"), "", ""); err != nil {
				t.Fatal(err)
			}
			if st.upload, err = s.CreateUpload(ctx, "faults-bucket", "large", "application/octet-stream"); err != nil {
				t.Fatal(err)
			}
			if st.part, err = s.UploadPart(ctx, "faults-bucket", "large", st.upload.ID, 1, []byte("part-one")); err != nil {
				t.Fatal(err)
			}
		}
		return st
	}
	sweepSteps(t, build, []struct {
		name   string
		seeded bool
		op     func(ctx context.Context, s s3FaultState) error
	}{
		{"create bucket", false, func(_ context.Context, st s3FaultState) error { return st.s.Create("another-bucket", "us-east-1") }},
		{"put object version", true, func(ctx context.Context, st s3FaultState) error {
			_, err := st.s.PutContext(ctx, "faults-bucket", "object", "text/plain", []byte("v2"), "", "")
			return err
		}},
		{"read and delete object", true, func(ctx context.Context, st s3FaultState) error {
			got, err := st.s.GetContext(ctx, "faults-bucket", "object", "")
			if err != nil {
				return err
			}
			if string(got.Data) != "v1" {
				t.Fatalf("read %q", got.Data)
			}
			if versions, err := st.s.Versions("faults-bucket"); err != nil {
				return err
			} else if len(versions) != 1 {
				t.Fatalf("versions %v", versions)
			}
			_, err = st.s.DeleteContext(ctx, "faults-bucket", "object", "")
			return err
		}},
		{"create upload", true, func(ctx context.Context, st s3FaultState) error {
			_, err := st.s.CreateUpload(ctx, "faults-bucket", "other", "")
			return err
		}},
		{"upload part", true, func(ctx context.Context, st s3FaultState) error {
			_, err := st.s.UploadPart(ctx, "faults-bucket", "large", st.upload.ID, 2, []byte("part-two"))
			return err
		}},
		{"list uploads and parts", true, func(ctx context.Context, st s3FaultState) error {
			if uploads, err := st.s.Uploads(ctx, "faults-bucket"); err != nil {
				return err
			} else if len(uploads) != 1 {
				t.Fatalf("uploads %v", uploads)
			}
			parts, err := st.s.Parts(ctx, "faults-bucket", "large", st.upload.ID)
			if err == nil && len(parts) != 1 {
				t.Fatalf("parts %v", parts)
			}
			return err
		}},
		{"complete upload", true, func(ctx context.Context, st s3FaultState) error {
			_, err := st.s.CompleteUpload(ctx, "faults-bucket", "large", st.upload.ID, []S3Part{st.part})
			return err
		}},
		{"abort upload", true, func(ctx context.Context, st s3FaultState) error {
			return st.s.AbortUpload(ctx, "faults-bucket", "large", st.upload.ID)
		}},
	})
}

type kmsFaultState struct {
	s    *KMSStore
	key  KMSKeyMetadata
	blob []byte
}

func TestKMSStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	build := func(t *testing.T, db *sql.DB, seeded bool) kmsFaultState {
		t.Helper()
		s, err := NewKMSStore(db)
		if err != nil {
			t.Fatal(err)
		}
		st := kmsFaultState{s: s}
		if seeded {
			if st.key, err = s.CreateKey(context.Background(), "us-east-1", "faults"); err != nil {
				t.Fatal(err)
			}
			if st.blob, _, err = s.Encrypt(context.Background(), "us-east-1", st.key.KeyID, map[string]string{"purpose": "faults"}, []byte("plain")); err != nil {
				t.Fatal(err)
			}
		}
		return st
	}
	sweepSteps(t, build, []struct {
		name   string
		seeded bool
		op     func(ctx context.Context, s kmsFaultState) error
	}{
		{"create key", false, func(ctx context.Context, st kmsFaultState) error {
			_, err := st.s.CreateKey(ctx, "us-east-1", "fresh")
			return err
		}},
		{"disable key", true, func(ctx context.Context, st kmsFaultState) error {
			return st.s.SetEnabled(ctx, "us-east-1", st.key.KeyID, false)
		}},
		{"describe and list", true, func(ctx context.Context, st kmsFaultState) error {
			if _, err := st.s.DescribeKey(ctx, "us-east-1", st.key.KeyID); err != nil {
				return err
			}
			list, err := st.s.ListKeys(ctx, "us-east-1", 10, "")
			if err == nil && len(list.Keys) != 1 {
				t.Fatalf("keys %v", list)
			}
			return err
		}},
		{"encrypt", true, func(ctx context.Context, st kmsFaultState) error {
			_, _, err := st.s.Encrypt(ctx, "us-east-1", st.key.KeyID, nil, []byte("other"))
			return err
		}},
		{"decrypt", true, func(ctx context.Context, st kmsFaultState) error {
			plain, _, err := st.s.Decrypt(ctx, "us-east-1", "", map[string]string{"purpose": "faults"}, st.blob)
			if err == nil && string(plain) != "plain" {
				t.Fatalf("decrypted %q", plain)
			}
			return err
		}},
	})
}

type snsFaultState struct {
	s     *SNSStore
	topic SNSTopic
	sub   SNSSubscription
}

func TestSNSStoreStorageFaultsNeverPartiallyCommit(t *testing.T) {
	build := func(t *testing.T, db *sql.DB, seeded bool) snsFaultState {
		t.Helper()
		s, err := NewSNSStore(db)
		if err != nil {
			t.Fatal(err)
		}
		st := snsFaultState{s: s}
		if seeded {
			if st.topic, _, err = s.CreateTopic("us-east-1", "faults", "Faults", false); err != nil {
				t.Fatal(err)
			}
			if st.sub, err = s.Subscribe(st.topic, "email", "ops@example.com"); err != nil {
				t.Fatal(err)
			}
		}
		return st
	}
	sweepSteps(t, build, []struct {
		name   string
		seeded bool
		op     func(ctx context.Context, s snsFaultState) error
	}{
		{"create topic", false, func(_ context.Context, st snsFaultState) error {
			_, _, err := st.s.CreateTopic("us-east-1", "fresh", "Fresh", false)
			return err
		}},
		{"subscribe", true, func(ctx context.Context, st snsFaultState) error {
			_, err := st.s.Subscribe(st.topic, "email", "audit@example.com")
			return err
		}},
		{"publish", true, func(ctx context.Context, st snsFaultState) error {
			_, n, err := st.s.Publish(st.topic, "message", "subject")
			if err == nil && n != 1 {
				t.Fatalf("delivered to %d subscriptions", n)
			}
			return err
		}},
		{"reads", true, func(ctx context.Context, st snsFaultState) error {
			if _, err := st.s.GetTopic(st.topic.ARN); err != nil {
				return err
			}
			if topics, err := st.s.ListTopics(ctx, "us-east-1"); err != nil {
				return err
			} else if len(topics) != 1 {
				t.Fatalf("topics %v", topics)
			}
			if _, err := st.s.GetSubscription(st.sub.ARN); err != nil {
				return err
			}
			if subs, err := st.s.ListSubscriptions(ctx, "us-east-1"); err != nil {
				return err
			} else if len(subs) != 1 {
				t.Fatalf("subscriptions %v", subs)
			}
			if _, err := st.s.ListSubscriptionsByTopic(ctx, st.topic.ARN); err != nil {
				return err
			}
			if _, err := st.s.ListMessages(ctx, st.topic.ARN); err != nil {
				return err
			}
			_, err := st.s.DeliveryCount(st.sub.ARN)
			return err
		}},
		{"unsubscribe", true, func(ctx context.Context, st snsFaultState) error { return st.s.Unsubscribe(st.sub.ARN) }},
		{"delete topic", true, func(ctx context.Context, st snsFaultState) error { return st.s.DeleteTopic(st.topic.ARN) }},
	})
}
