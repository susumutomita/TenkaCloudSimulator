//go:build linux && amd64

// Package rdsruntime owns a private PostgreSQL workload for trusted, explicit
// local composition. It does not grant or publish any client endpoint.
package rdsruntime

import (
	"crypto/sha256"
	"crypto/subtle"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	maxBundleFiles = 4096
	maxBundleBytes = 96 << 20
	bundleLabel    = "tcsim-pg15-bundle-v1\x00linux/amd64\n"
)

var (
	ErrBundle = errors.New("PostgreSQL bundle identity or layout not approved")
	ErrWork   = errors.New("PostgreSQL requires an owned private local work root")
	safePath  = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,500}$`)
	fixed     = [...]string{"bin/initdb", "bin/postgres", "bin/psql", "lib/libpq.so.5"}
)

// Bundle is an immutable, provider-side opt-in. The pin must come from the
// trusted deployment configuration, not from a resource request or the
// bundle being opened. Neither the path nor the pin grants a network route.
type Bundle struct {
	dir    string
	pin    [sha256.Size]byte
	root   os.FileInfo
	assets [len(fixed)]os.FileInfo
}

// OpenBundle verifies a separately supplied PostgreSQL 15.20 Linux/amd64
// installation. There is no downloaded/system-Postgres fallback.
func OpenBundle(directory, approvedSHA256 string) (*Bundle, error) {
	digest, err := hex.DecodeString(approvedSHA256)
	if err != nil || len(digest) != sha256.Size || approvedSHA256 != strings.ToLower(approvedSHA256) {
		return nil, ErrBundle
	}
	b := &Bundle{dir: directory}
	copy(b.pin[:], digest)
	b.root, b.assets, err = b.inspect()
	if err != nil {
		return nil, err
	}
	return b, nil
}

func ownedPath(directory string, private bool) (os.FileInfo, error) {
	if !safePath.MatchString(directory) || filepath.Clean(directory) != directory {
		return nil, ErrWork
	}
	var leaf os.FileInfo
	for part := directory; ; part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err != nil || !info.IsDir() || !trustedOwner(info) {
			return nil, ErrWork
		}
		mode := info.Mode()
		if mode.Perm()&0022 != 0 && (part == directory || mode&os.ModeSticky == 0) {
			return nil, ErrWork
		}
		if part == directory {
			if private && (mode.Perm() != 0700 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid()) {
				return nil, ErrWork
			}
			leaf = info
		}
		if part == "/" {
			return leaf, nil
		}
	}
}

func trustedOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || int(stat.Uid) == os.Geteuid())
}

// bundleDigest walks a bounded, platform-labeled install tree using a rooted
// file handle. Every relative name, type, permissions, link target and file
// content participates in the seal; a different path topology is not an alias.
func bundleDigest(root *os.Root) ([]byte, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, bundleLabel)
	total, entries := int64(0), 0
	err := fs.WalkDir(root.FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
		entries++
		if walkErr != nil || entries > maxBundleFiles || len(name) > 512 || strings.ContainsAny(name, "\r\n") {
			return ErrBundle
		}
		info, err := root.Lstat(name)
		if err != nil || !trustedOwner(info) || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			return ErrBundle
		}
		mode := info.Mode()
		if mode&os.ModeSymlink == 0 && mode.Perm()&0022 != 0 {
			return ErrBundle
		}
		var ty byte
		payload := ""
		switch {
		case info.IsDir():
			ty = 'd'
		case mode&os.ModeSymlink != 0:
			ty = 'l'
			payload, err = root.Readlink(name)
			if err != nil || payload == "." || payload == ".." || strings.ContainsAny(payload, "/\\\r\n") {
				return ErrBundle
			}
			target, err := root.Lstat(filepath.Join(filepath.Dir(name), payload))
			if err != nil || !target.Mode().IsRegular() || !trustedOwner(target) {
				return ErrBundle
			}
		case mode.IsRegular():
			ty = 'f'
			size := info.Size()
			if size < 0 || size > maxBundleBytes-total {
				return ErrBundle
			}
			total += size
			file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return ErrBundle
			}
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
				_ = file.Close()
				return ErrBundle
			}
			part := sha256.New()
			n, err := io.Copy(part, io.LimitReader(file, size+1))
			if closeErr := file.Close(); n != size || err != nil || closeErr != nil {
				return ErrBundle
			}
			payload = hex.EncodeToString(part.Sum(nil))
		default:
			return ErrBundle
		}
		_, _ = fmt.Fprintf(h, "%c\x00%s\x00%03o\x00%s\n", ty, name, mode.Perm(), payload)
		return nil
	})
	return h.Sum(nil), err
}

func (b *Bundle) inspect() (os.FileInfo, [len(fixed)]os.FileInfo, error) {
	var assets [len(fixed)]os.FileInfo
	rootInfo, err := ownedPath(b.dir, false)
	if err != nil {
		return nil, assets, ErrBundle
	}
	root, err := os.OpenRoot(b.dir)
	if err != nil {
		return nil, assets, ErrBundle
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, opened) {
		return nil, assets, ErrBundle
	}
	sum, err := bundleDigest(root)
	if err != nil || subtle.ConstantTimeCompare(sum, b.pin[:]) != 1 {
		return nil, assets, ErrBundle
	}
	for i, name := range fixed {
		info, err := root.Lstat(name)
		if err != nil || i < 3 && (!info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0) {
			return nil, assets, ErrBundle
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, assets, ErrBundle
		}
		actual, err := file.Stat()
		if err != nil || !actual.Mode().IsRegular() || i < 3 && !os.SameFile(info, actual) {
			_ = file.Close()
			return nil, assets, ErrBundle
		}
		check := checkedELF(file, b.dir, i == 3)
		if closeErr := file.Close(); check != nil || closeErr != nil {
			return nil, assets, ErrBundle
		}
		assets[i] = actual
	}
	return rootInfo, assets, nil
}

func checkedELF(file *os.File, directory string, sharedLibrary bool) error {
	f, err := elf.NewFile(file)
	if err != nil || f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 ||
		(f.Type != elf.ET_DYN && f.Type != elf.ET_EXEC) {
		return ErrBundle
	}
	interpreter := ""
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		if interpreter != "" || p.Filesz > 96 {
			return ErrBundle
		}
		value, err := io.ReadAll(p.Open())
		if err != nil {
			return ErrBundle
		}
		interpreter = string(value)
	}
	if sharedLibrary && interpreter != "" || !sharedLibrary && interpreter != "/lib64/ld-linux-x86-64.so.2\x00" {
		return ErrBundle
	}
	if rpath, err := f.DynString(elf.DT_RPATH); err != nil || len(rpath) != 0 {
		return ErrBundle
	}
	runpath, err := f.DynString(elf.DT_RUNPATH)
	if err != nil {
		return ErrBundle
	}
	for _, p := range runpath {
		if p != filepath.Join(directory, "lib") && p != "$ORIGIN/../lib" && !(sharedLibrary && p == "$ORIGIN") {
			return ErrBundle
		}
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return ErrBundle
	}
	for _, lib := range needed {
		if lib != "libc.so.6" && lib != "libm.so.6" && (sharedLibrary || lib != "libpq.so.5") {
			return ErrBundle
		}
	}
	return nil
}

func (b *Bundle) verify() error {
	if b == nil || b.root == nil {
		return ErrBundle
	}
	root, assets, err := b.inspect()
	if err != nil || !os.SameFile(root, b.root) {
		return ErrBundle
	}
	for i, info := range assets {
		if !os.SameFile(info, b.assets[i]) {
			return ErrBundle
		}
	}
	return nil
}

func (b *Bundle) executable(index int) (string, error) {
	root, err := ownedPath(b.dir, false)
	if err != nil || !os.SameFile(root, b.root) {
		return "", ErrBundle
	}
	path := filepath.Join(b.dir, fixed[index])
	info, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, b.assets[index]) || !info.Mode().IsRegular() ||
		!trustedOwner(info) || info.Mode() != b.assets[index].Mode() {
		return "", ErrBundle
	}
	return path, nil
}
