// Package blob implements a bounded Azure Blob REST leaf. It is not registered
// in the simulator until trusted host authentication and authorization are wired.
package blob

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const APIVersion = "2023-11-03"
const MaxBlobBytes = 8 << 20
const MaxStoredBytes = 64 << 20
const MaxContainers = 1000
const MaxBlobs = 10000
const MaxMetadataBytes = 8 << 10

type Actor struct{ Subject, Environment string }
type actorKey struct{}

// WithActor is a trusted Go composition port, never an HTTP credential parser.
// Root adapters must map the authenticated exercise identity, not an operator
// role shortcut or a user-supplied provider/environment header.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}
func ActorFromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok && validLabel(a.Subject, 256) && validLabel(a.Environment, 128)
}

type Config struct{ Account, Owner, Environment string }
type Operation string

const (
	CreateContainer        Operation = "CreateContainer"
	ListContainers         Operation = "ListContainers"
	GetContainerProperties Operation = "GetContainerProperties"
	DeleteContainer        Operation = "DeleteContainer"
	PutBlob                Operation = "PutBlob"
	GetBlob                Operation = "GetBlob"
	GetBlobProperties      Operation = "GetBlobProperties"
	ListBlobs              Operation = "ListBlobs"
	DeleteBlob             Operation = "DeleteBlob"
)

type Resource struct{ Provider, Environment, Account, Container, Blob, Owner string }

// PermissionKind distinguishes the two Azure role-definition collections.
// These identifiers describe the requested permission; no RBAC engine or
// built-in-role/operator grant is implied by carrying this metadata.
type PermissionKind string

const (
	ActionPermission PermissionKind = "Action"
	DataPermission   PermissionKind = "DataAction"
)

type Permission struct {
	Name string
	Kind PermissionKind
}
type Authorization struct {
	Actor      Actor
	Operation  Operation
	Permission Permission
	Resource   Resource
}

// Authorizer runs in the operation's transaction. Read policy via tx; do not
// start another transaction, wait for a guest or grant authority from headers.
// Missing actors always fail before this callback, even if it would return true.
type Authorizer func(context.Context, *sql.Tx, Authorization) (bool, error)

func permissionFor(op Operation) Permission {
	const base = "Microsoft.Storage/storageAccounts/blobServices/containers/"
	switch op {
	case CreateContainer:
		return Permission{base + "write", ActionPermission}
	case DeleteContainer:
		return Permission{base + "delete", ActionPermission}
	case ListContainers, GetContainerProperties:
		return Permission{base + "read", ActionPermission}
	case PutBlob:
		return Permission{base + "blobs/write", DataPermission}
	case DeleteBlob:
		return Permission{base + "blobs/delete", DataPermission}
	case GetBlob, GetBlobProperties, ListBlobs:
		return Permission{base + "blobs/read", DataPermission}
	default:
		return Permission{}
	}
}

type failure struct {
	Status        int
	Code, Message string
}

func (e *failure) Error() string              { return e.Message }
func fail(status int, code, msg string) error { return &failure{status, code, msg} }

var errInternal = errors.New("Azure Blob storage operation failed")
var storedTag = regexp.MustCompile(`^"0x[0-9a-f]{32}"$`)
var accountName = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
var containerName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,61})[a-z0-9]$`)
var metadataName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validLabel(s string, max int) bool {
	if strings.TrimSpace(s) == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
func validContainer(s string) bool { return containerName.MatchString(s) && !strings.Contains(s, "--") }
func validBlob(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) < 1 || utf8.RuneCountInString(s) > 1024 || strings.Count(s, "/") >= 254 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r >= 0x7f && r <= 0x9f || r == 0xfffe || r == 0xffff {
			return false
		}
	}
	return true
}

type condition struct{ Match, None string }

func (c condition) check(tag string, exists, read bool) error {
	if c.Match != "" && (!exists || c.Match != "*" && c.Match != tag) {
		return fail(412, "ConditionNotMet", "The condition was not met.")
	}
	if c.None != "" && exists && (c.None == "*" || c.None == tag) {
		if read {
			return fail(304, "NotModified", "")
		}
		return fail(412, "ConditionNotMet", "The condition was not met.")
	}
	return nil
}

type entity struct {
	Name, ETag, Modified, ContentType, MD5 string
	Metadata                               map[string]string
	Body                                   []byte
	Length                                 int64
}
type listOptions struct {
	Prefix, Marker string
	Limit          int
	Metadata       bool
}
type page struct {
	Items []entity
	Next  string
}
