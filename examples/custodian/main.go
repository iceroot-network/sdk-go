// The custodian example derives an address pool offline, builds a withdrawal using node facts,
// signs its serialized draft in an isolated runtime, submits it and waits for finality.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	sdk "github.com/iceroot-network/sdk-go"
	"os"
	"time"
)

func run() error {
	relay := flag.String("relay", "", "relay URL including /api; omit to derive addresses offline")
	recipient := flag.String("to", "", "withdrawal recipient")
	amount := flag.String("amount", "", "withdrawal amount in base units")
	confirmed := flag.Bool("confirmed", false, "classical devnet demonstration: wait for inclusion only")
	count := flag.Uint("count", 3, "offline address pool size, at most 10000")
	flag.Parse()
	if *count > 10000 {
		return fmt.Errorf("count exceeds 10000")
	}
	phrase := os.Getenv("ICEROOT_PHRASE")
	if phrase == "" {
		return fmt.Errorf("set ICEROOT_PHRASE to an existing recovery phrase")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	offline, err := sdk.New(ctx)
	if err != nil {
		return err
	}
	defer offline.Close(context.Background())
	p := sdk.Devnet()
	addresses, err := offline.AddressPool(ctx, p, phrase, sdk.AccountOptions{}, uint32(*count))
	if err != nil {
		return err
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"addresses": addresses}); err != nil {
		return err
	}
	if *relay == "" {
		return nil
	}
	if *recipient == "" || *amount == "" {
		return fmt.Errorf("a withdrawal requires -to and -amount")
	}
	online, err := sdk.New(ctx)
	if err != nil {
		return err
	}
	defer online.Close(context.Background())
	net, err := online.Connect(ctx, sdk.Devnet(*relay), sdk.ConnectOptions{})
	if err != nil {
		return err
	}
	p = net.Profile()
	if !*confirmed {
		caps, err := online.Capabilities(ctx, p)
		if err != nil {
			return err
		}
		hasFinality := false
		for _, c := range caps {
			if c == "finality" {
				hasFinality = true
			}
		}
		if !hasFinality {
			return fmt.Errorf("network has no finality; use -confirmed only for a classical devnet demonstration")
		}
	}
	signer, err := offline.FromPhrase(ctx, p, phrase, sdk.AccountOptions{})
	if err != nil {
		return err
	}
	defer signer.Release(context.Background())
	draft, err := net.Build(ctx, signer.PublicKey, sdk.BuildRequest{Operation: sdk.Operation{Kind: "transfer", To: []sdk.Recipient{{Address: *recipient, Amount: sdk.Amount(*amount)}}}, Memo: "custodian withdrawal"})
	if err != nil {
		return err
	}
	serialized, err := draft.Serialize()
	if err != nil {
		return err
	}
	portable, err := offline.DeserializeDraft(ctx, p, serialized)
	if err != nil {
		return err
	}
	// A service applies its approval policy to this exact summary before it signs.
	if err = json.NewEncoder(os.Stdout).Encode(portable.Summary); err != nil {
		return err
	}
	signed, err := portable.Sign(ctx, p, signer, nil)
	if err != nil {
		return err
	}
	report, err := net.Submit(ctx, signed)
	if err != nil {
		return err
	}
	fmt.Println(string(report))
	var outcomes sdk.SubmitReport
	if err = json.Unmarshal(report, &outcomes); err != nil {
		return err
	}
	if len(outcomes.Outcomes) != 1 || outcomes.Outcomes[0].Status != "accepted" {
		return fmt.Errorf("withdrawal was not accepted: %s", report)
	}
	var tx *sdk.Transaction
	if *confirmed {
		tx, err = net.WaitConfirmed(ctx, signed.ID)
	} else {
		tx, err = net.WaitFinal(ctx, signed.ID)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(tx)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
