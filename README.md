# IceRoot SDK for Go

A pure Go host for the IceRoot Rust SDK. The module embeds a `wasm32-wasip1` core and runs it
with wazero. Applications need Go 1.26 or later, with no cgo, Rust installation or native library.
The Rust core owns keys, derivation, signatures, transaction encoding, validation, fee rules and
vote selection; Go provides HTTP, cancellation and typed application values.

This version uses `heartwood-crypto` at `crypto-v0.1.0` and supports the classical devnet only.
Post-quantum profiles, names, assets, swaps and finality remain unavailable until the core
implements them. Capabilities report that accurately. `WaitFinal` fails with
`UnsupportedOnNetwork` on the classical devnet; `WaitConfirmed` means inclusion only and must
not be used to credit finalized deposits.

```go
package main

import (
    "context"
    "fmt"
    "os"
    sdk "github.com/iceroot-network/sdk-go"
)

func main() {
    ctx := context.Background()
    core, err := sdk.New(ctx)
    if err != nil { panic(err) }
    defer core.Close(ctx)
    addresses, err := core.AddressPool(ctx, sdk.Devnet(), os.Getenv("ICEROOT_PHRASE"),
        sdk.AccountOptions{}, 10)
    if err != nil { panic(err) }
    fmt.Println(addresses)
}
```

Until a module release is available, use a local `replace` directive or a repository revision
with read access. Private dependencies can use `GOPRIVATE=github.com/iceroot-network/*` and SSH.

## API

- `New`, `Close`: an isolated runtime. Calls are serialized and safe from concurrent goroutines;
  separate instances allow parallel signing. A cancelled or trapped core call closes its instance.
- `GeneratePhrase`, `CheckPhrase`, `FromPhrase`, `FromPhraseBytes`, `AddressPool`: offline
  hardened derivation. New accounts require 18, 21 or 24 words. `FromLegacyPassphrase` imports
  existing classical identities only. Release each `Account` when finished.
- `EncryptKeystore`, `FromKeystore`, `ChangeKeystorePassword`, `ReencryptKeystore`,
  `InspectKeystore`, `ArmorKeystore`, `DearmorKeystore`: the shared encrypted recovery format. `FromKeystore` derives internally without returning
  the phrase.
- `ParseAmount`, `FormatAmount`, `ValidateAddress`, `AddressFromPublicKey`: core validation.
  `Amount` is a decimal integer string in base units; neither amounts nor nonces use floats.
- `SignMessage`, `VerifyMessage`, `BuildSignIn`, `ParseSignIn`, `Account.SignSignIn`: shared
  message formats. A message is UTF-8 text: other bytes are refused with `InvalidArgument`, since
  in today's format a message signature over a transaction's bytes would sign that transaction.
  Text whose first line is an ownership proof's is refused too. Sign a website's sign-in message
  with `SignSignIn`, which signs only once the message passes its checks against the page's
  origin, the account and the time; a refusal is `InvalidSignIn`.
- `Connect`, `Network.Build`, `Draft.Sign`, `Network.Submit`: node facts, portable drafts,
  isolated signing and pool outcomes. `Network.DeserializeDraft` and `Network.SignDraft` read a
  serialized draft on the connection's chain. Transfer, vote, vote withdrawal, burn, second-key
  registration, validator registration and resignation use `BuildRequest.Operation`.
- `Network.Info`, `Refresh`, `LoadChain`: the token and the rules, economics and vote rules in
  force at a height. `Info` is for the node's next block as of `Connect` or the last `Refresh`.
- `Network.Account`, `Status`, `Transaction`, `WaitConfirmed`, `WaitFinal`: typed common reads.
  `Network.Read` exposes every shared read operation, including paged history, validators,
  blocks, fee statistics, supply, votes and name resolution. Supply an output struct or use
  `json.RawMessage`; wide integers remain decimal strings.
- `Vote`: `select`, `evaluate`, `check`, `validate`, `split`, `voter`, `validateSnapshot`, using
  the shared JSON schemas in the [Rust binding documentation](https://github.com/iceroot-network/sdk-rust/tree/dev/crates/iceroot-sdk-bindings).
  `Network.Info().VoteRules` supplies the network's rules for the next block. Each reason's
  `text` is a sentence for the review screen. A validator's declared names appear in it in
  quotes, written as Rust writes a string literal (control, invisible and direction characters,
  every space but the ASCII space, quotes and backslashes escaped), with every space of a run of
  two or more ASCII spaces written `\u{20}`. Show it on one line, or indent its continuation.

`Connect` tries the configured relays until connected and pins the chain identity. It checks the
crypto configuration and the node configuration against that identity. A connection then sends
each request to its selected relay first. When that relay is unavailable for a request, the
request goes to the profile's other relays in order, each used only once its node configuration
names the pinned chain; a relay of another chain is never asked again. Reconnect to select another
relay first, passing `Network.Profile()` to retain the pin. HTTP uses context deadlines, a
30-second default timeout and conservative rate pacing. `ConnectOptions` accepts an HTTP transport
and request headers. Submission reports preserve per-transaction refusals; transport failure after
a submission can mean uncertain acceptance, so query its id before retrying.

The transport keeps the core's bounds (`transportLimits`), as its Rust and TypeScript clients do.
A relay is unavailable for a request when it cannot be reached, when it answers with a redirect,
which is never followed, or with a server error (HTTP 5xx), when it declares or sends an answer
longer than 8 MiB, or when it stays rate limited. When no other relay answers, the error is the
last relay's, and a server error is the core's reading of that answer (`Refused`):

- Answers are read as the relay sends them and never decompressed: every request names
  `Accept-Encoding: identity`, which also stops Go's transport, or an application's, from
  decompressing. At most 8 MiB is read; the rest of a longer answer is not.
- After HTTP 429 the wait is the core's `backoffDelay`: 2 seconds, doubling, for three retries, or
  the node's `Retry-After` in whole seconds when that is longer. A relay that asks for more than
  a minute, or whose retries are spent, is not waited for. When no other relay answers, the error
  is `RateLimited`, with the node's `retryAfterSeconds` in its details.
- A relay's own text stays out of error messages. A request the transport cannot complete is
  `NodeUnavailable` with a message of the SDK's own; the transport's reason, which can quote what
  the relay sent, is only in the details (`reason`), escaped and cut to 200 characters, as the
  core keeps a node's text.

Use `Network.Build` for current nonce, next height, second-key state and minimum fees. The
lower-level `BuildOffline` takes explicit facts for offline protocols and tests. Applications must
reserve nonces across pending withdrawals themselves. Serialized drafts are revalidated against
the signing profile, and the signer must match their sender. `DeserializeDraft` has the profile
alone, so it computes a fee's floor under the network configuration the draft carries, whose fee
table the pinned network hash does not cover: a fee the draft calls the floor reads `unverified`,
never `floor`, and the floor beside it is for display only. Show such a fee as an amount, never
as the network's minimum. A connected reader uses `Network.DeserializeDraft` and
`Network.SignDraft`, which read the draft on the chain the connection loaded, as the Rust,
WebAssembly and Tauri hosts do: a draft built under another network configuration (another fee
table, say) is refused with `NetworkMismatch` (`reason`: `configuration`), and a fee at that
chain's floor reads `floor`. That floor is the one at the draft's height, which the builder chose;
where a milestone between it and the network's next block changes the fee table, show the fee as
an amount. A second key must belong to the same
runtime as the primary key. Do not mutate exported account metadata or a profile during a call.

The runtime has a 512 MiB linear-memory ceiling and a 16 MiB JSON limit. No filesystem or network
is exposed to production WebAssembly. WASI randomness comes from `crypto/rand.Reader`. Key
handles are never reused, keys are wiped in place, request and response buffers are wiped after
each call, and the host wipes the stack. Closing wipes linear memory. `FromPhraseBytes` and
the keystore functions take phrases and passwords as byte slices, send them without making Go
strings of them and wipe the slices on return. Go strings cannot be wiped, and
neither Go nor WebAssembly execution gives a constant-time guarantee. Keep phrases short lived
and prefer the byte inputs.

## Custodian example

`examples/custodian` derives an address pool without contacting a node, loads network facts in
one runtime and signs the serialized withdrawal in another runtime with no transport.
It submits and waits for `finalized: true`. On the classical devnet, its default finality check
refuses before submitting. `-confirmed` explicitly runs an inclusion-only demonstration.
Use an already funded account at derivation index zero:

```sh
# Set ICEROOT_PHRASE from a secure input source, then:
go run ./examples/custodian -count 10
go run ./examples/custodian -relay http://127.0.0.1:4003/api \
  -to ADDRESS -amount 100000000 -confirmed
```

A real custodian supplies its own approval, nonce reservation and secret-loading policy around
this flow. The example never prints its phrase or private key.

## Build and test

Check out the matching `sdk-rust` beside this repository, or set `SDK_RUST_DIR`. Source builds
use its pinned Rust 1.98.0 toolchain and lockfile, plus clang and llvm-ar with WebAssembly support.
The current classical core compiles libsecp256k1 from C at artifact build time; the Go application
still builds with `CGO_ENABLED=0`.

```sh
rustup target add wasm32-wasip1
CC_wasm32_wasip1=clang AR_wasm32_wasip1=llvm-ar scripts/build-wasm.sh
CGO_ENABLED=0 go test ./...
go vet ./...
scripts/test-vectors.sh
scripts/test-cross-language.sh
python3 scripts/notice.py --check
go test -run '^$' -bench BenchmarkSign -benchtime=3s -count=3 .
```

The embedded module, its SHA-256 and the sdk-rust revision it was built from are committed.
Build flags optimize for size, use LTO and strip symbols and source-machine paths, so the same
revision, Rust 1.98.0 and clang 16 rebuild the same bytes. `scripts/build-wasm.sh` always
produces the production module with test features off. Deterministic-signature test artifacts
stay under `.build`.
To compare against TypeScript as well, build that repository's test distribution and run:

```sh
SDK_TYPESCRIPT_DIR=../sdk-typescript scripts/test-cross-language.sh
```

The vector command compiles the Rust SDK's vector runners for `wasm32-wasip1` and runs their
exact assertions in Go with wazero: 1,765 Heartwood records, 625 SDK records including 136
keystore records, and 69 vote selections, plus the synthetic vote-mode expectations and the node
API fixtures. Every class runs. Operations outside the SDK keep the runners' explicit skip rules:
486 Heartwood records are skipped, while 108 Heartwood and 9 SDK records check the SDK's
documented stricter behavior. Go wrapper tests separately exercise the distributed module,
portable drafts, signing, memory erasure, keystores, transport behavior and finality refusal.
The differential command compares 10,000 core cases and 10,000 random vote snapshots against
native Rust, including byte-identical signatures, transactions, errors and vote explanations.
The optional TypeScript comparison checks 1,000 legacy signatures and 1,000 phrase accounts.

For a short live devnet, use the Rust SDK's harness, which stops and removes its own network:

```sh
ICEROOT_DEVNET_TOOLS=/path/to/devnet-tools E2E_NAME=go-example E2E_ROUNDS=2 \
  ../sdk-rust/tools/e2e/devnet.sh run go test -v -run '^TestCustodianDevnet$' -timeout 4m .
```

The live test funds a fresh phrase account, runs the custodian executable and checks the
recipient's exact balance. Finality remains a later-network gate.

CI tests Go 1.26.8 and 1.27.1, checks formatting, checksums and notices, and cross-compiles for
Linux arm64, macOS arm64 and Windows amd64 without cgo. Its core job checks out the sdk-rust
revision named in `internal/wasm/SOURCE`, rebuilds the module with clang 16 and requires the
committed bytes, then runs the vectors and the differential tests. `scripts/build-wasm.sh` writes
that file, and marks it when sdk-rust had uncommitted changes, which CI refuses. CI reads
heartwood-core with the read-only secret `HEARTWOOD_TOKEN`, and sdk-rust with `SDK_READ_TOKEN`
when that secret is set (a read-only token, needed only while sdk-rust is private).

See [measurements](docs/measurements.md) for module size, signing speed and validation results.
