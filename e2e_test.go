package iceroot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCustodianDevnet(t *testing.T) {
	relay := os.Getenv("ICEROOT_E2E_RELAY")
	if relay == "" {
		t.Skip("run under the Rust SDK devnet harness")
	}
	data, err := os.ReadFile(os.Getenv("ICEROOT_E2E_WALLETS"))
	if err != nil {
		t.Fatal(err)
	}
	var wallets struct {
		Genesis []struct {
			Label      string `json:"label"`
			Passphrase string `json:"passphrase"`
		} `json:"genesis"`
	}
	if err = json.Unmarshal(data, &wallets); err != nil {
		t.Fatal(err)
	}
	passphrase := ""
	for _, w := range wallets.Genesis {
		if w.Label == "genesis-1" {
			passphrase = w.Passphrase
		}
	}
	if passphrase == "" {
		t.Fatal("missing genesis account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	s := testSDK(t)
	net, err := s.Connect(ctx, Devnet(relay), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	funder, err := s.FromLegacyPassphrase(ctx, net.Profile(), passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer funder.Release(context.Background())
	recovery, err := s.GeneratePhrase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := s.AddressPool(ctx, net.Profile(), recovery, AccountOptions{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := net.Build(ctx, funder.PublicKey, BuildRequest{Operation: Operation{Kind: "transfer", To: []Recipient{{Address: pool[0], Amount: "1000000000"}}}})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := draft.Sign(ctx, net.Profile(), funder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = net.Submit(ctx, signed); err != nil {
		t.Fatal(err)
	}
	if _, err = net.WaitConfirmed(ctx, signed.ID); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "go", "run", "./examples/custodian", "-relay", relay, "-to", pool[1], "-amount", "100000000", "-confirmed", "-count", "2")
	command.Env = append(os.Environ(), "ICEROOT_PHRASE="+recovery)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("custodian example: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `"status":"confirmed"`) || !strings.Contains(string(output), `"finalized":false`) {
		t.Fatalf("unexpected outcome: %s", output)
	}
	recipient, err := net.Account(ctx, pool[1])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range recipient.Balances {
		if b.Asset == "ROOT" && b.Amount == "100000000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("withdrawal balance: %+v", recipient)
	}
	t.Log("offline address pool, funded account, isolated signing, withdrawal submission and inclusion passed; finality unavailable")
}
