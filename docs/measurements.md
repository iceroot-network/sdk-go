# Measurements

## Embedded module size

Measured from the committed artifact on 2026-10-10 UTC: 1,183,833 bytes uncompressed
and 403,605 bytes with gzip at level 9 and no timestamp.
Reproduce the size check with:

```sh
wc -c < internal/wasm/iceroot_sdk_ffi.wasm
gzip -n -9 -c internal/wasm/iceroot_sdk_ffi.wasm | wc -c
(cd internal/wasm && sha256sum -c SHA256SUMS)
```

The checksum is in `internal/wasm/SHA256SUMS`. The source revision is in `internal/wasm/SOURCE`.

## Signing benchmark method

The previous benchmark notes described x86-64 Linux, 16 cores, Go 1.27.1.
They did not record a measurement date for the current artifact, so no timing figures are retained.
Measure the current module with:

```sh
go test -run '^$' -bench BenchmarkSign -benchtime=3s -count=3 .
```

`BenchmarkSignMessage` measures message signing. `BenchmarkSignTransaction` measures signing
a portable transfer draft. Both reuse a derived account and exclude HTTP and account derivation.
The calls include the Go/JSON/WebAssembly boundary, response parsing and call cleanup.
Transaction signing also revalidates the serialized draft and returns its serialized form,
node JSON, bytes and id.

Record the date, platform, Go version, artifact checksum and all three runs when publishing results.
Calls within a runtime are serialized. Measure the application's concurrency and workload separately.

## Validation scope

See [development](development.md) for the test commands and CI configuration.
The repository includes tests for production-artifact signing, drafts, memory handling,
transport limits, keystores and finality refusal. Test coverage does not establish that a
particular artifact passed those checks. Record the revision and command output with each run.
