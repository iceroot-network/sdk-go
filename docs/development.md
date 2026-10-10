# Development

The embedded WebAssembly module is built from the `sdk-rust` revision named in
`internal/wasm/SOURCE`. Its checksum is in `internal/wasm/SHA256SUMS`.
Check out that revision of [sdk-rust](https://github.com/iceroot-network/sdk-rust/tree/prod)
beside this repository, or set `SDK_RUST_DIR` to its checkout.

## Rebuild the embedded module

Use the Rust checkout's pinned Rust 1.98.0 toolchain and lockfile, plus clang and llvm-ar
with WebAssembly support. CI uses clang 16 and llvm-ar 16 for the artifact rebuild.
The classical core compiles libsecp256k1 from C when building the artifact.
Go applications can still build with `CGO_ENABLED=0`.

```sh
rustup target add wasm32-wasip1
CC_wasm32_wasip1=clang-16 AR_wasm32_wasip1=llvm-ar-16 scripts/build-wasm.sh
```

The script optimizes for size, enables LTO, strips symbols and remaps source paths.
It updates the module, checksum and source revision. It builds the production module without
test features. A checkout with uncommitted changes gets a modified source marker, which CI refuses.
Deterministic-signature test artifacts stay under `.build`.

## Checks

```sh
CGO_ENABLED=0 go test ./...
go vet ./...
scripts/test-vectors.sh
scripts/test-cross-language.sh
python3 scripts/notice.py --check
```

Go tests cover the embedded module, portable drafts, signing, memory erasure, keystores,
transport behavior and finality refusal. The vector script compiles the Rust vector runners
for `wasm32-wasip1` and runs their assertions with wazero. It includes core and keystore
vectors, vote selections, vote modes and node API fixtures. The runners retain their explicit
skip rules for operations outside the SDK.

The differential script compares core cases and random vote snapshots with native Rust.
It builds separate artifacts with test features for deterministic signatures.
To include the optional TypeScript comparison, build that repository's test distribution and run:

```sh
SDK_TYPESCRIPT_DIR=../sdk-typescript scripts/test-cross-language.sh
```

## Live devnet test: maintainers only

This test needs a devnet tooling checkout that is not public. Set `ICEROOT_DEVNET_TOOLS`
to that checkout before running the Rust SDK harness:

```sh
E2E_NAME=go-example E2E_ROUNDS=2 \
  ../sdk-rust/tools/e2e/devnet.sh run go test -v -run '^TestCustodianDevnet$' -timeout 4m .
```

The harness stops and removes its own network. The test funds a fresh phrase account,
runs the custodian executable and checks the recipient's exact balance. It tests inclusion,
not finality. Keep live devnets to at most five rounds.

## CI

CI is configured to test Go 1.26.8 and 1.27.1, check formatting, module checksums and the
embedded artifact checksum, and run tests and static checks. It cross-compiles for Linux arm64,
macOS arm64 and Windows amd64 without cgo.

The core job checks out the Rust revision in `internal/wasm/SOURCE`, rebuilds the module
and requires identical committed bytes. It also runs tests, vectors, differential comparisons
and notice checks. These describe the configured checks, not a record of a successful run.

See [measurements](measurements.md) for artifact size and the benchmark method.
