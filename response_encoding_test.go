package iceroot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// withText is answer, a JSON object, with text in a member the core does not read.
func withText(answer []byte, text string) []byte {
	return append([]byte(`{"extra":"`+text+`",`), answer[1:]...)
}

// The core reads a node's answer as UTF-8 text and refuses one that is not; JSON would carry such
// bytes to it as U+FFFD. The transport refuses the answer as the core does, with BadResponse, and
// asks no other relay: the relay answered.
func TestASuccessfulAnswerThatIsNotUTF8IsRefused(t *testing.T) {
	s := testSDK(t)
	answer, err := os.ReadFile("testdata/node/node-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var text atomic.Value
	text.Store("")
	first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/node/status" || text.Load() == "" {
			return false
		}
		_, _ = w.Write(withText(answer, text.Load().(string)))
		return true
	})
	second := newTestRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", second.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	net.spacing = 0
	// A byte that starts no character, an overlong form, an encoded surrogate, a cut sequence and a
	// value above U+10FFFF.
	for _, bad := range []string{"\xff", "\xc0\xaf", "\xed\xa0\x80", "\xe2\x82", "\xf4\x90\x80\x80"} {
		text.Store(bad)
		_, err := net.Status(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Code != "BadResponse" || string(e.Details) != `{"reason":"HTTP 200: not UTF-8"}` {
			t.Fatalf("%+q: %v", bad, err)
		}
	}
	if asked := second.requests(); len(asked) != 0 {
		t.Fatal("another relay was asked", asked)
	}
	// UTF-8 text is read, the replacement character itself included.
	for _, good := range []string{"\uFFFD", "snow 雪", "\u2028"} {
		text.Store(good)
		if status, err := net.Status(ctx); err != nil || status.Height != "80" {
			t.Fatalf("%+q: %v %v", good, status, err)
		}
	}
}

// Connecting stops at a relay whose answer is not UTF-8, as at any answer the core refuses.
func TestConnectRefusesAnAnswerThatIsNotUTF8(t *testing.T) {
	s := testSDK(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for path, file := range map[string]string{
		"/api/node/configuration/crypto": "node-configuration-crypto.json",
		"/api/node/configuration":        "node-configuration.json",
	} {
		answer, err := os.ReadFile("testdata/node/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var text atomic.Value
		first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != path {
				return false
			}
			_, _ = w.Write(withText(answer, text.Load().(string)))
			return true
		})
		second := newTestRelay(t, nil)
		profile := Devnet(first.URL+"/api", second.URL+"/api")
		// The member is not read: as UTF-8, the answer is used.
		text.Store("snow 雪")
		if _, err = s.Connect(ctx, profile, ConnectOptions{}); err != nil {
			t.Fatal(path, err)
		}
		text.Store("\xff")
		if _, err = s.Connect(ctx, profile, ConnectOptions{}); errorCode(err) != "BadResponse" {
			t.Fatal(path, err)
		}
		if asked := second.requests(); len(asked) != 0 {
			t.Fatal(path, "another relay was asked", asked)
		}
	}
}

// An error status keeps its meaning when its body is not UTF-8. The core reads no text from such a
// body, so the error carries none, as the Rust and TypeScript clients' errors do, and a server
// error still moves the request to the next relay.
func TestAnErrorWhoseBodyIsNotUTF8KeepsItsStatus(t *testing.T) {
	s := testSDK(t)
	var status atomic.Int32
	first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		code := int(status.Load())
		if code == 0 || r.URL.Path != "/api/node/status" {
			return false
		}
		w.Header().Set("Retry-After", "4000000000")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"statusCode":` + strconv.Itoa(code) + `,"error":"` + "\xff" + `","message":"` + "\xff" + `"}`))
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	only, err := s.Connect(ctx, Devnet(first.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	only.spacing = 0
	for _, c := range []struct {
		status  int32
		code    string
		details string
	}{
		{404, "NotFound", `{"message":""}`},
		{422, "Refused", `{"message":"","status":422}`},
		{429, "RateLimited", `{"retryAfterSeconds":4000000000}`},
		{503, "Refused", `{"message":"","status":503}`},
	} {
		status.Store(c.status)
		_, err := only.Status(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Code != c.code {
			t.Fatalf("HTTP %d: %v", c.status, err)
		}
		// The details, with their keys sorted.
		var details map[string]any
		if err = json.Unmarshal(e.Details, &details); err != nil {
			t.Fatal(err)
		}
		if sorted, _ := json.Marshal(details); string(sorted) != c.details {
			t.Fatalf("HTTP %d: details %s", c.status, e.Details)
		}
	}
	status.Store(0)
	second := newTestRelay(t, nil)
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", second.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	status.Store(503)
	if answer, err := net.Status(ctx); err != nil || answer.Height != "80" || second.asked("/api/node/status") != 1 {
		t.Fatal(answer, err, second.requests())
	}
}
