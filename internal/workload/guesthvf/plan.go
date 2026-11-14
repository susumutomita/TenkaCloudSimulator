// Package guesthvf admits fixed ARM HVF invocation data for a future trusted
// supervisor. It never launches processes and is not a containment guarantee.
package guesthvf

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrPlan = errors.New("unqualified HVF launch plan")
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Files are paths supplied by the trusted asset owner, never participant input.
// Before any execution, a supervisor must pin provenance and file identity,
// verify a private independent writable image, and retain ownership until reap.
// This type deliberately does not represent evidence of those checks.
type Files struct{ Executable, Firmware, WritableImage string }

type Plan struct {
	executable string
	args       []string
}

// Admit builds only the paused, disconnected initial VM. There are no extra
// arguments, environment inheritance, shell commands, shared folders, implicit
// snapshot directories, network backends, or participant-selected host paths.
// FD 3 must later be a supervisor-owned QMP socket; this function does not open it.
func Admit(goos, goarch string, f Files, generation string) (Plan, error) {
	if goos != "darwin" || goarch != "arm64" || !uuid.MatchString(generation) || generation == "00000000-0000-0000-0000-000000000000" {
		return Plan{}, ErrPlan
	}
	paths := []string{f.Executable, f.Firmware, f.WritableImage}
	seen := map[string]bool{}
	for _, p := range paths {
		// QEMU option strings use commas as separators. Reject rather than attempting
		// ambiguous escaping. Canonical/inode checks belong to the asset owner.
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, ",\x00\r\n") || seen[p] {
			return Plan{}, ErrPlan
		}
		seen[p] = true
	}
	return Plan{f.Executable, []string{
		"-nodefaults", "-no-user-config", "-machine", "virt,accel=hvf", "-cpu", "host",
		"-m", "512", "-smp", "1", "-S", "-display", "none", "-monitor", "none",
		"-serial", "none", "-nic", "none", "-no-reboot", "-uuid", generation,
		"-bios", f.Firmware,
		"-drive", "if=none,id=root,format=qcow2,file=" + f.WritableImage,
		"-device", "virtio-blk-pci,drive=root",
		"-chardev", "socket,id=control,fd=3", "-mon", "chardev=control,mode=control",
	}}, nil
}

// Invocation returns fresh data. An admitted plan is NOT permission to launch:
// the production runtime remains unsupported until ownership and isolation are
// implemented and independently qualified. Empty Env is intentional.
func (p Plan) Invocation() (executable string, args, env []string, err error) {
	if p.executable == "" {
		return "", nil, nil, ErrPlan
	}
	return p.executable, append([]string(nil), p.args...), []string{}, nil
}
