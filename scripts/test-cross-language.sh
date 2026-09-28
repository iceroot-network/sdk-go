#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
rust=$(cd "${SDK_RUST_DIR:-$root/../sdk-rust}" && pwd)
export CC_wasm32_wasip1=${CC_wasm32_wasip1:-clang}
export AR_wasm32_wasip1=${AR_wasm32_wasip1:-llvm-ar}
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-"$rust/target"}
# Select the Rust repository's pinned toolchain even when invoked from Go.
cd "$rust"
export SDK_RUST_DIR="$rust"
# Test artifacts live outside the embedded production directory.
cargo build --manifest-path "$rust/Cargo.toml" --locked -p iceroot-sdk-ffi --release --example json --features test-seams
cargo build --manifest-path "$rust/Cargo.toml" --locked -p iceroot-sdk-ffi --release --target wasm32-wasip1 --features test-seams
export SDK_NATIVE_JSON="$CARGO_TARGET_DIR/release/examples/json"
mkdir -p "$root/.build"
cp "$CARGO_TARGET_DIR/wasm32-wasip1/release/iceroot_sdk_ffi.wasm" "$root/.build/test-core.wasm"
export SDK_TEST_WASM="$root/.build/test-core.wasm"
cd "$root"
go test -v -run '^TestDifferential$' -timeout 20m .
if [ -n "${SDK_TYPESCRIPT_DIR:-}" ]; then
    go test -v -run '^TestTypeScriptParity$' -timeout 5m .
fi
