package iceroot

import (
	"context"
	"encoding/json"
	"time"
)

type SignInRequest struct {
	Origin    string `json:"origin"`
	PublicKey string `json:"publicKey"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issuedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}
type SignInExpected struct {
	Origin    string `json:"origin,omitempty"`
	PublicKey string `json:"publicKey,omitempty"`
	Address   string `json:"address,omitempty"`
}

func (s *SDK) BuildSignIn(ctx context.Context, p Profile, request SignInRequest) (string, error) {
	var out string
	err := s.call(ctx, "signinBuild", map[string]any{"profile": p, "request": request}, &out)
	return out, err
}
func (s *SDK) ParseSignIn(ctx context.Context, p Profile, message string, expected SignInExpected, now time.Time) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "signinParse", map[string]any{"profile": p, "message": message, "expected": expected, "now": now.UnixMilli()}, &out)
	return out, err
}

// SignSignIn signs a website's sign-in message for the page of origin, as the browser reports
// it, at now. The core first checks the message against that origin, this account's public key and
// address, and the times it names, and signs only when every check passes; a refusal is
// InvalidSignIn with its reason. SignMessage refuses a sign-in message with InvalidArgument.
func (a *Account) SignSignIn(ctx context.Context, message, origin string, now time.Time) (MessageSignature, error) {
	var out MessageSignature
	err := a.sdk.call(ctx, "signinSign", map[string]any{"key": a.handle, "message": message, "origin": origin, "now": now.UnixMilli()}, &out)
	return out, err
}
