package iceroot

import (
	"bytes"
	"context"
	"fmt"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"os"
	"path/filepath"
	"testing"
)

// These are the original Rust assertions hosted by Go, covering every checked-in vector class
// including explicit unsupported-operation skips. The public Go wrappers are tested separately.
func TestSharedVectorRunners(t *testing.T) {
	dir := os.Getenv("SDK_VECTOR_MODULES")
	if dir == "" {
		t.Skip("run scripts/test-vectors.sh")
	}
	rust := os.Getenv("SDK_RUST_DIR")
	if rust == "" {
		t.Fatal("SDK_RUST_DIR required")
	}
	names := []string{"iceroot-sdk-core-heartwood_vectors", "iceroot-sdk-core-sdk_vectors", "iceroot-keystore-vectors", "iceroot-vote-vectors", "iceroot-vote-modes", "iceroot-sdk-api-fixtures"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			wasm, err := os.ReadFile(filepath.Join(dir, name+".wasm"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(16384))
			defer r.Close(ctx)
			if _, err = wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			config := wazero.NewModuleConfig().WithArgs(name, "--test-threads=1", "--nocapture").WithStdout(&out).WithStderr(&out).WithFSConfig(wazero.NewFSConfig().WithReadOnlyDirMount(rust, rust))
			_, err = r.InstantiateWithConfig(ctx, wasm, config)
			if exit, ok := err.(*sys.ExitError); ok && exit.ExitCode() == 0 {
				err = nil
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			fmt.Print(out.String())
			if !bytes.Contains(out.Bytes(), []byte("test result: ok")) {
				t.Fatal("runner did not report success")
			}
		})
	}
}
