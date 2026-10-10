package iceroot

import (
	"context"
	"encoding/hex"
	"encoding/json"
)

// Account holds an opaque key in one SDK instance. Public metadata remains usable after Release.
// Secret key bytes are never returned. Do not copy an Account; share its pointer.
type Account struct {
	Address   string `json:"address"`
	PublicKey string `json:"publicKey"`
	Path      string `json:"path"`
	Legacy    bool   `json:"legacy"`
	handle    uint32
	sdk       *SDK
}
type AccountOptions struct {
	Account    uint32
	Index      uint32
	Passphrase string
}

func (s *SDK) account(ctx context.Context, op string, args map[string]any) (*Account, error) {
	var result struct {
		Handle    uint32 `json:"handle"`
		Address   string `json:"address"`
		PublicKey string `json:"publicKey"`
		Path      string `json:"path"`
		Legacy    bool   `json:"legacy"`
	}
	if err := s.call(ctx, op, args, &result); err != nil {
		return nil, err
	}
	return &Account{Address: result.Address, PublicKey: result.PublicKey, Path: result.Path, Legacy: result.Legacy, handle: result.Handle, sdk: s}, nil
}

// FromPhrase derives a hardened account from an 18, 21 or 24 word phrase.
func (s *SDK) FromPhrase(ctx context.Context, p Profile, phrase string, o AccountOptions) (*Account, error) {
	return s.account(ctx, "keyPhrase", map[string]any{"profile": p, "phrase": phrase, "account": o.Account, "index": o.Index, "passphrase": o.Passphrase})
}

// FromLegacyPassphrase imports an existing classical account. Use FromPhrase for new accounts.
func (s *SDK) FromLegacyPassphrase(ctx context.Context, p Profile, passphrase string) (*Account, error) {
	return s.account(ctx, "keyLegacy", map[string]any{"profile": p, "passphrase": passphrase})
}
func (a *Account) Release(ctx context.Context) error {
	return a.sdk.call(ctx, "keyRelease", map[string]any{"key": a.handle}, nil)
}

// SignMessage signs UTF-8 text only. It refuses with InvalidArgument other bytes (which may
// be a transaction's), text whose first line is an ownership proof's, and a website's sign-in
// message. Sign-in text is any text the sign-in parser accepts for some network, origin, account
// and time, including a lapsed challenge. Sign a website's sign-in message with SignSignIn.
func (a *Account) SignMessage(ctx context.Context, message []byte) (MessageSignature, error) {
	var out MessageSignature
	err := a.sdk.call(ctx, "signMessage", map[string]any{"key": a.handle, "message": hex.EncodeToString(message)}, &out)
	return out, err
}
func (s *SDK) VerifyMessage(ctx context.Context, message []byte, signature MessageSignature) (bool, error) {
	var out bool
	err := s.call(ctx, "verifyMessage", map[string]any{"message": hex.EncodeToString(message), "publicKey": signature.PublicKey, "signature": signature.Signature, "algorithm": signature.Algorithm}, &out)
	return out, err
}
func (s *SDK) GeneratePhrase(ctx context.Context) (string, error) {
	var out string
	err := s.call(ctx, "phraseGenerate", nil, &out)
	return out, err
}
func (s *SDK) CheckPhrase(ctx context.Context, phrase string) (PhraseCheck, error) {
	var out PhraseCheck
	err := s.call(ctx, "phraseCheck", map[string]any{"phrase": phrase}, &out)
	return out, err
}
func (s *SDK) Capabilities(ctx context.Context, p Profile) ([]string, error) {
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	err := s.call(ctx, "profile", map[string]any{"profile": p}, &out)
	return out.Capabilities, err
}
func (s *SDK) ParseAmount(ctx context.Context, text string, decimals uint8) (Amount, error) {
	var out Amount
	err := s.call(ctx, "amountParse", map[string]any{"text": text, "decimals": decimals}, &out)
	return out, err
}
func (s *SDK) FormatAmount(ctx context.Context, amount Amount, decimals uint8) (string, error) {
	var out string
	err := s.call(ctx, "amountFormat", map[string]any{"amount": amount, "decimals": decimals}, &out)
	return out, err
}
func (s *SDK) ValidateAddress(ctx context.Context, p Profile, address string) error {
	return s.call(ctx, "addressParse", map[string]any{"profile": p, "address": address}, nil)
}
func (s *SDK) AddressFromPublicKey(ctx context.Context, p Profile, key string) (string, error) {
	var out string
	err := s.call(ctx, "addressFromKey", map[string]any{"profile": p, "publicKey": key}, &out)
	return out, err
}

// AddressPool derives and releases each key offline, returning only its public metadata.
func (s *SDK) AddressPool(ctx context.Context, p Profile, phrase string, o AccountOptions, count uint32) ([]string, error) {
	if count > 10000 || uint64(o.Index)+uint64(count) > 1<<31 {
		return nil, &Error{Code: "InvalidPath", Message: "address pool exceeds index or batch limit"}
	}
	out := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		opts := o
		opts.Index += i
		a, err := s.FromPhrase(ctx, p, phrase, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, a.Address)
		if err = a.Release(context.Background()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Vote calls the Rust vote library. Structured arguments use its canonical JSON schema.
// first, second and third follow the shared binding: select(snapshot, request),
// evaluate(snapshot, mode), check(selection, snapshot), validate(entries, rules, voter),
// split(validators), voter(snapshot, address), or validateSnapshot(snapshot).
func (s *SDK) Vote(ctx context.Context, operation, first, second, third string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "vote", map[string]any{"operation": operation, "first": first, "second": second, "third": third}, &out)
	return out, err
}

// FromPhraseBytes is FromPhrase for a phrase held as UTF-8 bytes. The slice is overwritten with
// zeros on return, whatever the outcome, and no Go string is made of it.
func (s *SDK) FromPhraseBytes(ctx context.Context, p Profile, phrase []byte, o AccountOptions) (*Account, error) {
	defer clear(phrase)
	return s.account(ctx, "keyPhrase", map[string]any{"profile": p, "phraseHex": secret(phrase), "account": o.Account, "index": o.Index, "passphrase": o.Passphrase})
}
