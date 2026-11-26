//go:build linux && amd64

package server

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

var (
	errOwnedPostgresState   = errors.New("owned PostgreSQL requires an existing private operator-owned SQLite state file")
	ownedPostgresIdentifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

type ownedPostgresOperator struct {
	path      string
	stateFile os.FileInfo
	runtime   catalog.OwnedPrivatePostgresConfig
}

func localOwnedState(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 512 {
		return nil, errOwnedPostgresState
	}
	uid := uint32(os.Geteuid())
	ownedBy := func(info os.FileInfo) (uint32, bool) {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, false
		}
		return st.Uid, true
	}
	file, err := os.Lstat(path)
	if err != nil || !file.Mode().IsRegular() || file.Mode().Perm() != 0600 {
		return nil, errOwnedPostgresState
	}
	if st, ok := file.Sys().(*syscall.Stat_t); !ok || st.Uid != uid || st.Nlink != 1 {
		return nil, errOwnedPostgresState
	}
	directory := filepath.Dir(path)
	for part := directory; ; part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err != nil || !info.IsDir() {
			return nil, errOwnedPostgresState
		}
		owner, ok := ownedBy(info)
		if !ok || owner != uid && owner != 0 {
			return nil, errOwnedPostgresState
		}
		if part == directory {
			if owner != uid || info.Mode().Perm() != 0700 {
				return nil, errOwnedPostgresState
			}
		} else if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return nil, errOwnedPostgresState
		}
		if part == "/" {
			break
		}
	}
	return file, nil
}

func admitOwnedPostgresOperator(path string, settings OwnedPostgresOperatorConfig) (*ownedPostgresOperator, error) {
	if os.Geteuid() == 0 {
		return nil, errOwnedPostgresPlatform
	}
	name := settings.Identifier
	if !slices.Contains(aws.Regions, settings.Region) || !ownedPostgresIdentifier.MatchString(name) ||
		strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return nil, errOwnedPostgresConfig
	}
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, "../")
	}
	if within(settings.BundleDirectory, settings.WorkRoot) || within(settings.WorkRoot, settings.BundleDirectory) ||
		within(settings.BundleDirectory, path) {
		return nil, errOwnedPostgresConfig
	}
	state, err := localOwnedState(path)
	if err != nil {
		return nil, err
	}
	if err := rdsruntime.CheckIdleWorkRoot(settings.WorkRoot); err != nil {
		return nil, err
	}
	bundle, err := rdsruntime.OpenBundle(settings.BundleDirectory, settings.BundleSHA256)
	if err != nil {
		return nil, err
	}
	return &ownedPostgresOperator{path: path, stateFile: state, runtime: catalog.OwnedPrivatePostgresConfig{
		Region: settings.Region, Identifier: name, Bundle: bundle, WorkRoot: settings.WorkRoot,
	}}, nil
}

func (o *ownedPostgresOperator) checkState(path string) error {
	if path != o.path {
		return errOwnedPostgresState
	}
	now, err := localOwnedState(path)
	if err != nil || o.stateFile == nil || !os.SameFile(o.stateFile, now) {
		return errOwnedPostgresState
	}
	return nil
}

func (o *ownedPostgresOperator) open(db *sql.DB, config catalog.Config) (*catalog.Engine, func() error, error) {
	engine, owner, err := catalog.NewWithOwnedPrivatePostgres(db, config, o.runtime)
	if err != nil {
		return nil, nil, err
	}
	return engine, owner.Close, nil
}
