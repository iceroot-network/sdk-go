package iceroot

import (
	"context"
	"encoding/hex"
	"encoding/json"
)

// KeystoreParams are the authenticated Argon2id parameters.
type KeystoreParams struct {
	MemoryKib   uint32 `json:"memoryKib"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint32 `json:"parallelism"`
}

// DefaultKeystoreParams fits the WebAssembly memory budget with a 64 MiB Argon2 allocation.
func DefaultKeystoreParams() KeystoreParams {
	return KeystoreParams{MemoryKib: 65536, Iterations: 3, Parallelism: 1}
}

// EncryptKeystore encrypts a recovery phrase's entropy under a password. The phrase and the
// password are UTF-8 bytes; both slices are overwritten with zeros on return, whatever the
// outcome, and no Go string is made of them.
func (s *SDK) EncryptKeystore(ctx context.Context, phrase, password []byte, params KeystoreParams) ([]byte, error) {
	defer clear(phrase)
	defer clear(password)
	var out string
	err := s.call(ctx, "keystoreEncrypt", map[string]any{"phrase": secret(phrase), "password": secret(password), "params": params}, &out)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(out)
}

// FromKeystore opens a keystore and derives the account inside the core; the recovery phrase
// never reaches Go. maxMemoryKib bounds the work a keystore from an untrusted source can ask
// for. The password slice is overwritten with zeros on return, and no Go string is made of it.
func (s *SDK) FromKeystore(ctx context.Context, p Profile, keystore, password []byte, o AccountOptions, maxMemoryKib uint32) (*Account, error) {
	defer clear(password)
	return s.account(ctx, "keyKeystore", map[string]any{"profile": p, "keystore": hex.EncodeToString(keystore), "password": secret(password), "account": o.Account, "index": o.Index, "passphrase": o.Passphrase, "maxMemoryKib": maxMemoryKib})
}

// ChangeKeystorePassword encrypts a keystore again under newPassword with params, a fresh salt
// and a fresh nonce, once password opens it. Both password slices are overwritten with zeros on
// return, and no Go string is made of them. maxMemoryKib bounds both the keystore
// parameters and params; exceeding it returns ParamsOutOfRange.
func (s *SDK) ChangeKeystorePassword(ctx context.Context, keystore, password, newPassword []byte, params KeystoreParams, maxMemoryKib uint32) ([]byte, error) {
	defer clear(password)
	defer clear(newPassword)
	var out string
	err := s.call(ctx, "keystoreChangePassword", map[string]any{"keystore": hex.EncodeToString(keystore), "password": secret(password), "newPassword": secret(newPassword), "params": params, "maxMemoryKib": maxMemoryKib}, &out)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(out)
}

// ReencryptKeystore encrypts a keystore again under the same password with new params, a fresh
// salt and a fresh nonce, for example to move it to stronger parameters. The password slice is
// overwritten with zeros on return, and no Go string is made of it. maxMemoryKib bounds
// both the keystore parameters and params; exceeding it returns ParamsOutOfRange.
func (s *SDK) ReencryptKeystore(ctx context.Context, keystore, password []byte, params KeystoreParams, maxMemoryKib uint32) ([]byte, error) {
	defer clear(password)
	var out string
	err := s.call(ctx, "keystoreReencrypt", map[string]any{"keystore": hex.EncodeToString(keystore), "password": secret(password), "params": params, "maxMemoryKib": maxMemoryKib}, &out)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(out)
}

func (s *SDK) InspectKeystore(ctx context.Context, keystore []byte) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "keystoreInspect", map[string]any{"keystore": hex.EncodeToString(keystore)}, &out)
	return out, err
}
func (s *SDK) ArmorKeystore(ctx context.Context, keystore []byte) (string, error) {
	var out string
	err := s.call(ctx, "keystoreArmor", map[string]any{"keystore": hex.EncodeToString(keystore)}, &out)
	return out, err
}
func (s *SDK) DearmorKeystore(ctx context.Context, text string) ([]byte, error) {
	var out string
	err := s.call(ctx, "keystoreDearmor", map[string]any{"text": text}, &out)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(out)
}
