# Transport and draft handling

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
`Network.SignDraft`. These read the draft using the connected chain configuration.
A draft built under another configuration, such as another fee table, is refused with
`NetworkMismatch` (`reason`: `configuration`). The floor is the one
at the draft's height, which the builder chose, so they also give the core the height of the
network's next block, as of the connection's last status read (`Connect`, `Refresh`, `Status` or
`Build`; a lower height than before is followed too): a fee at the draft's floor reads `floor`
when the floor of that block is the same, and `unverified`, with the floor kept for display,
when a milestone between the two heights changes the fee table. A second key must belong to the
same runtime as the primary key. Do not mutate exported account metadata or a profile during a
call.
