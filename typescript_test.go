package iceroot

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestTypeScriptParity(t *testing.T) {
	if os.Getenv("SDK_TYPESCRIPT_DIR") == "" {
		t.Skip("set SDK_TYPESCRIPT_DIR and run scripts/test-cross-language.sh")
	}
	wasm, err := os.ReadFile(os.Getenv("SDK_TEST_WASM"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := newSDK(ctx, wasm)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	cmd := exec.Command("node", "scripts/typescript-cases.mjs")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			_ = cmd.Process.Kill()
		}
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	scanner := bufio.NewScanner(out)
	count := 0
	for scanner.Scan() {
		var c struct {
			Kind       string
			Phrase     string
			Passphrase string
			Message    string
			Aux        string
			Address    string
			PublicKey  string
			Path       string
			Account    uint32
			Index      uint32
			Signed     json.RawMessage
		}
		if err = json.Unmarshal(scanner.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		var a *Account
		if c.Kind == "legacy" {
			a, err = s.FromLegacyPassphrase(ctx, Devnet(), c.Passphrase)
		} else {
			a, err = s.FromPhrase(ctx, Devnet(), c.Phrase, AccountOptions{Account: c.Account, Index: c.Index, Passphrase: c.Passphrase})
		}
		if err != nil {
			t.Fatal(err)
		}
		if a.Address != c.Address || a.PublicKey != c.PublicKey || a.Path != c.Path {
			t.Fatalf("case %d differs", count)
		}
		if c.Kind == "legacy" {
			var signature json.RawMessage
			if err = s.call(ctx, "signMessage", map[string]any{"key": a.handle, "message": hex.EncodeToString([]byte(c.Message)), "aux": c.Aux}, &signature); err != nil {
				t.Fatal(err)
			}
			var expected, actual MessageSignature
			_ = json.Unmarshal(c.Signed, &expected)
			_ = json.Unmarshal(signature, &actual)
			if actual != expected {
				t.Fatalf("signature %d differs", count)
			}
			ok, err := s.VerifyMessage(ctx, []byte(c.Message), expected)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
		}
		if err = a.Release(ctx); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2000 {
		t.Fatalf("expected 2000, got %d", count)
	}
	t.Log("1000 legacy signatures and 1000 phrase accounts match TypeScript")
}
