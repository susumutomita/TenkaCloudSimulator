// Package guestbundle materializes pinned embedded VM assets in private storage.
// It does not execute a helper, start a VM, provide a lease or grant guest authority.
package guestbundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const MaxFiles = 4096
const MaxBytes int64 = 4 << 30
const MaxArchiveBytes int64 = 4 << 30

// File describes a regular file. Symlinks, devices, sparse entries and arbitrary
// archive permissions are never accepted. Executables become owner-read/execute;
// other files become owner-read-only. All directories remain owner-only.
type File struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable,omitempty"`
}

// Manifest must come from trusted build-time metadata, never participant input.
// The archive digest pins compressed bytes as well as the per-file contents.
type Manifest struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	ArchiveSHA256 string `json:"archiveSha256"`
	Files         []File `json:"files"`
}

// Bundle has an exclusively created directory and no reuse/cache behavior. The
// lifecycle owner must retain it until every helper/guest using it is stopped.
// Close removes these files only; it never proves a guest has been contained.
type Bundle struct {
	directory string
	files     map[string]File
}

func digestOK(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}
func validPath(s string) bool {
	return fs.ValidPath(s) && s != "." && !strings.ContainsAny(s, "\\:\x00")
}
func validate(m Manifest) (map[string]File, error) {
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		return nil, errors.New("guest bundle host platform mismatch")
	}
	if !digestOK(m.ArchiveSHA256) || len(m.Files) == 0 || len(m.Files) > MaxFiles {
		return nil, errors.New("invalid guest bundle manifest")
	}
	files := make(map[string]File, len(m.Files))
	var total int64
	for _, f := range m.Files {
		if !validPath(f.Path) || f.Size < 0 || f.Size > MaxBytes || total > MaxBytes-f.Size || !digestOK(f.SHA256) {
			return nil, errors.New("invalid guest bundle file")
		}
		if _, exists := files[f.Path]; exists {
			return nil, errors.New("duplicate guest bundle file")
		}
		files[f.Path] = f
		total += f.Size
	}
	for name := range files {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if _, exists := files[dir]; exists {
				return nil, errors.New("guest bundle file/directory conflict")
			}
		}
	}
	return files, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// Extract consumes a finite trusted embedded archive. parent is a trusted host
// storage location, not a guest-supplied path. It creates a fresh mode-0700
// directory, validates every byte and removes partial output on any failure.
// No executable path is returned before both archive and file digests match.
// Callers must supply a nonblocking local asset reader; cancellation cannot
// interrupt a malicious Reader blocked inside Read.
func Extract(ctx context.Context, parent string, archive io.Reader, m Manifest) (_ *Bundle, err error) {
	if ctx == nil || archive == nil {
		return nil, errors.New("guest bundle context and archive required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := validate(m)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(parent, "guest-assets-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(directory))
		}
	}()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	compressedHash := sha256.New()
	limited := &io.LimitedReader{R: contextReader{ctx, archive}, N: MaxArchiveBytes + 1}
	compressed := io.TeeReader(limited, compressedHash)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	for {
		h, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}
		f, exists := files[h.Name]
		if !exists || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size != f.Size || len(h.PAXRecords) != 0 || h.Linkname != "" {
			return nil, errors.New("unexpected guest bundle archive entry")
		}
		if err = root.MkdirAll(path.Dir(h.Name), 0700); err != nil {
			return nil, err
		}
		out, openErr := root.OpenFile(h.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return nil, openErr
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, hash), contextReader{ctx, tr})
		if copyErr == nil && (n != f.Size || hex.EncodeToString(hash.Sum(nil)) != f.SHA256) {
			copyErr = errors.New("guest bundle file integrity mismatch")
		}
		mode := fs.FileMode(0400)
		if f.Executable {
			mode = 0500
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		if copyErr == nil {
			copyErr = out.Chmod(mode)
		}
		closeErr := out.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			return nil, err
		}
		seen[h.Name] = true
	}
	// Consume the gzip trailer and all compressed input. Additional decompressed
	// bytes are forbidden, including concatenated archives hidden after tar EOF.
	var extra [1]byte
	n, tailErr := gz.Read(extra[:])
	if n != 0 || tailErr != io.EOF {
		return nil, errors.New("trailing or corrupt guest bundle data")
	}
	if _, err = io.Copy(io.Discard, compressed); err != nil {
		return nil, err
	}
	if limited.N == 0 || hex.EncodeToString(compressedHash.Sum(nil)) != m.ArchiveSHA256 || len(seen) != len(files) {
		return nil, errors.New("guest bundle archive integrity mismatch")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &Bundle{directory: directory, files: files}, nil
}

// Path returns only a manifest-listed asset. This API is for trusted runtime
// owners and must not be exposed to participants or used as lease authority.
func (b *Bundle) Path(name string) (string, error) {
	if b == nil || b.directory == "" {
		return "", errors.New("guest bundle is closed")
	}
	if _, ok := b.files[name]; !ok {
		return "", fmt.Errorf("unknown guest asset %q", name)
	}
	return filepath.Join(b.directory, filepath.FromSlash(name)), nil
}
func (b *Bundle) Close() error {
	if b == nil || b.directory == "" {
		return nil
	}
	if err := os.RemoveAll(b.directory); err != nil {
		return err
	}
	b.directory = ""
	return nil
}

// Info returns a copy of pinned metadata for a listed asset. Trusted runtime
// composition uses this when rechecking assets while copying per-guest state.
// It does not grant execution authority or extend the bundle's lifetime.
func (b *Bundle) Info(name string) (File, error) {
	if b == nil || b.directory == "" {
		return File{}, errors.New("guest bundle is closed")
	}
	f, ok := b.files[name]
	if !ok {
		return File{}, fmt.Errorf("unknown guest asset %q", name)
	}
	return f, nil
}
