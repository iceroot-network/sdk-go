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
export SDK_VECTOR_MODULES="$root/.build/vectors"
mkdir -p "$SDK_VECTOR_MODULES"
# Use the existing runners with their class counts, documented differences and explicit skips.
for pair in 'iceroot-sdk-core heartwood_vectors' 'iceroot-sdk-core sdk_vectors' 'iceroot-keystore vectors' 'iceroot-vote vectors' 'iceroot-vote modes' 'iceroot-sdk-api fixtures'; do
    read -r package test <<< "$pair"
    cargo test --manifest-path "$rust/Cargo.toml" --locked -p "$package" --test "$test" --target wasm32-wasip1 --release --no-run --message-format=json > "$root/.build/artifacts.jsonl"
    python3 - "$root/.build/artifacts.jsonl" "$SDK_VECTOR_MODULES/$package-$test.wasm" <<'PY'
import json, sys, shutil
artifacts = [json.loads(line) for line in open(sys.argv[1])]
executables = [x['executable'] for x in artifacts if x.get('executable')]
assert len(executables) == 1, executables
shutil.copyfile(executables[0], sys.argv[2])
PY
done
cd "$root"
go test -v -run '^TestSharedVectorRunners$' -timeout 20m .
