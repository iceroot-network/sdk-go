package iceroot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMessagesAreSignedOnlyAsText(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	// A transaction's bytes start with 0xff and are not text, so a message signature can never
	// sign one. An ownership proof is signed only by its own proof keys.
	for _, message := range [][]byte{
		{0xff, 0x01, 0x5a, 0x00},
		[]byte("IceRoot migration ownership proof\nVersion: 1"),
	} {
		if _, err = a.SignMessage(ctx, message); errorCode(err) != "InvalidArgument" {
			t.Fatalf("signed %q: %v", message, err)
		}
	}
	message := []byte("any text, on\nmore than one line")
	signature, err := a.SignMessage(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyMessage(ctx, message, signature); err != nil || !ok {
		t.Fatal(ok, err)
	}
}

func TestMessagesRefuseSignInText(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	b, err := s.FromLegacyPassphrase(ctx, Devnet(), "another passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(ctx)
	otherNetwork := Devnet()
	otherNetwork.Chain.NetworkByte = 91
	// Plain signing has no clock, origin or identity to check against: a lapsed challenge and
	// another account's or another network's challenge are sign-in text too.
	for _, c := range []struct {
		name      string
		profile   Profile
		publicKey string
		issuedAt  int64
	}{
		{"challenge", Devnet(), a.PublicKey, 1_790_000_000},
		{"lapsed", Devnet(), a.PublicKey, 1_577_836_800},
		{"another_account", Devnet(), b.PublicKey, 1_790_000_000},
		{"another_network", otherNetwork, a.PublicKey, 1_790_000_000},
	} {
		t.Run(c.name, func(t *testing.T) {
			message, err := s.BuildSignIn(ctx, c.profile, SignInRequest{
				Origin: "https://validators.example", PublicKey: c.publicKey,
				Nonce: strings.Repeat("ab", 32), IssuedAt: c.issuedAt, ExpiresAt: c.issuedAt + 300,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.SignMessage(ctx, []byte(message))
			if errorCode(err) != "InvalidArgument" {
				t.Fatalf("expected InvalidArgument, got %v", err)
			}
			var coreErr *Error
			if !errors.As(err, &coreErr) {
				t.Fatalf("expected core error, got %v", err)
			}
			var details struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(coreErr.Details, &details); err != nil {
				t.Fatal(err)
			}
			const reason = "a sign-in message is signed only for the page that asks for it, never as a plain message"
			if details.Reason != reason {
				t.Fatalf("reason = %q, want %q", details.Reason, reason)
			}
		})
	}
}

func TestMessagesSignSignInNearMisses(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	message, err := s.BuildSignIn(ctx, Devnet(), SignInRequest{
		Origin: "https://validators.example", PublicKey: a.PublicKey,
		Nonce: strings.Repeat("ab", 32), IssuedAt: 1_790_000_000, ExpiresAt: 1_790_000_300,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, text string }{
		{"trailing_newline", message + "\n"},
		{"crlf", strings.ReplaceAll(message, "\n", "\r\n")},
		{"byte_order_mark", "\ufeff" + message},
		{"mention", "This text only mentions sign-in."},
	} {
		t.Run(c.name, func(t *testing.T) {
			signature, err := a.SignMessage(ctx, []byte(c.text))
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := s.VerifyMessage(ctx, []byte(c.text), signature); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if ok, err := s.VerifyMessage(ctx, []byte(message), signature); err != nil || ok {
				t.Fatalf("near miss verified as challenge: %v, %v", ok, err)
			}
		})
	}
}

func TestSignInIsSignedAfterItsChecks(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	origin := "https://validators.example"
	message, err := s.BuildSignIn(ctx, Devnet(), SignInRequest{Origin: origin, PublicKey: a.PublicKey,
		Nonce: strings.Repeat("ab", 32), IssuedAt: 1_790_000_000, ExpiresAt: 1_790_000_300})
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_790_000_010_000)
	signature, err := a.SignSignIn(ctx, message, origin, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyMessage(ctx, []byte(message), signature); err != nil || !ok || signature.PublicKey != a.PublicKey {
		t.Fatal(ok, err, signature)
	}
	// Another site's page, a message that has expired, and another account's sign-in.
	b, err := s.FromLegacyPassphrase(ctx, Devnet(), "another passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(ctx)
	for _, c := range []struct {
		name    string
		account *Account
		origin  string
		now     time.Time
	}{
		{"another_origin", a, "https://validators.example.net", now},
		{"lapsed", a, origin, time.UnixMilli(1_790_000_301_000)},
		{"another_account", b, origin, now},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.account.SignSignIn(ctx, message, c.origin, c.now); errorCode(err) != "InvalidSignIn" {
				t.Fatalf("%s at %s: %v", c.origin, c.now, err)
			}
		})
	}
}
