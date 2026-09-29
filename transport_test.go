package iceroot

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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

// requests returns the paths the relay was asked for, in order.
func (r *testRelay) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// asked returns how many times the relay was asked for path.
func (r *testRelay) asked(path string) int {
	n := 0
	for _, p := range r.requests() {
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

// rateLimitedRelay is a relay of the fixtures that, while after is not empty, answers a node
// status request with HTTP 429 and that Retry-After ("none" sends no Retry-After).
func rateLimitedRelay(t *testing.T, after *atomic.Value) *testRelay {
	after.Store("")
	return newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		value := after.Load().(string)
		if value == "" || r.URL.Path != "/api/node/status" {
			return false
		}
		if value != "none" {
			w.Header().Set("Retry-After", value)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
}

func TestRetryAfterLongerThanAMinuteMovesToTheNextRelay(t *testing.T) {
	s := testSDK(t)
	var after atomic.Value
	first := rateLimitedRelay(t, &after)
	second := newTestRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", second.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if asked := second.requests(); len(asked) != 0 {
		t.Fatal("the second relay was asked while connecting", asked)
	}
	for _, value := range []string{"61", " 4000000000 ", "+61", "18446744073709551615"} {
		after.Store(value)
		started := time.Now()
		status, err := net.Status(ctx)
		if err != nil || status.Height != "80" {
			t.Fatalf("Retry-After %q: %v %v", value, status, err)
		}
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("Retry-After %q: took %s", value, elapsed)
		}
	}
	// The second relay's chain was checked once, before its first answer was used.
	if asked := second.requests(); asked[0] != "/api/node/configuration" || second.asked("/api/node/configuration") != 1 || second.asked("/api/node/status") != 4 {
		t.Fatal(asked)
	}
}

func TestRateLimitedByTheLastRelay(t *testing.T) {
	s := testSDK(t)
	var after atomic.Value
	relay := rateLimitedRelay(t, &after)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(relay.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after.Store("4000000000")
	_, err = net.Status(ctx)
	var got *Error
	if !errors.As(err, &got) {
		t.Fatal(err)
	}
	// The error is the one the core gives for that answer.
	want := s.call(ctx, "apiDecode", map[string]any{"seats": 53, "operation": "nodeStatus", "args": map[string]any{},
		"status": 429, "headers": [][2]string{{"Retry-After", "4000000000"}}, "body": "{}"}, nil)
	var e *Error
	if !errors.As(want, &e) || e.Code != "RateLimited" || got.Code != e.Code || got.Message != e.Message || string(got.Details) != string(e.Details) {
		t.Fatalf("got %v %s, want %v", got, got.Details, want)
	}
}

func TestWaitsAfterHTTP429AreTheCoreBackoff(t *testing.T) {
	s := testSDK(t)
	var limited atomic.Int32
	var after atomic.Value
	after.Store("")
	first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/node/status" || limited.Load() == 0 {
			return false
		}
		limited.Add(-1)
		if value := after.Load().(string); value != "" {
			w.Header().Set("Retry-After", value)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	second := newTestRelay(t, nil)
	ctx := context.Background()
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", second.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	net.spacing = 0
	var waits []time.Duration
	net.pause = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	for _, c := range []struct {
		after   string
		limited int32
		waits   []time.Duration
		second  bool
	}{
		// The node's longer wait, then an answer from the same relay.
		{"9", 1, []time.Duration{9 * time.Second}, false},
		// 2 s doubling, three retries, then the next relay.
		{"", 100, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, true},
		{"0", 100, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, true},
		// A wait the core reads no number from is no wait asked for.
		{"Wed, 21 Oct 2037 07:28:00 GMT", 100, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, true},
		{"60", 100, []time.Duration{time.Minute, time.Minute, time.Minute}, true},
		{"61", 100, nil, true},
	} {
		waits = nil
		after.Store(c.after)
		limited.Store(c.limited)
		asked := second.asked("/api/node/status")
		status, err := net.Status(ctx)
		if err != nil || status.Height != "80" {
			t.Fatal(c.after, err)
		}
		if fmt.Sprint(waits) != fmt.Sprint(c.waits) || (second.asked("/api/node/status") > asked) != c.second {
			t.Fatalf("Retry-After %q: waits %v, second relay asked %v", c.after, waits, second.asked("/api/node/status") > asked)
		}
		limited.Store(0)
	}
}

func TestARelayOfAnotherChainIsNeverAsked(t *testing.T) {
	s := testSDK(t)
	configuration, err := os.ReadFile("testdata/node/node-configuration.json")
	if err != nil {
		t.Fatal(err)
	}
	var after atomic.Value
	first := rateLimitedRelay(t, &after)
	other := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/node/configuration" {
			return false
		}
		_, _ = w.Write(bytes.Replace(configuration, []byte("c9b03ab996ef3ac216a2ac53eaee71118cbf7995fa44449a7fb7f94bbe18bcca"), []byte(strings.Repeat("ab", 32)), 1))
		return true
	})
	third := newTestRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", other.URL+"/api", third.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after.Store("4000000000")
	for range 2 {
		if _, err = net.Status(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if other.asked("/api/node/configuration") != 1 || other.asked("/api/node/status") != 0 {
		t.Fatal("the relay of another chain was used", other.requests())
	}
	if third.asked("/api/node/configuration") != 1 || third.asked("/api/node/status") != 2 {
		t.Fatal(third.requests())
	}
	// When no relay of the chain answers, the error is the last relay's.
	third.Close()
	if _, err = net.Status(ctx); errorCode(err) != "NodeUnavailable" {
		t.Fatal(err)
	}
}

func TestAnUnavailableRelayIsSkipped(t *testing.T) {
	s := testSDK(t)
	var oversized atomic.Bool
	first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/node/status" || !oversized.Load() {
			return false
		}
		_, _ = w.Write(paddedStatus(answerLimit + 1))
		return true
	})
	second := newTestRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", second.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A relay that sends more than the limit, then one that cannot be reached.
	oversized.Store(true)
	if status, err := net.Status(ctx); err != nil || status.Height != "80" {
		t.Fatal(status, err)
	}
	first.Close()
	if status, err := net.Status(ctx); err != nil || status.Height != "80" {
		t.Fatal(status, err)
	}
	if second.asked("/api/node/status") != 2 {
		t.Fatal(second.requests())
	}
}

func TestARelayThatAnswersWithAServerErrorOrARedirectIsSkipped(t *testing.T) {
	s := testSDK(t)
	elsewhere := newTestRelay(t, nil)
	var mode atomic.Value
	mode.Store("")
	first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch mode := mode.Load().(string); {
		case mode == "503" || (mode == "503 status" && r.URL.Path == "/api/node/status"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"statusCode":503,"error":"Service Unavailable","message":"busy"}`))
			return true
		case mode == "redirect" && r.URL.Path == "/api/node/status":
			http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
			return true
		}
		return false
	})
	second := newTestRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profile := Devnet(first.URL+"/api", second.URL+"/api")
	net, err := s.Connect(ctx, profile, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A server error, then a redirect, which is never followed: the next relay answers.
	for i, value := range []string{"503 status", "redirect"} {
		mode.Store(value)
		if status, err := net.Status(ctx); err != nil || status.Height != "80" {
			t.Fatalf("%s: %v %v", value, status, err)
		}
		if second.asked("/api/node/status") != i+1 {
			t.Fatal(value, second.requests())
		}
	}
	if asked := elsewhere.requests(); len(asked) != 0 {
		t.Fatal("the redirect was followed", asked)
	}
	// Connecting moves on from a relay that answers with a server error.
	mode.Store("503")
	if _, err = s.Connect(ctx, profile, ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	// With no other relay, the server error is what the core reads from the answer.
	mode.Store("")
	only, err := s.Connect(ctx, Devnet(first.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mode.Store("503 status")
	if _, err = only.Status(ctx); errorCode(err) != "Refused" {
		t.Fatal(err)
	}
	mode.Store("redirect")
	if _, err = only.Status(ctx); errorCode(err) != "NodeUnavailable" || !strings.Contains(err.Error(), "redirect") {
		t.Fatal(err)
	}
}

func TestConnectSkipsARelayThatStaysRateLimited(t *testing.T) {
	s := testSDK(t)
	first := newTestRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Retry-After", "4000000000")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	second := newTestRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	net, err := s.Connect(ctx, Devnet(first.URL+"/api", second.URL+"/api"), ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The connection's relay is asked first.
	before := len(first.requests())
	if _, err = net.Status(ctx); err != nil || len(first.requests()) != before {
		t.Fatal(err, first.requests())
	}
}

func TestARelaysOwnTextStaysOutOfErrors(t *testing.T) {
	s := testSDK(t)
	// A relay that answers with text that is not HTTP: Go's transport quotes it in its error.
	listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_, _ = conn.Write([]byte("RELAY-TEXT‮" + strings.Repeat("A", 5000) + "\r\n\r\n"))
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.Connect(ctx, Devnet("http://"+listener.Addr().String()+"/api"), ConnectOptions{})
	var e *Error
	if !errors.As(err, &e) || e.Code != "NodeUnavailable" {
		t.Fatal(err)
	}
	if strings.Contains(e.Error(), "RELAY-TEXT") || strings.Contains(e.Error(), "AAAA") {
		t.Fatalf("the relay's text is in the message: %.300s", e.Error())
	}
	// The transport's reason is kept in the details, escaped and cut to 200 characters.
	var details struct {
		Reason string `json:"reason"`
	}
	if err = json.Unmarshal(e.Details, &details); err != nil {
		t.Fatal(err, string(e.Details))
	}
	if n := utf8.RuneCountInString(details.Reason); n > 201 || !strings.HasSuffix(details.Reason, "…") ||
		!strings.Contains(details.Reason, "RELAY-TEXT") || strings.ContainsRune(details.Reason, '‮') {
		t.Fatalf("reason of %d characters: %s", n, details.Reason)
	}
}

func TestNodeText(t *testing.T) {
	for text, want := range map[string]string{
		"plain text":           "plain text",
		"a‮b\nc":               `a\u{202e}b\u{a}c`,
		"a­b c":                `a\u{ad}b\u{2003}c`,
		"":                     "",
		"\xff":                 `\u{fffd}`,
		strings.Repeat("é", 5): strings.Repeat("é", 5),
	} {
		if got := nodeText(text); got != want {
			t.Errorf("nodeText(%q) = %q, want %q", text, got, want)
		}
	}
	long := nodeText(strings.Repeat("é", 500))
	if utf8.RuneCountInString(long) != 201 || !strings.HasSuffix(long, "…") {
		t.Fatal(long)
	}
	// An escape is not cut: it is left out whole.
	if cut := nodeText(strings.Repeat("x", 195) + "‮"); cut != strings.Repeat("x", 195)+"…" {
		t.Fatal(cut)
	}
}
