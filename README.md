# IceRoot SDK for Go

Build accounts, sign transactions and read IceRoot nodes from Go with an embedded WebAssembly core.

## Status

This SDK is in early development. It supports the classical devnet only and has no tagged release yet.
`WaitFinal` is unsupported on this network and returns `UnsupportedOnNetwork`.
`WaitConfirmed` means inclusion only and must not be used to credit finalized deposits.
Post-quantum profiles, names, assets, swaps and finality are unavailable in the embedded core.
Use `Capabilities` to check what a profile supports.

## Install

Go 1.26 or later is required. Applications need no cgo, Rust installation or native library.
There is no tagged release yet, so install from the production branch, or pin an exact commit
with `@<commit>`:

```sh
go get github.com/iceroot-network/sdk-go@prod
```

## Quickstart

Derive ten addresses offline from an existing recovery phrase. Set `ICEROOT_PHRASE` from a secure input source.

```go
package main

import (
	"context"
	"fmt"
	"os"

	sdk "github.com/iceroot-network/sdk-go"
)

func run() (err error) {
	ctx := context.Background()
	phrase := os.Getenv("ICEROOT_PHRASE")
	if phrase == "" {
		return fmt.Errorf("set ICEROOT_PHRASE to an existing recovery phrase")
	}
	core, err := sdk.New(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := core.Close(ctx); err == nil {
			err = closeErr
		}
	}()
	addresses, err := core.AddressPool(ctx, sdk.Devnet(), phrase, sdk.AccountOptions{}, 10)
	if err != nil {
		return err
	}
	_, err = fmt.Println(addresses)
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

## API tour

### Runtime

`New`, `Close`: an isolated runtime. Calls are serialized and safe from concurrent goroutines;
separate instances allow parallel signing. A cancelled or trapped core call closes its instance.

### Accounts

`GeneratePhrase`, `CheckPhrase`, `FromPhrase`, `FromPhraseBytes`, `AddressPool`: offline
hardened derivation. New accounts require 18, 21 or 24 words. `FromLegacyPassphrase` imports
existing classical identities only. Release each `Account` when finished.

### Keystores

`EncryptKeystore`, `FromKeystore`, `ChangeKeystorePassword`, `ReencryptKeystore`,
`InspectKeystore`, `ArmorKeystore`, `DearmorKeystore`: the shared encrypted recovery format.
`FromKeystore` derives internally without returning the phrase. `FromKeystore`, `ChangeKeystorePassword` and `ReencryptKeystore` take a final
`maxMemoryKib uint32`: the most key-derivation memory, in KiB, a keystore from an untrusted
source may ask for. A password change or re-encryption applies it to the stored keystore's
parameters and to the new ones, and either one asking for more is refused with
`ParamsOutOfRange` before any key derivation. The core keeps the ceiling in the format's
range: under 19,456 KiB it counts as 19,456, so 0 is the tightest ceiling rather than none,
and over 524,288 KiB it counts as 524,288, the format's own ceiling and the TypeScript SDK's
default.

### Amounts and addresses

`ParseAmount`, `FormatAmount`, `ValidateAddress`, `AddressFromPublicKey`: core validation.
`Amount` is a decimal integer string in base units; neither amounts nor nonces use floats.

### Messages and sign-in

`SignMessage`, `VerifyMessage`, `BuildSignIn`, `ParseSignIn`, `Account.SignSignIn`: shared
message formats. A message is UTF-8 text: other bytes are refused with `InvalidArgument`, since
in today's format a message signature over a transaction's bytes would sign that transaction.
`SignMessage` also refuses, with `InvalidArgument`, text whose first line is an ownership
proof's and a website's sign-in message: any text the sign-in parser accepts for some network,
origin, account and time, a lapsed challenge included. Sign a website's sign-in message with
`SignSignIn`, which signs only once the message passes its checks against the page's origin,
the account and the time; a refusal is `InvalidSignIn`.

### Transactions

`Connect`, `Network.Build`, `Draft.Sign`, `Network.Submit`: node facts, portable drafts,
isolated signing and pool outcomes. `Network.DeserializeDraft` and `Network.SignDraft` read a
serialized draft on the connection's chain. Transfer, vote, vote withdrawal, burn, second-key
registration, validator registration and resignation use `BuildRequest.Operation`.

### Chain information

`Network.Info`, `Refresh`, `LoadChain`: the token and the rules, economics and vote rules in
force at a height. `Info` is for the node's next block as of `Connect` or the last `Refresh`.

### Node reads

`Network.Account`, `Status`, `Transaction`, `WaitConfirmed`, `WaitFinal`: typed common reads.
`Network.Read` exposes every shared read operation, including paged history, validators,
blocks, fee statistics, supply, votes and name resolution. Supply an output struct or use
`json.RawMessage`; wide integers remain decimal strings.

### Votes

`Vote`: `select`, `evaluate`, `check`, `validate`, `split`, `voter`, `validateSnapshot`, using
the shared JSON schemas in the [Rust binding documentation](https://github.com/iceroot-network/sdk-rust/tree/prod/crates/iceroot-sdk-bindings).
`Network.Info().VoteRules` supplies the network's rules for the next block. Each reason's
`text` is a sentence for the review screen. A validator's declared names appear in it in
quotes, written as Rust writes a string literal (control, invisible and direction characters,
every space but the ASCII space, quotes and backslashes escaped), with every space of a run of
two or more ASCII spaces written `\u{20}`. Show it on one line, or indent its continuation.

See [transport and draft handling](docs/transport.md) for relay failover, request bounds,
submission uncertainty and fee verification. See [runtime and secret handling](docs/memory.md)
for memory limits, wiping and byte-based secret inputs.

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

## Documentation

- [IceRoot documentation](https://docs.iceroot.com)
- [Rust SDK](https://github.com/iceroot-network/sdk-rust/tree/prod)
- [TypeScript SDK](https://github.com/iceroot-network/sdk-typescript/tree/prod)
- [Development](docs/development.md)
- [Measurements](docs/measurements.md)

## Security

Follow the [security policy](https://github.com/iceroot-network/.github/blob/prod/SECURITY.md)
to report a vulnerability. Do not report vulnerabilities in public issues.

## Contributing

Read the [contributing guide](https://github.com/iceroot-network/.github/blob/prod/CONTRIBUTING.md).
Open pull requests against `dev`.

## Licence

Licensed under Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
