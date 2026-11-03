// Package python executes real CPython bytecode in a bounded WASI sandbox.
// It has no cloud, identity, scoring or host-network knowledge.
package python

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed assets/python.wasm.gz assets/stdlib.zip bootstrap.py
var assets embed.FS

const MaxCodeBytes = 256 << 10
const MaxExpandedBytes = 1 << 20
const MaxOutputBytes = 256 << 10

var compilationCache = wazero.NewCompilationCache()
var handlerName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)

type Runtime struct {
	runtime  wazero.Runtime
	module   wazero.CompiledModule
	stdlib   fs.FS
	slots    chan struct{}
	lifetime context.Context
	cancel   context.CancelFunc
	active   sync.RWMutex
}

type Invocation struct {
	Code        []byte
	Handler     string
	Event       json.RawMessage
	Environment map[string]string
	Context     map[string]string
	Timeout     time.Duration
}

type Result struct {
	Payload       json.RawMessage
	FunctionError bool
	Logs          string
}

func ValidateCode(code []byte, handler string) (*zip.Reader, error) {
	if len(code) == 0 || len(code) > MaxCodeBytes || !handlerName.MatchString(handler) {
		return nil, errors.New("Use a Python module.function handler and a ZIP of at most 256 KiB")
	}
	z, err := zip.NewReader(bytes.NewReader(code), int64(len(code)))
	if err != nil {
		return nil, errors.New("Invalid function ZIP")
	}
	seen := map[string]bool{}
	var expanded uint64
	for _, file := range z.File {
		name := strings.TrimSuffix(file.Name, "/")
		_, duplicate := seen[name]
		if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00") || duplicate || file.Mode()&fs.ModeSymlink != 0 || !file.FileInfo().IsDir() && (!file.Mode().IsRegular() || !strings.HasSuffix(name, ".py")) {
			return nil, errors.New("ZIP may contain only regular Python sources and directories with safe unique paths")
		}
		seen[name] = !file.FileInfo().IsDir()
		if len(seen) > 64 || file.UncompressedSize64 > MaxExpandedBytes-expanded {
			return nil, errors.New("Function ZIP exceeds 64 entries or 1 MiB expanded")
		}
		expanded += file.UncompressedSize64
		if !file.FileInfo().IsDir() {
			r, e := file.Open()
			if e != nil {
				return nil, e
			}
			_, e = io.Copy(io.Discard, io.LimitReader(r, int64(MaxExpandedBytes)+1))
			_ = r.Close()
			if e != nil {
				return nil, e
			}
		}
	}
	parts := strings.Split(handler, ".")
	path := strings.Join(parts[:len(parts)-1], "/") + ".py"
	if !seen[path] {
		return nil, errors.New("Handler module is missing from the ZIP")
	}
	return z, nil
}

func New(ctx context.Context) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(2048).WithCloseOnContextDone(true).WithCompilationCache(compilationCache)
	rt := wazero.NewRuntimeWithConfig(ctx, config)
	fail := func(e error) (*Runtime, error) { _ = rt.Close(context.Background()); return nil, e }
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return fail(err)
	}
	compressed, _ := assets.ReadFile("assets/python.wasm.gz")
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fail(err)
	}
	code, err := io.ReadAll(io.LimitReader(r, 32<<20))
	_ = r.Close()
	if err != nil {
		return fail(err)
	}
	module, err := rt.CompileModule(ctx, code)
	if err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	data, _ := assets.ReadFile("assets/stdlib.zip")
	stdlib, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fail(err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Runtime{runtime: rt, module: module, stdlib: stdlib, slots: make(chan struct{}, 2), lifetime: lifetime, cancel: cancel}, nil
}

func (r *Runtime) Close(ctx context.Context) error {
	r.cancel()
	r.active.Lock()
	defer r.active.Unlock()
	return r.runtime.Close(ctx)
}

type cappedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > MaxOutputBytes-b.Len() {
		b.exceeded = true
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

func (r *Runtime) Run(ctx context.Context, in Invocation) (Result, error) {
	r.active.RLock()
	defer r.active.RUnlock()
	if err := r.lifetime.Err(); err != nil {
		return Result{}, err
	}
	code, err := ValidateCode(in.Code, in.Handler)
	if err != nil {
		return Result{}, err
	}
	if !json.Valid(in.Event) || len(in.Event) > MaxOutputBytes {
		return Result{}, errors.New("Event must be JSON of at most 256 KiB")
	}
	if in.Timeout <= 0 || in.Timeout > 30*time.Second {
		return Result{}, errors.New("Use a function timeout from greater than zero through thirty seconds")
	}
	if len(in.Environment) > 64 || len(in.Context) > 32 {
		return Result{}, errors.New("Too many function environment or context entries")
	}
	size := 0
	for key, value := range in.Environment {
		size += len(key) + len(value)
	}
	for key, value := range in.Context {
		size += len(key) + len(value)
	}
	if size > 8192 {
		return Result{}, errors.New("Function environment and context exceed 8 KiB")
	}
	ctx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	stop := context.AfterFunc(r.lifetime, cancel)
	defer stop()
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	envelope, _ := json.Marshal(map[string]any{"handler": in.Handler, "event": in.Event, "context": in.Context})
	bootstrap, _ := assets.ReadFile("bootstrap.py")
	var output, stderr cappedOutput
	config := wazero.NewModuleConfig().WithName("").WithArgs("python", "-I", "-B", "-c", string(bootstrap)).WithStdin(bytes.NewReader(envelope)).WithStdout(&output).WithStderr(&stderr).WithRandSource(rand.Reader).WithSysWalltime().WithSysNanotime().WithNanosleep(func(ns int64) {
		timer := time.NewTimer(time.Duration(ns))
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}).WithFSConfig(wazero.NewFSConfig().WithFSMount(r.stdlib, "/python/lib/python3.12").WithFSMount(code, "/var/task"))
	for key, value := range in.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return Result{}, errors.New("Invalid function environment")
		}
		config = config.WithEnv(key, value)
	}
	_, err = r.runtime.InstantiateModule(ctx, r.module, config)
	if ctx.Err() != nil {
		return Result{Logs: output.String()[:min(output.Len(), 64<<10)]}, ctx.Err()
	}
	if output.exceeded || stderr.exceeded {
		return Result{}, errors.New("Function output exceeded 256 KiB")
	}
	if err != nil {
		return Result{}, fmt.Errorf("Python sandbox exited before returning: %w", err)
	}
	marker := []byte("\x1eTCSIM_RESULT ")
	index := bytes.LastIndex(output.Bytes(), marker)
	if index < 0 {
		return Result{}, errors.New("Python sandbox returned no result")
	}
	var response struct {
		Payload json.RawMessage `json:"payload"`
		Error   bool            `json:"error"`
	}
	if json.Unmarshal(output.Bytes()[index+len(marker):], &response) != nil || response.Payload == nil {
		return Result{}, errors.New("Python sandbox returned an invalid result")
	}
	logs := string(output.Bytes()[:index]) + stderr.String()
	if len(logs) > 64<<10 {
		logs = logs[:64<<10]
	}
	return Result{response.Payload, response.Error, logs}, nil
}
