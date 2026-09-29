package iceroot

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// answerLimit is the core's limit on the answer a relay may send (8 MiB).
const answerLimit = 8 << 20

// testRelay is a relay of the devnet fixtures. answer, when it returns true, has answered a request
// in place of the fixtures. The paths the relay was asked for are kept in order.
type testRelay struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	answer func(w http.ResponseWriter, r *http.Request) bool
}

func newTestRelay(t *testing.T, answer func(w http.ResponseWriter, r *http.Request) bool) *testRelay {
	t.Helper()
	relay := &testRelay{answer: answer}
	relay.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relay.mu.Lock()
		relay.paths = append(relay.paths, r.URL.Path)
		relay.mu.Unlock()
		if relay.answer != nil && relay.answer(w, r) {
			return
		}
		file := map[string]string{
			"/api/node/configuration/crypto": "node-configuration-crypto.json",
			"/api/node/configuration":        "node-configuration.json",
			"/api/node/status":               "node-status.json",
		}[r.URL.Path]
		if file == "" {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile("testdata/node/" + file)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	t.Cleanup(relay.Close)
	return relay
}

// asked returns how many times the relay was asked for path.
func (r *testRelay) asked(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path {
			n++
		}
	}
	return n
}

// paddedStatus is a node status answer of exactly size bytes.
func paddedStatus(size int) []byte {
	head := `{"data":{"synced":true,"now":80,"blocksCount":0,"timestamp":634},"pad":"`
	return []byte(head + strings.Repeat("x", size-len(head)-2) + `"}`)
}

func errorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAnswerLimit(t *testing.T) {
	s := testSDK(t)
	var mode atomic.Value
	mode.Store("")
	relay := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/node/status" {
			return false
		}
		switch mode.Load().(string) {
		case "declared":
			// Declares far more than the limit, sends one byte and waits: a client that reads
			// on because of the declared length never finishes.
			w.Header().Set("Content-Length", strconv.Itoa(1<<30))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "streamed":
			// No declared length: the body arrives in chunks and is one byte too long.
			body := paddedStatus(answerLimit + 1)
			for len(body) > 0 {
				n := min(len(body), 1<<20)
				_, _ = w.Write(body[:n])
				w.(http.Flusher).Flush()
				body = body[n:]
			}
		case "exact":
			_, _ = w.Write(paddedStatus(answerLimit))
		default:
			return false
		}
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(relay.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"declared", "streamed"} {
		mode.Store(m)
		started := time.Now()
		_, err := net.Status(ctx)
		if code := errorCode(err); code != "NodeUnavailable" {
			t.Fatalf("%s: %v", m, err)
		}
		if strings.Contains(err.Error(), "xxxx") {
			t.Fatalf("%s: the answer is in the error: %.100s", m, err)
		}
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("%s: took %s", m, elapsed)
		}
	}
	// An answer of exactly the limit is read and decoded.
	mode.Store("exact")
	status, err := net.Status(ctx)
	if err != nil || status.Height != "80" {
		t.Fatal(status, err)
	}
}

func TestAnswersAreReadAsSent(t *testing.T) {
	s := testSDK(t)
	// A node status, sent compressed whatever the client asks for.
	status, err := os.ReadFile("testdata/node/node-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	_, _ = gz.Write(status)
	_ = gz.Close()
	var encodings sync.Map
	var compressed atomic.Bool
	relay := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		encodings.Store(r.Header.Get("Accept-Encoding"), true)
		if r.URL.Path != "/api/node/status" || !compressed.Load() {
			return false
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(zipped.Bytes())
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The SDK's own client, and an application's client with Go's default transport, which
	// decompresses unless the request names its own Accept-Encoding.
	for _, client := range []HTTPDoer{nil, &http.Client{}} {
		compressed.Store(false)
		net, err := s.Connect(ctx, Devnet(relay.URL+"/api"), ConnectOptions{HTTP: client})
		if err != nil {
			t.Fatal(err)
		}
		compressed.Store(true)
		// The compressed bytes reach the core as they were sent, and are not a node status.
		if _, err = net.Status(ctx); errorCode(err) != "BadResponse" {
			t.Fatalf("client %T: %v", client, err)
		}
	}
	encodings.Range(func(key, _ any) bool {
		if key != "identity" {
			t.Errorf("a request asked for Accept-Encoding %q", key)
		}
		return true
	})
}
