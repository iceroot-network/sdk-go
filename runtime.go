// Package iceroot provides accounts, transaction signing and a node client backed by the
// IceRoot Rust SDK compiled to WebAssembly. It requires neither cgo nor a native library.
package iceroot

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed internal/wasm/iceroot_sdk_ffi.wasm
var core []byte

const maxJSON = 16 * 1024 * 1024

var compiledCache = wazero.NewCompilationCache()

// Error preserves the stable SDK code and structured details across the language boundary.
type Error struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// errorCode is the SDK code of err, or empty when err is not an SDK error.
func errorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// SDK owns an isolated runtime and its secret keys. Calls are safe from multiple goroutines,
// serialized within one instance. Use independent instances for parallel signing.
type SDK struct {
	runtime wazero.Runtime
	module  api.Module
	gate    chan struct{}
	closed  bool
}

// New loads the embedded core. Close the SDK after releasing its accounts.
func New(ctx context.Context) (*SDK, error) { return newSDK(ctx, core) }
func newSDK(ctx context.Context, wasm []byte) (*SDK, error) {
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(8192).WithCloseOnContextDone(true).WithCompilationCache(compiledCache))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		_ = r.Close(ctx)
		return nil, err
	}
	m, err := r.InstantiateWithConfig(ctx, wasm, wazero.NewModuleConfig().WithName("").WithStartFunctions().WithRandSource(rand.Reader))
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("load core: %w", err)
	}
	s := &SDK{runtime: r, module: m, gate: make(chan struct{}, 1)}
	s.gate <- struct{}{}
	return s, nil
}
func (s *SDK) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.gate:
		return nil
	}
}
func (s *SDK) unlock() { s.gate <- struct{}{} }

// Close wipes the instance's linear memory and releases all resources. It is idempotent.
func (s *SDK) Close(ctx context.Context) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if mem := s.module.Memory(); mem != nil {
		if data, ok := mem.Read(0, mem.Size()); ok {
			clear(data)
		}
	}
	return s.runtime.Close(ctx)
}

// secret marks a request value held as bytes: it is written into the request as hex, in a buffer
// that is wiped after the call, and is never made into a Go string.
type secret []byte

// encodeRequest writes args as a JSON object. Secret values are appended as hex strings to a
// buffer of exact capacity, so no copy of them is left behind by a buffer that grew.
func encodeRequest(args map[string]any) ([]byte, error) {
	public := make(map[string]any, len(args))
	var names []string
	size := 0
	for name, value := range args {
		if value, ok := value.(secret); ok {
			names = append(names, name)
			size += len(name) + 2*len(value) + 6
			continue
		}
		public[name] = value
	}
	head, err := json.Marshal(public)
	if err != nil || len(names) == 0 {
		return head, err
	}
	sort.Strings(names)
	data := make([]byte, 0, len(head)+size)
	data = append(data, head[:len(head)-1]...)
	for _, name := range names {
		if len(data) > 1 {
			data = append(data, ',')
		}
		data = append(data, '"')
		data = append(data, name...)
		data = append(data, `":"`...)
		data = hex.AppendEncode(data, args[name].(secret))
		data = append(data, '"')
	}
	return append(data, '}'), nil
}

func (s *SDK) call(ctx context.Context, op string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	args["op"] = op
	data, err := encodeRequest(args)
	if err != nil {
		return err
	}
	defer clear(data)
	if len(data) > maxJSON {
		return &Error{Code: "InvalidArgument", Message: "request exceeds 16 MiB"}
	}
	if err = s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	if s.closed || s.module.IsClosed() {
		return &Error{Code: "RuntimeClosed", Message: "runtime is closed"}
	}
	// Cleanup uses a live context even when the caller cancels. A trap or cancellation destroys
	// the runtime, so no key handle can be used after uncertain execution.
	defer func() {
		if !s.module.IsClosed() {
			_, _ = s.module.ExportedFunction("sdk_clear").Call(context.Background())
			_, _ = s.module.ExportedFunction("sdk_wipe_stack").Call(context.Background())
		}
	}()
	fail := func(err error) error {
		if memory := s.module.Memory(); memory != nil {
			if data, ok := memory.Read(0, memory.Size()); ok {
				clear(data)
			}
		}
		_ = s.module.Close(context.Background())
		return fmt.Errorf("core call: %w", err)
	}
	p, err := s.module.ExportedFunction("sdk_alloc").Call(ctx, uint64(len(data)))
	if err != nil {
		return fail(err)
	}
	if len(p) != 1 || p[0] == 0 || !s.module.Memory().Write(uint32(p[0]), data) {
		return fail(errors.New("input allocation failed"))
	}
	result, err := s.module.ExportedFunction("sdk_call").Call(ctx)
	if err != nil {
		return fail(err)
	}
	if len(result) != 1 {
		return fail(errors.New("invalid ABI result"))
	}
	ptr, size := uint32(result[0]>>32), uint32(result[0])
	if size == 0 || size > maxJSON {
		return fail(errors.New("invalid output size"))
	}
	response, ok := s.module.Memory().Read(ptr, size)
	if !ok {
		return fail(errors.New("invalid output memory"))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err = json.Unmarshal(response, &envelope); err != nil {
		return fail(err)
	}
	defer clear(envelope.Result)
	if envelope.Error != nil {
		return envelope.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}
