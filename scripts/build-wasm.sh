#!/usr/bin/env bash
# Builds the embedded module from sdk-rust (beside this repository, or SDK_RUST_DIR) with fixed
# release settings. The same sdk-rust revision, Rust toolchain and clang give the same bytes.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
rust=$(cd "${SDK_RUST_DIR:-$root/../sdk-rust}" && pwd)
export CC_wasm32_wasip1=${CC_wasm32_wasip1:-clang}
export AR_wasm32_wasip1=${AR_wasm32_wasip1:-llvm-ar}
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-"$rust/target"}
# Select the Rust repository's pinned toolchain even when invoked from Go.
cd "$rust"
export CARGO_PROFILE_RELEASE_OPT_LEVEL=z
export CARGO_PROFILE_RELEASE_LTO=true
export CARGO_PROFILE_RELEASE_CODEGEN_UNITS=1
export CARGO_PROFILE_RELEASE_STRIP=symbols
# Remove build-machine paths from the distributed module.
export RUSTFLAGS="${RUSTFLAGS:-} --remap-path-prefix=$rust=/sdk-rust --remap-path-prefix=${CARGO_HOME:-$HOME/.cargo}=/cargo"
cargo build --manifest-path "$rust/Cargo.toml" --locked -p iceroot-sdk-ffi --release --target wasm32-wasip1
cp "$CARGO_TARGET_DIR/wasm32-wasip1/release/iceroot_sdk_ffi.wasm" "$root/internal/wasm/iceroot_sdk_ffi.wasm"
(cd "$root/internal/wasm" && sha256sum iceroot_sdk_ffi.wasm > SHA256SUMS)
# Record the sdk-rust revision the module was built from: CI rebuilds it from that revision and
# requires the same bytes.
revision=$(git -C "$rust" rev-parse HEAD)
if [ -n "$(git -C "$rust" status --porcelain)" ]; then
    echo "build-wasm: sdk-rust has uncommitted changes, so this module cannot be rebuilt from $revision" >&2
    revision="$revision-modified"
fi
printf 'sdk-rust %s\n' "$revision" > "$root/internal/wasm/SOURCE"
