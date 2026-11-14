//go:build darwin || linux

package guesthvf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxImageBytes int64 = 4 << 30

// OpenIndependentImage checks a trusted operator's candidate copy, retaining the
// validated read-only descriptor on success. original must be an already-open,
// provenance-verified original. The caller owns both descriptor lifetimes.
// No file is modified, no process launched. This does not prove QCOW allocation
// correctness, prevent concurrent same-user mutation, or authorize a VM launch.
func OpenIndependentImage(path, digest string, original *os.File) (_ *os.File, err error) {
	pin, e := hex.DecodeString(digest)
	if e != nil || len(pin) != sha256.Size || hex.EncodeToString(pin) != digest || original == nil {
		return nil, ErrPlan
	}
	canonical, e := filepath.EvalSymlinks(path)
	if e != nil || !filepath.IsAbs(path) || canonical != path || filepath.Clean(path) != path {
		return nil, ErrPlan
	}
	parent, e := os.Stat(filepath.Dir(path))
	if e != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
		return nil, ErrPlan
	}
	ps, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || ps.Uid != uint32(os.Getuid()) {
		return nil, ErrPlan
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() {
		if err != nil {
			err = errors.Join(err, f.Close())
		}
	}()
	before, e := f.Stat()
	if e != nil {
		return nil, e
	}
	old, e := original.Stat()
	if e != nil {
		return nil, e
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || os.SameFile(before, old) || before.Size() < 512 || before.Size() > maxImageBytes {
		return nil, ErrPlan
	}
	if e := checkStandaloneHeader(f, before.Size()); e != nil {
		return nil, e
	}
	hash := sha256.New()
	n, e := io.Copy(hash, io.NewSectionReader(f, 0, before.Size()))
	if e != nil {
		return nil, e
	}
	if n != before.Size() || hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, ErrPlan
	}
	after, e := f.Stat()
	if e != nil {
		return nil, e
	}
	pathInfo, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || afterStat.Nlink != 1 || afterStat.Uid != stat.Uid || after.Mode() != before.Mode() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) || !os.SameFile(before, pathInfo) || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrPlan
	}
	return f, nil
}

// Strict admission subset of QEMU's documented QCOW2 header, not a disk repairer
// or full parser. This intentionally rejects otherwise valid advanced images.
func checkStandaloneHeader(r io.ReaderAt, size int64) error {
	var h [104]byte
	if _, e := r.ReadAt(h[:], 0); e != nil {
		return ErrPlan
	}
	u32 := func(offset int) uint32 { return binary.BigEndian.Uint32(h[offset : offset+4]) }
	u64 := func(offset int) uint64 { return binary.BigEndian.Uint64(h[offset : offset+8]) }
	if string(h[:4]) != "QFI\xfb" || u32(4) != 3 || u64(8) != 0 || u32(16) != 0 || u32(32) != 0 || u32(60) != 0 || u64(64) != 0 || u64(72) != 0 || u64(80) != 0 || u64(88) != 0 || u32(96) != 4 {
		return ErrPlan
	}
	headerLength := int64(u32(100))
	if headerLength != 104 && headerLength != 112 {
		return ErrPlan
	}
	if headerLength == 112 {
		var optional [8]byte
		if _, e := r.ReadAt(optional[:], 104); e != nil {
			return ErrPlan
		}
		// No compression feature: compression type and padding must be zero.
		if optional != [8]byte{} {
			return ErrPlan
		}
	}
	bits := u32(20)
	if bits < 9 || bits > 21 || u64(24) == 0 || u64(24) > uint64(maxImageBytes) {
		return ErrPlan
	}
	cluster := int64(1) << bits
	if size < cluster {
		return ErrPlan
	}
	// Only the display-only feature-name table is admitted. Reject backing-format,
	// external-data, encryption, bitmap and unknown extensions even with clear flags.
	seenFeatures := false
	for offset := headerLength; offset+8 <= cluster; {
		var ext [8]byte
		if _, e := r.ReadAt(ext[:], offset); e != nil {
			return ErrPlan
		}
		kind := binary.BigEndian.Uint32(ext[:4])
		length := int64(binary.BigEndian.Uint32(ext[4:]))
		if kind == 0 {
			if length != 0 {
				return ErrPlan
			}
			return nil
		}
		if kind != 0x6803f857 || seenFeatures || length%48 != 0 || length == 0 {
			return ErrPlan
		}
		seenFeatures = true
		next := offset + 8 + ((length+7)/8)*8
		if next > cluster {
			return ErrPlan
		}
		offset = next
	}
	return ErrPlan
}
