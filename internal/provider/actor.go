package provider

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CredentialRole describes how the local host authenticated a request. It is not
// a provider data-plane permission or an authenticated cloud IAM role.
type CredentialRole string

const (
	CredentialOperator    CredentialRole = "operator"
	CredentialParticipant CredentialRole = "participant"
)

// Actor separates the authenticated local identity from the selected exercise
// identity. It contains no credential, token fingerprint or tenant assertion.
// A trusted provider adapter must separately bind its configured environment.
type Actor struct {
	CredentialIdentity string
	ExerciseIdentity   string
	CredentialRole     CredentialRole
}

type actorContextKey struct{}

func validActor(a Actor) bool {
	identity := func(value string) bool {
		return value != "" && utf8.ValidString(value) && len(value) <= 1024 && strings.TrimSpace(value) == value &&
			!strings.ContainsFunc(value, unicode.IsControl)
	}
	if !identity(a.CredentialIdentity) || !identity(a.ExerciseIdentity) {
		return false
	}
	switch a.CredentialRole {
	case CredentialOperator:
		return true
	case CredentialParticipant:
		return a.CredentialIdentity == a.ExerciseIdentity
	default:
		return false
	}
}

// WithActor is a trusted in-process adapter seam, never an HTTP authentication
// mechanism. The value is copied. Invalid input shadows any previous actor so a
// caller cannot accidentally retain an earlier authority after failed mapping.
// Like context.WithValue, it requires a non-nil parent context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorFromContext fails closed for missing, malformed or unsupported actors.
// It never reads headers, applies an administrator default or grants permissions.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	if ctx == nil {
		return Actor{}, false
	}
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok || !validActor(actor) {
		return Actor{}, false
	}
	return actor, true
}
