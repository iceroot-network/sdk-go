package iceroot

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkWithdrawal(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	a, err := s.FromLegacyPassphrase(ctx, Devnet(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	var submitted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Test-Transport") != "present" {
			t.Error("missing configured header")
		}
		var file string
		switch r.URL.Path {
		case "/api/node/configuration/crypto":
			file = "node-configuration-crypto.json"
		case "/api/node/configuration":
			file = "node-configuration.json"
		case "/api/node/status":
			file = "node-status.json"
		case "/api/wallets/" + a.Address:
			fmt.Fprintf(w, `{"data":{"address":%q,"publicKey":%q,"balance":"1000000000","nonce":"4","attributes":{},"votingFor":{}}}`, a.Address, a.PublicKey)
			return
		case "/api/transactions":
			if r.Method != "POST" {
				t.Error(r.Method)
			}
			var body struct {
				Transactions []struct {
					ID    string `json:"id"`
					Nonce string `json:"nonce"`
				} `json:"transactions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if len(body.Transactions) != 1 || body.Transactions[0].Nonce != "5" {
				t.Errorf("wrong submission %+v", body)
			}
			submitted.Store(true)
			fmt.Fprintf(w, `{"data":{"accept":[%q],"broadcast":[],"excess":[],"invalid":[]}}`, body.Transactions[0].ID)
			return
		default:
			if strings.HasPrefix(r.URL.Path, "/api/transactions/") {
				file = "transaction-transfer.json"
			} else {
				t.Error(r.URL.Path)
				http.NotFound(w, r)
				return
			}
		}
		data, err := os.ReadFile("testdata/node/" + file)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/transactions/") {
			var value map[string]any
			_ = json.Unmarshal(data, &value)
			value["data"].(map[string]any)["id"] = strings.TrimPrefix(r.URL.Path, "/api/transactions/")
			data, _ = json.Marshal(value)
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	net, err := s.Connect(ctx, Devnet(server.URL+"/api"), ConnectOptions{Headers: http.Header{"X-Test-Transport": []string{"present"}}})
	if err != nil {
		t.Fatal(err)
	}
	// The node is at height 80: the facts are those of block 81, whose milestone has donations,
	// not those of the first block.
	next, err := s.LoadChain(ctx, net.Profile(), net.configuration, 81)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.LoadChain(ctx, net.Profile(), net.configuration, 1)
	if err != nil {
		t.Fatal(err)
	}
	if info := net.Info(); !bytes.Equal(info.Economics, next.Economics) || bytes.Equal(info.Economics, first.Economics) {
		t.Fatalf("economics not those of the next block: %s", info.Economics)
	}
	draft, err := net.Build(ctx, a.PublicKey, BuildRequest{Operation: Operation{Kind: "transfer", To: []Recipient{{Address: a.Address, Amount: "100"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Summary.Nonce != "5" || draft.Summary.Fee.Source != "floor" {
		t.Fatal(draft.Summary)
	}
	signed, err := draft.Sign(ctx, net.Profile(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := net.Submit(ctx, signed)
	if err != nil || !submitted.Load() || !strings.Contains(string(report), signed.ID) {
		t.Fatal(string(report), err)
	}
	tx, err := net.WaitConfirmed(ctx, signed.ID)
	if err != nil || tx == nil || tx.Finalized || tx.Details.Kind != "transfer" || len(tx.Details.Recipients) == 0 {
		t.Fatal(tx, err)
	}
	_, err = net.WaitFinal(ctx, signed.ID)
	var e *Error
	if !errors.As(err, &e) || e.Code != "UnsupportedOnNetwork" {
		t.Fatal(err)
	}
	wrong := Devnet(server.URL + "/api")
	wrong.Chain.Nethash = strings.Repeat("ab", 32)
	if _, err = s.Connect(ctx, wrong, ConnectOptions{Headers: http.Header{"X-Test-Transport": []string{"present"}}}); err == nil {
		t.Fatal("accepted wrong chain")
	}
}
func TestTransportCancellation(t *testing.T) {
	s := testSDK(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Connect(ctx, Devnet(server.URL), ConnectOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestOversizedResponseAndRateLimitCancellation(t *testing.T) {
	s := testSDK(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			_, _ = w.Write([]byte(strings.Repeat("x", answerLimit+1)))
			return
		}
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(429)
	}))
	defer server.Close()
	n, err := s.newNetwork(context.Background(), Devnet(server.URL), ConnectOptions{HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = n.send(context.Background(), nodeRequest{Method: "GET", Target: "/large"})
	var e *Error
	if !errors.As(err, &e) || e.Code != "NodeUnavailable" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = n.send(ctx, nodeRequest{Method: "GET", Target: "/limited"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestADraftIsReadOnTheConnectionsChain(t *testing.T) {
	ctx := context.Background()
	s := testSDK(t)
	relay := newTestRelay(t, nil)
	net, err := s.Connect(ctx, Devnet(relay.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The key is held by another runtime, as an isolated signer's is.
	offline := testSDK(t)
	a, err := offline.FromLegacyPassphrase(ctx, net.Profile(), "probe passphrase")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	facts := OnlineFacts{Sender: a.PublicKey, Nonce: "1", Height: 81}
	built, err := s.BuildOffline(ctx, net.Profile(), net.configuration, transferTo(a.Address, "1"), facts)
	if err != nil || built.Summary.Fee.Source != "floor" {
		t.Fatal(built.Summary.Fee, err)
	}
	data, err := built.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// With the profile alone the floor is unverified; on the connection's chain it is the floor.
	if read, err := offline.DeserializeDraft(ctx, net.Profile(), data); err != nil || read.Summary.Fee.Source != "unverified" {
		t.Fatal(read.Summary.Fee, err)
	}
	read, err := net.DeserializeDraft(ctx, data)
	if err != nil || read.Summary.Fee.Source != "floor" || read.Summary.Fee.Amount != built.Summary.Fee.Amount || *read.Summary.Fee.Floor != *built.Summary.Fee.Floor {
		t.Fatal(read.Summary.Fee, err)
	}
	signed, err := net.SignDraft(ctx, read, a, nil)
	if err != nil || !signed.Verified {
		t.Fatal(signed, err)
	}

	// A draft that carries a fee table of its own is refused on the connection's chain, to read
	// or to sign.
	request := transferTo(a.Address, "1")
	request.Fee = &FeeChoice{Kind: "exact", Amount: tenfold(t, built.Summary.Fee.Amount)}
	raised, err := s.BuildOffline(ctx, net.Profile(), net.configuration, request, facts)
	if err != nil {
		t.Fatal(err)
	}
	if data, err = raised.Serialize(); err != nil {
		t.Fatal(err)
	}
	tampered := raisedFloor(t, data)
	refused := func(err error) {
		t.Helper()
		var e *Error
		if !errors.As(err, &e) || e.Code != "NetworkMismatch" {
			t.Fatal(err)
		}
		var details struct {
			Reason string `json:"reason"`
		}
		if err = json.Unmarshal(e.Details, &details); err != nil || details.Reason != "configuration" {
			t.Fatal(string(e.Details), err)
		}
	}
	_, err = net.DeserializeDraft(ctx, tampered)
	refused(err)
	_, err = net.SignDraft(ctx, Draft{Serialized: hex.EncodeToString(tampered)}, a, nil)
	refused(err)
	if _, err = net.SignDraft(ctx, read, nil, nil); errorCode(err) != "InvalidKey" {
		t.Fatal(err)
	}
}
