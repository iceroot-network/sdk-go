# Local validation and measurements

Measured on Linux amd64, AMD Ryzen 9 7950X, with Go 1.27.1, wazero 1.12.0, Rust 1.98.0 and
clang 16. Source builds use the release flags in `scripts/build-wasm.sh`. These are classical
BIP340 measurements; a later post-quantum core must be measured again. The machine also runs
other work, so timings are observations rather than service guarantees.

The embedded production module is 1,183,833 bytes, or 403,067 bytes with `gzip -n -9`.
Its checksum is in `internal/wasm/SHA256SUMS`, and the sdk-rust revision it was built from in
`internal/wasm/SOURCE`; a build of that revision from a clean export, at another path and with an
empty target directory, gave the same bytes. It includes key derivation, transaction handling,
node response mapping, the vote library and keystores. Fixed signing randomness is unavailable
in this artifact and is tested as a rejection.

With the module built from sdk-rust e3904dd,
`go test -run '^$' -bench BenchmarkSign -benchtime=3s -count=3 .` measured:

| Operation | Three runs, milliseconds per operation | Median throughput |
| --- | --- | --- |
| Sign a message | 2.373, 2.303, 2.275 | about 430 per second |
| Sign a portable transfer draft | 8.839, 8.737, 8.700 | about 114 per second |

These include Go/JSON/WASI crossing, response parsing and buffer and stack wiping. Transfer
signing also deserializes and validates the draft and returns its serialized form, node JSON,
bytes, id and summary. HTTP and account derivation are excluded. Calls within an instance are
serialized; use multiple instances for parallel work. These results support retaining the pure
Go host for classical signing; the application must measure its own concurrency and workload.

This module was not measured on an idle machine; run in turn with the e3904dd module on a busy
one, it signed a message just as fast.

Validation:

- Go 1.26.8 and 1.27.1 pass the Go tests and static checks, and so does the race detector.
  Builds for Linux arm64, macOS arm64 and Windows amd64 pass with `CGO_ENABLED=0`.
- The exact shared vector assertions run in Go/wazero: 1,765 Heartwood records, 625 SDK records
  including 136 keystore records, and 69 vote selections. Of the Heartwood records, 1,171 match
  the oracle, 108 assert documented stricter behavior and 486 explicitly skip operations outside
  the SDK. All 625 SDK records are checked, including 9 documented differences. No vector class
  is silently omitted.
- The vote mode tests also check the synthetic snapshot's expected pools, scores and selections;
  69 node API fixtures run through WASI as well.
- 10,000 core cases and 10,000 random vote snapshots produce 37,237 byte-identical native Rust
  and Go/WASI responses. With an earlier module, the TypeScript comparison matched 1,000 legacy
  keys and fixed-aux message signatures plus 1,000 phrase-derived accounts.
- The production artifact tests cover offline address pools, every supported operation,
  portable drafts across runtimes, wrong networks, released keys, concurrent callers, keystore
  opening, cancellation, response bounds, rate-limit waits and finality refusal. Memory tests
  search the instance for input phrases and passwords (as text and as the hex they cross in) and
  for released secret scalars, including after map growth and deletion, and check that
  cancellation wipes the interrupted instance.
- With an earlier module, the custodian executable ran on a fresh classical devnet: funding,
  withdrawal submission and inclusion succeeded, and the recipient received exactly 100,000,000
  base units. The chain was stopped at height 4.

A run that waits for `finalized: true` needs a network with finality, which this core does not
have yet: it has only the classical backend and reports no finality capability, so the example
refuses its default finality flow before submitting. Post-quantum signing, a source build with no
C dependency and a run against a network with finality wait for the later core and node formats.
