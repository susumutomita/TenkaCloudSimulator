package blob

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

// AuthenticatedActor carries only the exercise identity already authenticated by
// the trusted host. It carries no credential role and cannot grant operator bypass.
type AuthenticatedActor struct{ ExerciseIdentity string }

// TrustedActorExtractor must read trusted in-process authentication context.
// Never implement it using request headers, query parameters, or Azure metadata.
type TrustedActorExtractor func(context.Context) (AuthenticatedActor, bool)

// NewOwnedHandler composes a synthetic account with an explicit local owner-only
// policy. This is not Azure RBAC, Entra, SharedKey, or SAS authentication. The host
// fixes account/environment/owner and supplies an authenticated exercise identity.
// Root adapters map provider.Actor.ExerciseIdentity, never credential role.
func NewOwnedHandler(db *sql.DB, cfg Config, extractor TrustedActorExtractor) (http.Handler, error) {
	if extractor == nil {
		return nil, fmt.Errorf("trusted Azure actor extractor is required")
	}
	authorize := func(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) {
		// New invokes this callback in the resource operation's transaction, after
		// reloading its actual persisted account owner. No nested transaction or
		// unscoped default allow is used here.
		if tx == nil || a.Actor.Subject != cfg.Owner || a.Actor.Environment != cfg.Environment ||
			a.Resource.Provider != "azure" || a.Resource.Environment != cfg.Environment ||
			a.Resource.Account != cfg.Account || a.Resource.Owner != cfg.Owner {
			return false, nil
		}
		var expected Permission
		switch a.Operation {
		case CreateContainer:
			expected = Permission{"Microsoft.Storage/storageAccounts/blobServices/containers/write", ActionPermission}
		case ListContainers, GetContainerProperties:
			expected = Permission{"Microsoft.Storage/storageAccounts/blobServices/containers/read", ActionPermission}
		case DeleteContainer:
			expected = Permission{"Microsoft.Storage/storageAccounts/blobServices/containers/delete", ActionPermission}
		case PutBlob:
			expected = Permission{"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/write", DataPermission}
		case GetBlob, GetBlobProperties, ListBlobs:
			expected = Permission{"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/read", DataPermission}
		case DeleteBlob:
			expected = Permission{"Microsoft.Storage/storageAccounts/blobServices/containers/blobs/delete", DataPermission}
		default:
			return false, nil
		}
		return a.Permission == expected, nil
	}
	service, err := New(db, cfg, authorize)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := extractor(r.Context())
		if !ok || !validLabel(actor.ExerciseIdentity, 256) {
			writeError(w, r, fail(403, "AuthorizationPermissionMismatch", "Trusted Azure actor binding is required."))
			return
		}
		// Overwrite any previous leaf actor. A trusted central extractor is the only
		// authority, including when an untrusted lower layer attempted to bind owner.
		service.ServeHTTP(w, r.WithContext(WithActor(r.Context(), Actor{Subject: actor.ExerciseIdentity, Environment: cfg.Environment})))
	}), nil
}
