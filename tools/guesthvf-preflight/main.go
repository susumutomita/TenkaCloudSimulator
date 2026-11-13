// guesthvf-preflight is a read/copy-only development check of one pinned official
// Debian ARM asset. It never launches QEMU, launchctl or the Battle runtime.
package main

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesthvf"
)

const originalSize int64 = 338231296
const originalSHA512 = "4a166ab9a73f27857896b799dff913199ea982e2379889340e60734ba43bea83840c1317f6257a9a8ca466b2293862181a597edc5cdb5d19d4cb704a85b4130a"

type result struct {
	Result      string `json:"result"`
	CopySHA256  string `json:"copySha256"`
	CopyRemoved bool   `json:"copyRemoved"`
	BattleReady bool   `json:"battleReady"`
}

func verify(originalPath, parent string) (result, error) {
	return verifyPinned(originalPath, parent, originalSize, originalSHA512)
}

// The CLI always uses the fixed official pin above. Parameters here permit
// small synthetic regression fixtures, not operator-selected qualification.
func verifyPinned(originalPath, parent string, expectedSize int64, expectedSHA512 string) (_ result, err error) {
	if originalPath == "" || parent == "" {
		return result{}, errors.New("original and scratch are required")
	}
	canonical, e := filepath.EvalSymlinks(originalPath)
	if e != nil || canonical != originalPath {
		return result{}, errors.New("original must be canonical and not symlinked")
	}
	original, e := os.Open(originalPath)
	if e != nil {
		return result{}, e
	}
	defer func() { err = errors.Join(err, original.Close()) }()
	info, e := original.Stat()
	if e != nil {
		return result{}, e
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return result{}, errors.New("unexpected original type/size")
	}
	directory, e := os.MkdirTemp(parent, "hvf-asset-check-")
	if e != nil {
		return result{}, e
	}
	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			err = errors.Join(err, os.RemoveAll(directory))
		}
	}()
	canonicalDirectory, e := filepath.EvalSymlinks(directory)
	if e != nil {
		return result{}, e
	}
	directory = canonicalDirectory
	if e = os.Chmod(directory, 0700); e != nil {
		return result{}, e
	}
	path := filepath.Join(directory, "candidate.qcow2")
	copyFile, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return result{}, e
	}
	sha256Hash, sha512Hash := sha256.New(), sha512.New()
	n, copyErr := io.Copy(io.MultiWriter(copyFile, sha256Hash, sha512Hash), io.LimitReader(original, expectedSize+1))
	closeErr := copyFile.Close()
	if e = errors.Join(copyErr, closeErr); e != nil {
		return result{}, e
	}
	if n != expectedSize || hex.EncodeToString(sha512Hash.Sum(nil)) != expectedSHA512 {
		return result{}, errors.New("official original pin mismatch")
	}
	digest := hex.EncodeToString(sha256Hash.Sum(nil))
	fd, e := guesthvf.OpenIndependentImage(path, digest, original)
	if e != nil {
		return result{}, e
	}
	if e = fd.Close(); e != nil {
		return result{}, e
	}
	if e = os.RemoveAll(directory); e != nil {
		return result{}, e
	}
	cleanupNeeded = false
	if _, e = os.Lstat(directory); !os.IsNotExist(e) {
		return result{}, errors.New("test copy cleanup not confirmed")
	}
	return result{Result: "pinned-arm-image-preflight-pass", CopySHA256: digest, CopyRemoved: true, BattleReady: false}, nil
}
func main() {
	original := flag.String("original", "", "canonical pinned original image")
	scratch := flag.String("scratch", "", "operator-owned scratch parent")
	flag.Parse()
	r, e := verify(*original, *scratch)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if e = json.NewEncoder(os.Stdout).Encode(r); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
