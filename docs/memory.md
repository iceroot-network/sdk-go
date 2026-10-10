# Runtime and secret handling

The runtime has a 512 MiB linear-memory ceiling and a 16 MiB JSON limit. No filesystem or network
is exposed to production WebAssembly. WASI randomness comes from `crypto/rand.Reader`. Key
handles are never reused, keys are wiped in place, request and response buffers are wiped after
each call, and the host wipes the stack. Closing wipes linear memory. `FromPhraseBytes` and
the keystore functions take phrases and passwords as byte slices, send them without making Go
strings of them and wipe the slices on return. Go strings cannot be wiped, and
neither Go nor WebAssembly execution gives a constant-time guarantee. Keep phrases short lived
and prefer the byte inputs.
