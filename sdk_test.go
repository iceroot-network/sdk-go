package iceroot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const phrase = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"

func testSDK(t testing.TB) *SDK {
	t.Helper()
	s, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}
func TestAccounts(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if a.Address != "dDSccdbPRhfrcbUeFLMbGC1rtnfCsjJcNF" || a.PublicKey != "03f83f83227e28add5598d2c75c20f72b4bfb0328957ef27e2da2ff73779fa4bd2" {
		t.Fatal(a)
	}
	message := []byte("0123456789abcdef0123456789abcdef")
	sig, err := a.SignMessage(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyMessage(ctx, message, sig); err != nil || !ok {
		t.Fatal(ok, err)
	}
	message[0] ^= 1
	if ok, err := s.VerifyMessage(ctx, message, sig); err != nil || ok {
		t.Fatal(ok, err)
	}
	if err = a.Release(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = a.SignMessage(ctx, message)
	var e *Error
	if !errors.As(err, &e) || e.Code != "KeyReleased" {
		t.Fatal(err)
	}
	b, err := s.FromPhrase(ctx, Devnet(), phrase, AccountOptions{Index: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(ctx)
	if b.Path != "m/44'/1'/0'/0'/1'" || b.Legacy {
		t.Fatal(b)
	}
	pool, err := s.AddressPool(ctx, Devnet(), phrase, AccountOptions{}, 3)
	if err != nil || pool[1] != b.Address || pool[0] == pool[1] {
		t.Fatal(pool, err)
	}
	amount, err := s.ParseAmount(ctx, "123456789.12345678", 8)
	if err != nil || amount != "12345678912345678" {
		t.Fatal(amount, err)
	}
	if _, err = s.ParseAmount(ctx, "1.000000001", 8); err == nil {
		t.Fatal("accepted fractional base unit")
	}
	if _, err = s.GeneratePhrase(ctx); err != nil {
		t.Fatal(err)
	}
}
func configuration(t testing.TB) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/configuration.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestDrafts(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	p := Devnet()
	cfg := configuration(t)
	info, err := s.LoadChain(ctx, p, cfg, 2)
	if err != nil {
		t.Fatal(err)
	}
	p = info.Profile
	a, err := s.FromLegacyPassphrase(ctx, p, "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	requests := []BuildRequest{{Operation: Operation{Kind: "transfer", To: []Recipient{{Address: a.Address, Amount: "150000000"}}}, Memo: "withdrawal"}, {Operation: Operation{Kind: "burn", Amount: "2000000"}}, {Operation: Operation{Kind: "vote", Entries: []VoteEntry{{Validator: "genesis_1", BasisPoints: 10000}}}}, {Operation: Operation{Kind: "vote"}}, {Operation: Operation{Kind: "register-validator", Name: "example"}}, {Operation: Operation{Kind: "resign-validator", Resignation: "temporary"}}, {Operation: Operation{Kind: "register-second-key", PublicKey: a.PublicKey}}}
	for _, request := range requests {
		t.Run(request.Operation.Kind, func(t *testing.T) {
			d, err := s.BuildOffline(ctx, p, cfg, request, OnlineFacts{Sender: a.PublicKey, Nonce: "1", Height: 2})
			if err != nil {
				t.Fatal(err)
			}
			data, err := d.Serialize()
			if err != nil {
				t.Fatal(err)
			}
			other := testSDK(t)
			roundtrip, err := other.DeserializeDraft(ctx, p, data)
			if err != nil {
				t.Fatal(err)
			}
			// With the profile alone, a fee at the floor of the configuration the draft carries is
			// unverified, and the floor is kept for display.
			want := d.Summary.Fee
			if want.Source == "floor" {
				want.Source = "unverified"
			}
			if got := roundtrip.Summary.Fee; got.Source != want.Source || got.Amount != want.Amount || fmt.Sprint(amountOrNil(got.Floor)) != fmt.Sprint(amountOrNil(want.Floor)) {
				t.Fatalf("fee %s %s %v, want %s %s %v", got.Amount, got.Source, amountOrNil(got.Floor), want.Amount, want.Source, amountOrNil(want.Floor))
			}
			key, err := other.FromLegacyPassphrase(ctx, p, "probe passphrase")
			if err != nil {
				t.Fatal(err)
			}
			defer key.Release(ctx)
			signed, err := roundtrip.Sign(ctx, p, key, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !signed.Verified || len(signed.ID) != 64 {
				t.Fatal(signed)
			}
			var summary json.RawMessage
			if err = other.call(ctx, "signedRead", map[string]any{"profile": p, "serialized": signed.Serialized}, &summary); err != nil {
				t.Fatal(err)
			}
			bad := p
			bad.Chain.NetworkByte = 30
			if _, err = other.DeserializeDraft(ctx, bad, data); err == nil {
				t.Fatal("accepted wrong network")
			}
		})
	}
}

// amountOrNil is the amount a points to, or nil.
func amountOrNil(a *Amount) any {
	if a == nil {
		return nil
	}
	return *a
}

// transferTo is a request to send amount base units to address.
func transferTo(address string, amount Amount) BuildRequest {
	return BuildRequest{Operation: Operation{Kind: "transfer", To: []Recipient{{Address: address, Amount: amount}}}}
}

// raisedFloor is data, a serialized draft whose fee is ten times the floor, rewritten to say that
// the fee is the floor and to carry a fee table ten times the network's.
func raisedFloor(t *testing.T, data []byte) []byte {
	t.Helper()
	text := string(data)
	tampered := strings.ReplaceAll(strings.Replace(text, `"source":"explicit"`, `"source":"floor"`, 1), `"minFee":6173`, `"minFee":61730`)
	if tampered == text || strings.Contains(tampered, `"minFee":6173,`) || !strings.Contains(tampered, `"source":"floor"`) {
		t.Fatalf("the serialized form changed its layout: %s", text)
	}
	return []byte(tampered)
}

// tenfold is ten times amount.
func tenfold(t *testing.T, amount Amount) Amount {
	t.Helper()
	value, err := strconv.ParseUint(string(amount), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return Amount(strconv.FormatUint(value*10, 10))
}

func TestADraftsOwnFeeTableNeverMakesItsFeeTheFloor(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	cfg := configuration(t)
	info, err := s.LoadChain(ctx, Devnet(), cfg, 2)
	if err != nil {
		t.Fatal(err)
	}
	p := info.Profile
	a, err := s.FromLegacyPassphrase(ctx, p, "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	facts := OnlineFacts{Sender: a.PublicKey, Nonce: "1", Height: 2}
	floor, err := s.BuildOffline(ctx, p, cfg, transferTo(a.Address, "1"), facts)
	if err != nil || floor.Summary.Fee.Source != "floor" {
		t.Fatal(floor.Summary.Fee, err)
	}
	// A fee ten times the floor, which the form then calls the floor of a fee table of its own.
	request := transferTo(a.Address, "1")
	raised := tenfold(t, floor.Summary.Fee.Amount)
	request.Fee = &FeeChoice{Kind: "exact", Amount: raised}
	built, err := s.BuildOffline(ctx, p, cfg, request, facts)
	if err != nil || built.Summary.Fee.Source != "explicit" {
		t.Fatal(built.Summary.Fee, err)
	}
	data, err := built.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.DeserializeDraft(ctx, p, raisedFloor(t, data))
	if err != nil {
		t.Fatal(err)
	}
	// The fee is the transaction's own; the floor of the form's fee table is kept for display
	// only, and the fee is never called the floor.
	fee := read.Summary.Fee
	if fee.Source != "unverified" || fee.Amount != raised || fee.Floor == nil || *fee.Floor != raised {
		t.Fatalf("fee %s %s, floor %v", fee.Amount, fee.Source, amountOrNil(fee.Floor))
	}
	// Untouched, the raised fee reads as the explicit fee it is.
	if read, err = s.DeserializeDraft(ctx, p, data); err != nil || read.Summary.Fee.Source != "explicit" {
		t.Fatal(read.Summary.Fee, err)
	}
}

func TestMemoryAndConcurrency(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	secret := "a unique phrase buffer for the memory erasure check"
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Release(ctx); err != nil {
		t.Fatal(err)
	}
	memory, ok := s.module.Memory().Read(0, s.module.Memory().Size())
	secretKey := sha256.Sum256([]byte(secret))
	if !ok || bytes.Contains(memory, []byte(secret)) || bytes.Contains(memory, secretKey[:]) {
		t.Fatal("input secret remained in module memory")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ParseAmount(ctx, "1.5", 8); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ParseAmount(cancelled, "1", 8); err == nil {
		t.Fatal("cancelled call succeeded")
	}
	_ = s.Close(ctx)
	if _, err := s.ParseAmount(ctx, "1", 8); err == nil {
		t.Fatal("closed runtime succeeded")
	}
}
func TestAVoteReasonEscapesRunsOfSpacesInDeclaredNames(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	// 25 seated validators that declare one operator and one hosting provider, whose names have
	// runs of spaces.
	records := make([]map[string]any, 25)
	for i := range records {
		records[i] = map[string]any{
			"name": "node" + string(rune('a'+i)), "address": fmt.Sprintf("addr-%d", i), "rank": 1 + i, "seated": true, "status": "active",
			"registeredHeight": "1", "seatedDaysInWindow": 30, "voteWeight": "1000", "voters": 1,
			"production":   map[string]any{"forged": 100, "assigned": 100},
			"penalties":    map[string]any{"jailedInWindow": false, "equivocationInWindow": false, "ever": false},
			"declarations": map[string]any{"operator": "Frost   line", "hosting": "Metal  box", "country": "NL", "complete": true},
			"payouts":      map[string]any{"perUnitWeight": "10", "intervals": 30}, "selfFundedWeightBp": 0,
		}
	}
	snapshot, err := json.Marshal(map[string]any{"height": "5000000", "windowDays": 30, "seats": 53, "blockTimeSeconds": 8, "source": "indexer", "records": records})
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]any{"minEntries": 20, "maxEntries": 53, "maxEntryBasisPoints": 500, "maxBytes": 1280, "names": "lowercase-letters", "validatorsMayVote": false}
	request, err := json.Marshal(map[string]any{"mode": "diversity", "account": "holder", "count": 20, "draw": 1, "rules": rules})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Vote(ctx, "select", string(snapshot), string(request), "")
	if err != nil {
		t.Fatal(err)
	}
	var selection struct {
		Entries []struct {
			Reasons []struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
				Text  string `json:"text"`
			} `json:"reasons"`
		} `json:"entries"`
	}
	if err = json.Unmarshal(result, &selection); err != nil {
		t.Fatal(err)
	}
	// The value keeps the name as declared; the sentence escapes each space of a run.
	quoted := map[string]string{"Frost   line": `"Frost\u{20}\u{20}\u{20}line"`, "Metal  box": `"Metal\u{20}\u{20}box"`}
	seen := map[string]bool{}
	for _, entry := range selection.Entries {
		for _, reason := range entry.Reasons {
			want, declared := quoted[reason.Value]
			if reason.Kind != "group" || !declared {
				continue
			}
			seen[reason.Value] = true
			if !strings.Contains(reason.Text, want) || strings.Contains(reason.Text, "  ") {
				t.Fatalf("%q %q", reason.Value, reason.Text)
			}
		}
	}
	if len(seen) != len(quoted) {
		t.Fatal("no pick shares the operator or the hosting provider", string(result))
	}
}
func TestFinalityIsNotConfirmations(t *testing.T) {
	s := testSDK(t)
	n := &Network{sdk: s, profile: Devnet()}
	_, err := n.WaitFinal(context.Background(), "id")
	var e *Error
	if !errors.As(err, &e) || e.Code != "UnsupportedOnNetwork" {
		t.Fatal(err)
	}
}
func BenchmarkSignMessage(b *testing.B) {
	s := testSDK(b)
	ctx := context.Background()
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	defer a.Release(ctx)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err = a.SignMessage(ctx, []byte("withdrawal approval")); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkSignTransaction(b *testing.B) {
	s := testSDK(b)
	ctx := context.Background()
	cfg := configuration(b)
	info, err := s.LoadChain(ctx, Devnet(), cfg, 2)
	if err != nil {
		b.Fatal(err)
	}
	a, err := s.FromLegacyPassphrase(ctx, info.Profile, "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	defer a.Release(ctx)
	d, err := s.BuildOffline(ctx, info.Profile, cfg, BuildRequest{Operation: Operation{Kind: "transfer", To: []Recipient{{Address: a.Address, Amount: "1"}}}}, OnlineFacts{Sender: a.PublicKey, Nonce: "1", Height: 2})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err = d.Sign(ctx, info.Profile, a, nil); err != nil {
			b.Fatal(err)
		}
	}
}
func TestCancelledGate(t *testing.T) {
	s := testSDK(t)
	<-s.gate
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := s.ParseAmount(ctx, "1", 8); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	s.gate <- struct{}{}
}

func TestKeystoreBoundary(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	phraseBytes, password := []byte(phrase), []byte("keystore password")
	stored, err := s.EncryptKeystore(ctx, phraseBytes, password, KeystoreParams{MemoryKib: 19456, Iterations: 2, Parallelism: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(phraseBytes, make([]byte, len(phraseBytes))) || !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("input buffers not wiped")
	}
	wrong := []byte("wrong password")
	if _, err = s.FromKeystore(ctx, Devnet(), stored, wrong, AccountOptions{}, 65536); err == nil {
		t.Fatal("wrong password accepted")
	}
	key, err := s.FromKeystore(ctx, Devnet(), stored, []byte("keystore password"), AccountOptions{}, 65536)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := s.FromPhrase(ctx, Devnet(), phrase, AccountOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if key.Address != direct.Address {
		t.Fatal("keystore derived another account")
	}
	_ = key.Release(ctx)
	_ = direct.Release(ctx)
	params := KeystoreParams{MemoryKib: 19456, Iterations: 3, Parallelism: 1}
	changed, err := s.ChangeKeystorePassword(ctx, stored, []byte("keystore password"), []byte("new password"), params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FromKeystore(ctx, Devnet(), changed, []byte("keystore password"), AccountOptions{}, 65536); err == nil {
		t.Fatal("old password opened the changed keystore")
	}
	again, err := s.ReencryptKeystore(ctx, changed, []byte("new password"), params)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := s.FromKeystore(ctx, Devnet(), again, []byte("new password"), AccountOptions{}, 65536)
	if err != nil || reopened.Address != direct.Address {
		t.Fatal("re-encrypted keystore opened another account", err)
	}
	_ = reopened.Release(ctx)
}
func TestProductionRefusesFixedRandomness(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "fixed randomness test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	err = s.call(ctx, "signMessage", map[string]any{"key": a.handle, "message": "00", "aux": "42"}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != "InvalidArgument" {
		t.Fatal(err)
	}
}

func TestReleasedKeysLeaveNoMaterialAfterMapChanges(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	accounts := make([]*Account, 32)
	secrets := make([][32]byte, 32)
	for i := range accounts {
		text := fmt.Sprintf("memory key %d", i)
		secrets[i] = sha256.Sum256([]byte(text))
		a, err := s.FromLegacyPassphrase(ctx, Devnet(), text)
		if err != nil {
			t.Fatal(err)
		}
		accounts[i] = a
	}
	for i := len(accounts) - 1; i >= 0; i-- {
		if err := accounts[i].Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	generated, err := s.GeneratePhrase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	memory, ok := s.module.Memory().Read(0, s.module.Memory().Size())
	if !ok {
		t.Fatal("no memory")
	}
	for _, secret := range secrets {
		if bytes.Contains(memory, secret[:]) {
			t.Fatal("released secret remained in module memory")
		}
	}
	if bytes.Contains(memory, []byte(generated)) {
		t.Fatal("returned phrase remained in module memory")
	}
}
func TestInterruptedCoreClosesAndWipes(t *testing.T) {
	s := testSDK(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := s.EncryptKeystore(ctx, []byte(phrase), []byte("cancelled password"), DefaultKeystoreParams())
	if err == nil {
		t.Fatal("expensive call escaped deadline")
	}
	if !s.module.IsClosed() {
		t.Fatal("interrupted module remained callable")
	}
	if memory, ok := s.module.Memory().Read(0, s.module.Memory().Size()); ok {
		for _, b := range memory {
			if b != 0 {
				t.Fatal("interrupted module was not wiped")
			}
		}
	}
}

// A phrase and a password given as bytes leave no copy in the module's memory, in either their
// text or the hex form they cross the boundary in, and the caller's slices are wiped.
func TestSecretBytesLeaveNoCopies(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	recovery, err := s.GeneratePhrase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	password := "a password that must not stay in memory"
	input := []byte(recovery)
	a, err := s.FromPhraseBytes(ctx, Devnet(), input, AccountOptions{Index: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, make([]byte, len(input))) {
		t.Fatal("phrase bytes not wiped")
	}
	direct, err := s.FromPhrase(ctx, Devnet(), recovery, AccountOptions{Index: 3})
	if err != nil || direct.Address != a.Address {
		t.Fatal("phrase bytes derived another account", err)
	}
	stored, err := s.EncryptKeystore(ctx, []byte(recovery), []byte(password), KeystoreParams{MemoryKib: 19456, Iterations: 2, Parallelism: 1})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.FromKeystore(ctx, Devnet(), stored, []byte(password), AccountOptions{Index: 3}, 65536)
	if err != nil || opened.Address != a.Address {
		t.Fatal("keystore opened another account", err)
	}
	for _, account := range []*Account{a, direct, opened} {
		if err = account.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	memory, ok := s.module.Memory().Read(0, s.module.Memory().Size())
	if !ok {
		t.Fatal("no memory")
	}
	for _, needle := range []string{recovery, hex.EncodeToString([]byte(recovery)), password, hex.EncodeToString([]byte(password))} {
		if bytes.Contains(memory, []byte(needle)) {
			t.Fatalf("a secret remained in module memory: %.12s...", needle)
		}
	}
}
