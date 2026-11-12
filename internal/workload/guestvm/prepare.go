// Package guestvm prepares private VM deployment files and a fixed launch plan.
// Preparation never starts a process or opens a socket. The explicit Launch
// adapter can start its fixed trusted process; no durable Backend is provided.
package guestvm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
)

const MaxDiskBytes int64 = 4 << 30
const MaxVirtualDiskBytes uint64 = 32 << 30

var fencePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Layout is operator-reviewed build-time metadata, never participant input.
// All three assets must be regular files from one verified bundle. The emulator
// must be executable and independently qualified as runnable on the target host.
type Layout struct{ Emulator, Firmware, BaseDisk string }

// Record is a preparation receipt, not evidence that a VM exists or is stopped.
// A lifecycle backend must durably bind this exact record to its acquire intent
// before any launch, fence all effects and persist actual process identity.
type Record struct {
	SessionImageSHA256 string            `json:"sessionImageSha256,omitempty"`
	Version            int               `json:"version"`
	Intent             guestlease.Intent `json:"intent"`
	Lease              string            `json:"lease"`
	UUID               string            `json:"uuid"`
	BaseSHA256         string            `json:"baseSha256"`
	SeedSHA256         string            `json:"seedSha256"`
}

type Prepared struct {
	network                       *guestseed.IPv4Network
	record                        Record
	directory, emulator, firmware string
}

func (p *Prepared) Record() Record    { return p.record }
func (p *Prepared) Directory() string { return p.directory }
func (p *Prepared) QMPSocket() string { return filepath.Join(p.directory, "qmp.sock") }
func (p *Prepared) QGASocket() string { return filepath.Join(p.directory, "qga.sock") }

// Invocation describes a fixed, paused, network-disabled Linux/amd64 TCG VM.
// Arguments are not participant-extensible. Starting it is NOT authorized by
// preparation: a durable lifecycle owner must still bind/fence the launch and
// prove its containment, process identity and exit. No environment is inherited
// by this plan; a future executor must not substitute the host environment.
type Invocation struct {
	Executable string
	Args       []string
	Env        []string
}

// Invocation does not execute anything. -S ensures guest CPUs cannot run before
// durable startup/UUID verification. This increment supplies no resume operation.
func (p *Prepared) Invocation() Invocation {
	root, _ := json.Marshal(map[string]any{"driver": "qcow2", "node-name": "root", "backing": nil, "file": map[string]any{"driver": "file", "filename": filepath.Join(p.directory, "root.qcow2")}})
	seed, _ := json.Marshal(map[string]any{"driver": "raw", "node-name": "seed", "read-only": true, "file": map[string]any{"driver": "file", "filename": filepath.Join(p.directory, "seed.iso")}})
	inv := Invocation{Executable: p.emulator, Env: []string{}, Args: []string{
		"-no-user-config", "-nodefaults", "-machine", "pc,accel=tcg", "-cpu", "max", "-m", "1024", "-smp", "1", "-S",
		"-uuid", p.record.UUID, "-L", filepath.Dir(p.firmware), "-bios", p.firmware, "-display", "none", "-vga", "none", "-monitor", "none", "-serial", "none", "-nic", "none", "-no-reboot",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-blockdev", string(root), "-device", "virtio-blk-pci,drive=root",
		"-blockdev", string(seed), "-device", "ide-cd,drive=seed",
		"-qmp", "unix:" + p.QMPSocket() + ",server=on,wait=off",
		"-device", "virtio-serial-pci", "-chardev", "socket,id=qga,path=" + p.QGASocket() + ",server=on,wait=off",
		"-device", "virtserialport,chardev=qga,name=org.qemu.guest_agent.0",
	}}
	if p.record.SessionImageSHA256 != "" {
		inv.Args = append(inv.Args, "-chardev", "socket,id=session,path="+p.SessionSocket()+",server=on,wait=off", "-device", "virtserialport,chardev=session,name=org.tenkacloud.session.0")
	}
	return inv
}

// Prepare materializes one independent writable base-image copy, a read-only
// NoCloud seed and a preparation receipt. Only already-persisted acquire intents
// may reach this function. It does not deduplicate intents: the durable backend
// must call it at most once and retain its immutable mapping across restart.
// parent and bundle lifetime belong to that trusted owner. No cleanup API is
// provided because file removal after a possible launch requires proven exit.
func Prepare(ctx context.Context, parent string, bundle *guestbundle.Bundle, layout Layout, intent guestlease.Intent, userData []byte) (result *Prepared, err error) {
	return prepare(ctx, parent, bundle, layout, intent, userData, nil)
}

func prepare(ctx context.Context, parent string, bundle *guestbundle.Bundle, layout Layout, intent guestlease.Intent, userData []byte, network *guestseed.IPv4Network, sessionImage ...string) (result *Prepared, err error) {
	var sessionPin string
	if len(sessionImage) > 1 {
		return nil, errors.New("one session image pin required")
	}
	if len(sessionImage) == 1 {
		sessionPin = sessionImage[0]
		if err := ValidateSessionImage(bundle, layout, sessionPin); err != nil {
			return nil, err
		}
		if network == nil {
			return nil, errors.New("session channel requires explicit network preparation")
		}
	}
	if ctx == nil {
		return nil, errors.New("guest preparation context required")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, errors.New("guest launch plan supports Linux/amd64 only")
	}
	if !fencePattern.MatchString(intent.Fence) || intent.Operation != 1 || intent.Action != guestlease.Acquire || intent.Lease != "" {
		return nil, errors.New("guest preparation requires persisted acquire intent")
	}
	if err = checkPrivateDirectory(parent); err != nil {
		return nil, err
	}
	emulator, err := verifyAsset(ctx, bundle, layout.Emulator, true, nil)
	if err != nil {
		return nil, err
	}
	firmware, err := verifyAsset(ctx, bundle, layout.Firmware, false, nil)
	if err != nil {
		return nil, err
	}
	if layout.Emulator == layout.Firmware || layout.Emulator == layout.BaseDisk || layout.Firmware == layout.BaseDisk {
		return nil, errors.New("guest asset roles must be distinct")
	}
	info, err := bundle.Info(layout.BaseDisk)
	if err != nil {
		return nil, err
	}
	if info.Executable || info.Size < 72 || info.Size > MaxDiskBytes {
		return nil, errors.New("invalid guest base image")
	}
	var data []byte
	if network == nil {
		data, err = guestseed.Build("vm-"+intent.Fence, "guest-"+intent.Fence[:12], userData)
	} else {
		data, err = guestseed.BuildWithNetwork("vm-"+intent.Fence, "guest-"+intent.Fence[:12], userData, *network)
	}
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(parent, "guest-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(directory))
		}
	}()
	if !validPrivateEndpoints(directory, sessionPin != "") {
		return nil, errors.New("private guest control path exceeds Unix socket limit")
	}
	disk, err := os.OpenFile(filepath.Join(directory, "root.qcow2"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, copyErr := verifyAsset(ctx, bundle, layout.BaseDisk, false, disk)
	if copyErr == nil {
		copyErr = validateDisk(disk, info.Size)
	}
	if copyErr == nil {
		copyErr = disk.Sync()
	}
	closeErr := disk.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}
	if err = writePrivate(filepath.Join(directory, "seed.iso"), data, 0400); err != nil {
		return nil, err
	}
	var uuid [16]byte
	if _, err = rand.Read(uuid[:]); err != nil {
		return nil, err
	}
	uuid[6] = (uuid[6] & 15) | 64
	uuid[8] = (uuid[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
	hash := sha256.Sum256(data)
	record := Record{SessionImageSHA256: sessionPin, Version: 1, Intent: intent, Lease: "vm-" + intent.Fence, UUID: id, BaseSHA256: info.SHA256, SeedSHA256: hex.EncodeToString(hash[:])}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if err = writePrivate(filepath.Join(directory, "prepared.json"), raw, 0400); err != nil {
		return nil, err
	}
	if err = syncDirectory(directory); err != nil {
		return nil, err
	}
	if err = syncDirectory(parent); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var captured *guestseed.IPv4Network
	if network != nil {
		copyNetwork := *network
		captured = &copyNetwork
	}
	return &Prepared{record: record, directory: directory, emulator: emulator, firmware: firmware, network: captured}, nil
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
func verifyAsset(ctx context.Context, bundle *guestbundle.Bundle, name string, executable bool, out io.Writer) (string, error) {
	info, err := bundle.Info(name)
	if err != nil {
		return "", err
	}
	if info.Executable != executable {
		return "", errors.New("invalid guest asset executable role")
	}
	path, err := bundle.Path(name)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, ",:\x00\r\n") {
		return "", errors.New("unsafe guest asset path")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() != info.Size {
		return "", errors.New("guest asset changed after extraction")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writer := io.Writer(hash)
	if out != nil {
		writer = io.MultiWriter(out, hash)
	}
	n, copyErr := io.Copy(writer, io.LimitReader(contextReader{ctx, file}, info.Size+1))
	closeErr := file.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	if n != info.Size || hex.EncodeToString(hash.Sum(nil)) != info.SHA256 {
		return "", errors.New("guest asset integrity mismatch during preparation")
	}
	return path, nil
}
func validateDisk(file *os.File, size int64) error {
	var h [104]byte
	n, err := file.ReadAt(h[:], 0)
	if err != nil && err != io.EOF {
		return err
	}
	if n < 72 || string(h[:4]) != "QFI\xfb" {
		return errors.New("guest image must be standalone QCOW2")
	}
	version := binary.BigEndian.Uint32(h[4:8])
	if version != 2 && version != 3 {
		return errors.New("unsupported QCOW2 version")
	}
	if binary.BigEndian.Uint64(h[8:16]) != 0 || binary.BigEndian.Uint32(h[16:20]) != 0 || binary.BigEndian.Uint32(h[32:36]) != 0 || binary.BigEndian.Uint32(h[60:64]) != 0 || binary.BigEndian.Uint64(h[64:72]) != 0 {
		return errors.New("QCOW2 backing files, encryption and snapshots are unsupported")
	}
	bits := binary.BigEndian.Uint32(h[20:24])
	virtual := binary.BigEndian.Uint64(h[24:32])
	if bits < 9 || bits > 21 || virtual == 0 || virtual > MaxVirtualDiskBytes {
		return errors.New("QCOW2 dimensions exceed bounds")
	}
	if version == 3 && (n < 104 || binary.BigEndian.Uint64(h[72:80]) != 0 || binary.BigEndian.Uint32(h[100:104]) != 104) {
		return errors.New("QCOW2 extensions/features unsupported")
	}
	// This checks only the deployment boundary, not all QCOW2 allocation tables.
	// Actual QEMU image opening/boot must be separately tested on trusted assets.
	if size < int64(1<<bits) {
		return errors.New("truncated QCOW2 header cluster")
	}
	return nil
}
func writePrivate(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
