package main

import (
	"errors"
	"flag"

	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
)

// sakuraCLIFlags contains only parsed values. It opens no files/listeners and
// does not grant authority. The root run function must validate before startup.
type sakuraCLIFlags struct {
	enabled       bool
	scope         sakura.Config
	scopeSupplied bool
}

// registerSakuraFlags adds the opt-in flags to the caller's flag set. Explicit
// empty scope values count as supplied, so disabled mode cannot silently ignore
// a user's attempt to configure the provider. No environment variables are read.
func registerSakuraFlags(set *flag.FlagSet) (*sakuraCLIFlags, error) {
	if set == nil {
		return nil, errors.New("Sakura flag registration requires a flag set")
	}
	values := &sakuraCLIFlags{}
	set.BoolVar(&values.enabled, "enable-sakura-disks", false, "Enable Sakura Disk configuration metadata only (no VM or block storage)")
	for _, field := range []struct {
		name, help string
		target     *string
	}{
		{"sakura-environment", "Explicit per-team local Sakura environment", &values.scope.Environment},
		{"sakura-project", "Explicit local Sakura project label", &values.scope.Project},
		{"sakura-zone", "Explicit fixed local Sakura zone", &values.scope.Zone},
		{"sakura-owner", "Explicit exercise owner of local Sakura Disk metadata", &values.scope.Owner},
	} {
		set.Func(field.name, field.help, func(value string) error { *field.target = value; values.scopeSupplied = true; return nil })
	}
	return values, nil
}

// validateSakuraFlags is pure: it inspects a value copy and returns a separate
// scope copy. The caller owns authentication, state/token files and listener
// lifecycle. Passing this function is not proof that run calls it early enough.
func validateSakuraFlags(values sakuraCLIFlags, unsafeNoAuth bool) (*sakura.Config, error) {
	if !values.enabled {
		if values.scopeSupplied || values.scope != (sakura.Config{}) {
			return nil, errors.New("Sakura scope flags require --enable-sakura-disks")
		}
		return nil, nil
	}
	if unsafeNoAuth {
		return nil, errors.New("--enable-sakura-disks requires authenticated access; cannot combine with --unsafe-no-auth")
	}
	if err := sakura.ValidateConfig(values.scope); err != nil {
		return nil, err
	}
	scope := values.scope
	return &scope, nil
}
