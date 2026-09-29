package iceroot

import (
	"context"
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
		account *Account
		origin  string
		now     time.Time
	}{
		{a, "https://validators.example.net", now},
		{a, origin, time.UnixMilli(1_790_000_301_000)},
		{b, origin, now},
	} {
		if _, err = c.account.SignSignIn(ctx, message, c.origin, c.now); errorCode(err) != "InvalidSignIn" {
			t.Fatalf("%s at %s: %v", c.origin, c.now, err)
		}
	}
}
