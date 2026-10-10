package iceroot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDifferential(t *testing.T) {
	native := os.Getenv("SDK_NATIVE_JSON")
	if native == "" {
		t.Skip("run scripts/test-cross-language.sh")
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
	cmd := exec.Command(native)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), maxJSON)
	calls := 0
	compare := func(op string, args map[string]any) json.RawMessage {
		t.Helper()
		args["op"] = op
		request, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = input.Write(append(request, '\n')); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("native response: %v", scanner.Err())
		}
		var expected struct {
			Result json.RawMessage `json:"result"`
			Error  *Error          `json:"error"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &expected); err != nil {
			t.Fatal(err)
		}
		var got json.RawMessage
		actualErr := s.call(ctx, op, args, &got)
		if expected.Error != nil {
			e, ok := actualErr.(*Error)
			if !ok || e.Code != expected.Error.Code || e.Message != expected.Error.Message || !bytes.Equal(e.Details, expected.Error.Details) {
				t.Fatalf("call %d %s: %v != %s", calls, op, actualErr, scanner.Text())
			}
		} else {
			if actualErr != nil || !bytes.Equal(got, expected.Result) {
				t.Fatalf("call %d %s: %s %v != %s", calls, op, got, actualErr, expected.Result)
			}
		}
		calls++
		return got
	}
	rng := rand.New(rand.NewPCG(0x1ce, 0x1234))
	p := Devnet()
	cfg := configuration(t)
	info := compare("chainLoad", map[string]any{"profile": p, "configuration": cfg})
	var ci ChainInfo
	_ = json.Unmarshal(info, &ci)
	p = ci.Profile
	var initial struct {
		Handle    uint32 `json:"handle"`
		PublicKey string `json:"publicKey"`
		Address   string `json:"address"`
	}
	_ = json.Unmarshal(compare("keyLegacy", map[string]any{"profile": p, "passphrase": "differential signer"}), &initial)
	aux := strings.Repeat("42", 32)
	for i := 0; i < 10000; i++ {
		switch i % 6 {
		case 0:
			text := fmt.Sprintf("%d.%08d", rng.Uint64()%100000000000, rng.Uint32()%100000000)
			if i%18 == 0 {
				text += "x"
			}
			compare("amountParse", map[string]any{"text": text, "decimals": uint8(rng.Uint32() % 19)})
		case 1:
			var k struct {
				Handle uint32 `json:"handle"`
			}
			result := compare("keyLegacy", map[string]any{"profile": p, "passphrase": fmt.Sprintf("case %d %x café", i, rng.Uint64())})
			_ = json.Unmarshal(result, &k)
			compare("signMessage", map[string]any{"key": k.Handle, "message": hex.EncodeToString([]byte(fmt.Sprintf("message %x", rng.Uint64()))), "aux": aux})
			compare("keyRelease", map[string]any{"key": k.Handle})
		case 2:
			words := phrase
			if i%24 == 2 {
				words = strings.Replace(words, "art", "abandon", 1)
			}
			result := compare("keyPhrase", map[string]any{"profile": p, "phrase": words, "account": rng.Uint32() % 4, "index": rng.Uint32(), "passphrase": fmt.Sprintf("%x", rng.Uint32())})
			if len(result) > 0 {
				var k struct {
					Handle uint32 `json:"handle"`
				}
				_ = json.Unmarshal(result, &k)
				compare("keyRelease", map[string]any{"key": k.Handle})
			}
		case 3:
			address := initial.Address
			if i%12 == 3 {
				address += "x"
			}
			compare("addressParse", map[string]any{"profile": p, "address": address})
		default:
			var operation Operation
			if i%6 == 4 {
				recipients := make([]Recipient, 1+rng.IntN(256))
				for j := range recipients {
					recipients[j] = Recipient{Address: initial.Address, Amount: Amount(fmt.Sprint(1 + rng.Uint32()))}
				}
				operation = Operation{Kind: "transfer", To: recipients}
			} else {
				entries := make([]VoteEntry, rng.IntN(56))
				for j := range entries {
					entries[j] = VoteEntry{Validator: fmt.Sprintf("genesis_%d", j+1), BasisPoints: uint16(10000 / max(1, len(entries)))}
				}
				operation = Operation{Kind: "vote", Entries: entries}
			}
			request := BuildRequest{Operation: operation, Memo: strings.Repeat("a", rng.IntN(258))}
			result := compare("draftBuild", map[string]any{"profile": p, "configuration": cfg, "request": request, "facts": OnlineFacts{Sender: initial.PublicKey, Nonce: fmt.Sprint(1 + rng.Uint64()%1000000), Height: 2}})
			if len(result) > 0 {
				var d Draft
				_ = json.Unmarshal(result, &d)
				compare("draftSign", map[string]any{"profile": p, "serialized": d.Serialized, "key": initial.Handle, "aux": aux})
			}
		}
	}
	t.Logf("10000 core cases, %d byte-identical calls", calls)
	fixture, err := os.ReadFile(filepath.Join(os.Getenv("SDK_RUST_DIR"), "crates/iceroot-vote/tests/data/synthetic-80.json"))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err = DecodeJSON(fixture, &base); err != nil {
		t.Fatal(err)
	}
	rules := map[string]any{"minEntries": 20, "maxEntries": 53, "maxEntryBasisPoints": 500, "maxBytes": 1280, "names": "lowercase-letters", "validatorsMayVote": false}
	modes := []string{"diversity", "reliability", "maximum-rewards", "support-newcomers"}
	successes := 0
	for i := 0; i < 10000; i++ {
		var snapshot map[string]any
		_ = DecodeJSON(fixture, &snapshot)
		records := snapshot["records"].([]any)
		records = records[:1+rng.IntN(len(records))]
		snapshot["records"] = records
		snapshot["height"] = fmt.Sprint(5000000 + rng.IntN(1000000))
		for _, item := range records {
			r := item.(map[string]any)
			r["rank"] = 1 + rng.IntN(110)
			r["seatedDaysInWindow"] = rng.IntN(31)
			assigned := 1 + rng.IntN(10000)
			r["production"] = map[string]any{"forged": assigned - rng.IntN(assigned/10+1), "assigned": assigned}
			r["payouts"] = map[string]any{"perUnitWeight": fmt.Sprint(1 + rng.Uint64()%100000000000), "intervals": 30}
			r["penalties"] = map[string]any{"jailedInWindow": rng.IntN(50) == 0, "equivocationInWindow": rng.IntN(70) == 0, "ever": rng.IntN(15) == 0}
		}
		snap, _ := json.Marshal(snapshot)
		mode := modes[rng.IntN(4)]
		request, _ := json.Marshal(map[string]any{"mode": mode, "account": fmt.Sprintf("holder-%x", rng.Uint64()), "count": 20 + rng.IntN(34), "draw": rng.Uint32(), "rules": rules})
		result := compare("vote", map[string]any{"operation": "select", "first": string(snap), "second": string(request), "third": ""})
		compare("vote", map[string]any{"operation": "evaluate", "first": string(snap), "second": mode, "third": ""})
		if len(result) > 0 {
			successes++
			compare("vote", map[string]any{"operation": "check", "first": string(result), "second": string(snap), "third": ""})
		}
	}
	if successes == 0 {
		t.Fatal("all random vote requests failed")
	}
	t.Logf("10000 vote snapshots, %d selections, %d total byte-identical calls", successes, calls)
}
