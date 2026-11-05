// Package sakura composes the bounded local IaaS Disk configuration provider.
// It allocates no disk device, runs no VM, and supplies no host authentication.
package sakura

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura/iaas"
)

const ProviderName = "sakura"

// Config is explicit trusted host configuration, not a request or credential.
// There are no defaults. Hosts allocate separate DBs and token authorities per
// team; equal owner strings do not establish isolation between environments.
type Config = iaas.Config

// ValidateConfig performs pure preflight so a host can reject invalid options
// before opening state, creating token files, or binding a listener. These local
// scope limits are not a claim about Sakura account/project naming constraints.
func ValidateConfig(c Config) error {
	for _, field := range []struct{ name, value string }{{"environment", c.Environment}, {"project", c.Project}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) < 1 || utf8.RuneCountInString(field.value) > 128 || strings.TrimSpace(field.value) != field.value || strings.ContainsFunc(field.value, unicode.IsControl) {
			return fmt.Errorf("%w: explicit local %s is required", iaas.ErrInvalid, field.name)
		}
	}
	// Match local identity syntax. The reserved operator identity may be an
	// explicitly configured owner, but is never supplied as a default.
	if len(c.Owner) < 1 || len(c.Owner) > 64 {
		return fmt.Errorf("%w: owner must contain 1-64 ASCII letters, digits, underscores or hyphens", iaas.ErrInvalid)
	}
	for _, r := range c.Owner {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("%w: invalid local owner syntax", iaas.ErrInvalid)
		}
	}
	if len(c.Zone) < 1 || len(c.Zone) > 32 {
		return fmt.Errorf("%w: explicit literal local zone is required", iaas.ErrInvalid)
	}
	for i, r := range c.Zone {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && r == '-') {
			return fmt.Errorf("%w: invalid local zone syntax", iaas.ErrInvalid)
		}
	}
	return nil
}

// NewProvider borrows db and never closes it, on either success or failure.
// The caller owns provider registration, listener lifecycle and NewAccess (or an
// equivalent trusted authenticator). Without its typed Actor, every leaf request
// is denied: Basic routing markers, headers and operator status grant no access.
func NewProvider(db *sql.DB, c Config) (http.Handler, error) {
	if err := ValidateConfig(c); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, iaas.ErrInvalid
	}
	store, err := iaas.NewDiskStore(db, c)
	if err != nil {
		return nil, err
	}
	return iaas.NewDiskHandler(store, c, func(ctx context.Context) (iaas.Actor, bool) {
		actor, ok := provider.ActorFromContext(ctx)
		if !ok {
			return iaas.Actor{}, false
		}
		// Realm labels come only from copied host configuration. Common Actor carries
		// no environment/project/zone or issuer identity; do not infer those from a
		// request, or claim this mapping alone authenticates a different team host.
		return iaas.Actor{Subject: actor.ExerciseIdentity, Environment: c.Environment, Project: c.Project, Zone: c.Zone}, true
	})
}
