package provider

import (
	"context"
	"strings"
	"testing"
)

func TestActorContextRejectsMissingInvalidAndImpersonation(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background(), context.WithValue(context.Background(), "actor", Actor{"host", "reader", CredentialOperator})} {
		if actor, ok := ActorFromContext(ctx); ok || actor != (Actor{}) {
			t.Fatal("missing/private-key actor accepted", actor, ok)
		}
	}
	valid := WithActor(context.Background(), Actor{"host", "reader", CredentialOperator})
	for _, bad := range []Actor{
		{}, {"host", "reader", "admin"}, {"host", "reader", ""},
		{"", "reader", CredentialOperator}, {"host", "", CredentialOperator},
		{" host", "reader", CredentialOperator}, {"host", "reader\n", CredentialOperator},
		{"host\x00", "reader", CredentialOperator}, {"host", string([]byte{0xff}), CredentialOperator}, {"host", strings.Repeat("x", 1025), CredentialOperator},
		{"reader", "other", CredentialParticipant},
	} {
		if actor, ok := ActorFromContext(WithActor(valid, bad)); ok || actor != (Actor{}) {
			t.Fatalf("invalid mapping retained prior authority: %#v => %#v %v", bad, actor, ok)
		}
	}
}

func TestActorContextCopiesAndSeparatesCredentialFromExerciseIdentity(t *testing.T) {
	parent := context.WithValue(context.Background(), "request-marker", "kept")
	for _, input := range []Actor{{"host", "reader", CredentialOperator}, {"reader", "reader", CredentialParticipant}, {"svc:local/user", "azure:reader", CredentialOperator}} {
		original := input
		ctx := WithActor(parent, input)
		input.CredentialRole = CredentialParticipant
		input.ExerciseIdentity = "other"
		got, ok := ActorFromContext(ctx)
		if !ok || got != original || ctx.Value("request-marker") != "kept" {
			t.Fatal("context lost identity, copy or parent", got, original, ok)
		}
		got.CredentialIdentity = "mutated"
		if again, _ := ActorFromContext(ctx); again != original {
			t.Fatal("reader mutated stored authority", again)
		}
	}
}
