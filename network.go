package iceroot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// HTTPDoer allows an application to inject its transport.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// ConnectOptions configure a connection. HTTP defaults to a client with a 30-second timeout that
// follows no redirect; a relay that answers with one is unavailable for the request. Headers are sent with every request, except Accept-Encoding, which is always
// identity so that answers are read as sent. PollInterval spaces the reads of WaitConfirmed and
// WaitFinal (default 2 seconds).
type ConnectOptions struct {
	HTTP         HTTPDoer
	Headers      http.Header
	PollInterval time.Duration
}
type nodeRequest struct {
	Method  string      `json:"method"`
	Target  string      `json:"target"`
	Body    *string     `json:"body"`
	Headers [][2]string `json:"headers"`
}
type nodeResponse struct {
	Status  uint16      `json:"status"`
	Headers [][2]string `json:"headers"`
	Body    string      `json:"body"`
}

// transportLimits are the bounds of the core's node API client, which the host's transport keeps:
// the longest answer it reads, and the longest wait after HTTP 429 it accepts.
type transportLimits struct {
	MaxResponseBytes int64  `json:"maxResponseBytes"`
	MaxRetryAfterMs  uint64 `json:"maxRetryAfterMs"`
}

// What a connection knows of a relay's chain.
const (
	relayUnchecked = iota
	relaySameChain
	relayOtherChain
)

// Network is a node client pinned to one chain. Use Profile to pass the pinned identity to an
// offline signer. Nonce reservation across pending withdrawals is the custodian's responsibility.
type Network struct {
	sdk           *SDK
	profile       Profile
	configuration json.RawMessage
	info          ChainInfo
	node          NodeConfiguration
	// relays are the profile's relays as the core writes them, in order; relays[current] is the
	// one the connection was made through.
	relays      []string
	current     int
	connected   bool
	checkMu     sync.Mutex
	checked     []int
	refused     []error
	options     ConnectOptions
	limits      transportLimits
	pause       func(context.Context, time.Duration) error
	spacing     time.Duration
	mu          sync.Mutex
	nextRequest time.Time
	infoMu      sync.RWMutex
}

func (n *Network) Profile() Profile {
	p := n.profile
	p.API.Relays = append([]string{}, p.API.Relays...)
	return p
}

// Info returns the network's token and the rules, economics and vote rules in force at the next
// block, as of the last Connect or Refresh.
func (n *Network) Info() ChainInfo {
	n.infoMu.RLock()
	v := n.info
	n.infoMu.RUnlock()
	v.Profile = n.Profile()
	v.Token = append(json.RawMessage(nil), v.Token...)
	v.Rules = append(json.RawMessage(nil), v.Rules...)
	v.Economics = append(json.RawMessage(nil), v.Economics...)
	v.VoteRules = append(json.RawMessage(nil), v.VoteRules...)
	return v
}

// newNetwork is a client of p's relays, not yet connected, with the options' defaults and the
// core's transport limits.
func (s *SDK) newNetwork(ctx context.Context, p Profile, o ConnectOptions) (*Network, error) {
	if len(p.API.Relays) == 0 {
		return nil, &Error{Code: "NodeUnavailable", Message: "profile has no relays"}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	o.Headers = o.Headers.Clone()
	n := &Network{sdk: s, profile: p, options: o, pause: wait, spacing: 610 * time.Millisecond}
	if err := s.call(ctx, "transportLimits", nil, &n.limits); err != nil {
		return nil, err
	}
	n.relays = make([]string, len(p.API.Relays))
	for i, relay := range p.API.Relays {
		if err := s.call(ctx, "relay", map[string]any{"url": relay}, &n.relays[i]); err != nil {
			return nil, err
		}
	}
	n.checked = make([]int, len(n.relays))
	n.refused = make([]error, len(n.relays))
	return n, nil
}

// Connect tries p's relays in order until one answers, checks the chain it serves against p, and
// pins that chain. The connection sends each request to that relay first, and to the profile's
// other relays of the same chain when it is unavailable for the request.
func (s *SDK) Connect(ctx context.Context, p Profile, o ConnectOptions) (*Network, error) {
	n, err := s.newNetwork(ctx, p, o)
	if err != nil {
		return nil, err
	}
	var last error
	for i := range n.relays {
		n.current = i
		crypto, err := n.exchange(ctx, "cryptoConfiguration", map[string]any{})
		if err != nil {
			last = err
			continue
		}
		args := map[string]any{"profile": p, "status": crypto.Status, "headers": crypto.Headers, "body": crypto.Body}
		if err = s.call(ctx, "chainNode", args, &n.info); err != nil {
			if serverError(crypto) {
				last = err
				continue
			}
			return nil, err
		}
		var configuration struct {
			Data json.RawMessage `json:"data"`
		}
		if err = json.Unmarshal([]byte(crypto.Body), &configuration); err != nil {
			return nil, err
		}
		n.configuration = configuration.Data
		n.profile = n.info.Profile
		response, err := n.exchange(ctx, "nodeConfiguration", map[string]any{})
		if err != nil {
			last = err
			continue
		}
		args = map[string]any{"profile": n.profile, "configuration": n.configuration, "status": response.Status, "headers": response.Headers, "body": response.Body}
		if err = s.call(ctx, "chainCheck", args, &n.node); err != nil {
			if serverError(response) {
				last = err
				continue
			}
			return nil, err
		}
		if _, err = n.Refresh(ctx); err != nil {
			last = err
			continue
		}
		n.checked[i] = relaySameChain
		n.connected = true
		return n, nil
	}
	return nil, last
}

// Refresh reads the node's status and moves Info to the rules in force at the node's next block.
func (n *Network) Refresh(ctx context.Context) (NodeStatus, error) {
	status, err := n.Status(ctx)
	if err != nil {
		return status, err
	}
	height, err := strconv.ParseUint(status.Height, 10, 64)
	if err != nil {
		return status, &Error{Code: "BadResponse", Message: "node height is not a number"}
	}
	next := min(height+1, math.MaxUint32)
	var info ChainInfo
	if err = n.sdk.call(ctx, "chainInfo", map[string]any{"profile": n.profile, "configuration": n.configuration, "height": next}, &info); err != nil {
		return status, err
	}
	n.infoMu.Lock()
	n.info = info
	n.infoMu.Unlock()
	return status, nil
}
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// A conservative shared allowance matches the supported relay's 100 requests per minute.
func (n *Network) throttle(ctx context.Context) error {
	n.mu.Lock()
	now := time.Now()
	at := n.nextRequest
	if at.Before(now) {
		at = now
	}
	n.nextRequest = at.Add(n.spacing)
	n.mu.Unlock()
	return wait(ctx, time.Until(at))
}

// send sends request through the connection's relay. When that relay is unavailable for it (it
// cannot be reached, answers with a redirect or a server error, its answer is longer than the
// core's limit, or it stays rate limited), the profile's other relays are tried in order, each
// once its node configuration names the connection's chain. A relay of another chain is never
// asked again. The outcome is the last relay's: its error, or its server error's answer for the
// core to read. While connecting, only the relay being connected to is asked.
func (n *Network) send(ctx context.Context, request nodeRequest) (nodeResponse, error) {
	if !n.connected {
		return n.attempt(ctx, n.relays[n.current], request)
	}
	var last error
	var answer *nodeResponse
	for _, i := range n.order() {
		if err := n.identify(ctx, i); err != nil {
			if ctx.Err() != nil {
				return nodeResponse{}, ctx.Err()
			}
			last, answer = err, nil
			continue
		}
		response, err := n.attempt(ctx, n.relays[i], request)
		if err == nil && serverError(response) {
			last, answer = nil, &response
			continue
		}
		if code := errorCode(err); err == nil || (code != "NodeUnavailable" && code != "RateLimited") {
			return response, err
		}
		last, answer = err, nil
	}
	if answer != nil {
		return *answer, nil
	}
	return nodeResponse{}, last
}

// serverError reports whether response is a server error (HTTP 5xx), which makes its relay
// unavailable for the request, as the core's HTTP client reads it.
func serverError(response nodeResponse) bool {
	return response.Status >= 500
}

// order is the connection's relay, then the profile's other relays, each once.
func (n *Network) order() []int {
	order := []int{n.current}
	for i, relay := range n.relays {
		if !slices.ContainsFunc(order, func(j int) bool { return n.relays[j] == relay }) {
			order = append(order, i)
		}
	}
	return order
}

// identify checks, once, that relays[i] serves the connection's chain, by its node configuration.
// A relay of another chain is refused with NetworkMismatch from then on; one that cannot be checked
// now is checked again next time.
func (n *Network) identify(ctx context.Context, i int) error {
	n.checkMu.Lock()
	state, refused := n.checked[i], n.refused[i]
	n.checkMu.Unlock()
	switch state {
	case relaySameChain:
		return nil
	case relayOtherChain:
		return refused
	}
	request, err := n.prepare(ctx, "nodeConfiguration", map[string]any{})
	if err != nil {
		return err
	}
	response, err := n.attempt(ctx, n.relays[i], request)
	if err != nil {
		return err
	}
	args := map[string]any{"profile": n.profile, "configuration": n.configuration, "status": response.Status, "headers": response.Headers, "body": response.Body}
	err = n.sdk.call(ctx, "chainCheck", args, nil)
	n.checkMu.Lock()
	defer n.checkMu.Unlock()
	switch {
	case err == nil:
		n.checked[i] = relaySameChain
	case errorCode(err) == "NetworkMismatch":
		n.checked[i], n.refused[i] = relayOtherChain, err
	}
	return err
}

// attempt sends request to relay alone. After HTTP 429 it waits as the core's backoffDelay says,
// or returns RateLimited when the core says not to wait: the relay's retries are spent, or it asked
// for longer than the core's longest wait.
func (n *Network) attempt(ctx context.Context, relay string, request nodeRequest) (nodeResponse, error) {
	for attempt := uint32(0); ; attempt++ {
		if err := n.throttle(ctx); err != nil {
			return nodeResponse{}, err
		}
		response, err := n.fetch(ctx, relay, request)
		if err != nil || response.Status != http.StatusTooManyRequests {
			return response, err
		}
		seconds := retryAfterSeconds(response.Headers)
		args := map[string]any{"attempt": attempt, "retryAfterMs": nil}
		if seconds != nil {
			ms := uint64(math.MaxUint64)
			if *seconds <= math.MaxUint64/1000 {
				ms = *seconds * 1000
			}
			args["retryAfterMs"] = ms
		}
		var delay *uint64
		if err = n.sdk.call(ctx, "backoffDelay", args, &delay); err != nil {
			return nodeResponse{}, err
		}
		if delay == nil || *delay > n.limits.MaxRetryAfterMs {
			details, _ := json.Marshal(map[string]any{"retryAfterSeconds": seconds})
			return nodeResponse{}, &Error{Code: "RateLimited", Message: "rate limited by the node", Details: details}
		}
		if err = n.pause(ctx, time.Duration(*delay)*time.Millisecond); err != nil {
			return nodeResponse{}, err
		}
	}
}

// retryAfterSeconds reads the first Retry-After as the core does: whole seconds, or nothing.
func retryAfterSeconds(headers [][2]string) *uint64 {
	for _, h := range headers {
		if !strings.EqualFold(h[0], "Retry-After") {
			continue
		}
		// Rust's integers take one leading plus sign; strconv takes none.
		seconds, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(h[1]), "+"), 10, 64)
		if err != nil {
			return nil
		}
		return &seconds
	}
	return nil
}

// fetch makes one HTTP exchange with relay.
func (n *Network) fetch(ctx context.Context, relay string, request nodeRequest) (nodeResponse, error) {
	var body io.Reader
	if request.Body != nil {
		body = strings.NewReader(*request.Body)
	}
	url := relay + request.Target
	req, err := http.NewRequestWithContext(ctx, request.Method, url, body)
	if err != nil {
		return nodeResponse{}, err
	}
	req.Header = n.options.Headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for _, h := range request.Headers {
		req.Header.Set(h[0], h[1])
	}
	// Answers are read as they are sent. Go's transport decompresses an answer when it asked
	// for compression itself, and naming an encoding stops that for any transport.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := n.options.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nodeResponse{}, ctx.Err()
		}
		return nodeResponse{}, unavailable(request.Method+" "+url, err)
	}
	// A redirect would take the request to a host the profile does not name: the relay is
	// unavailable for the request, as one that cannot be reached, and its body is not read.
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		_ = res.Body.Close()
		return nodeResponse{}, &Error{Code: "NodeUnavailable", Message: fmt.Sprintf("%s %s answered with a redirect (HTTP %d), which the connection does not follow", request.Method, url, res.StatusCode)}
	}
	data, err := n.read(ctx, res, request.Method+" "+url)
	if err != nil {
		return nodeResponse{}, err
	}
	response := nodeResponse{Status: uint16(res.StatusCode), Body: string(data), Headers: make([][2]string, 0, len(res.Header))}
	for k, values := range res.Header {
		for _, v := range values {
			response.Headers = append(response.Headers, [2]string{k, v})
		}
	}
	return response, nil
}

// read reads the body of res, closing it, up to the core's answer limit. A relay that declares or
// sends a longer answer is unavailable, as one that cannot be reached: the body is not read past
// the limit.
func (n *Network) read(ctx context.Context, res *http.Response, what string) ([]byte, error) {
	defer res.Body.Close()
	limit := n.limits.MaxResponseBytes
	tooLong := func(length string) error {
		return &Error{Code: "NodeUnavailable", Message: fmt.Sprintf("%s answered with a body of %s bytes; at most %d are read", what, length, limit)}
	}
	if res.ContentLength > limit {
		return nil, tooLong(strconv.FormatInt(res.ContentLength, 10))
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable(what, err)
	}
	if int64(len(data)) > limit {
		return nil, tooLong(fmt.Sprintf("%d or more", len(data)))
	}
	return data, nil
}

// unavailable is NodeUnavailable for the request what, which failed with cause. The message is the
// SDK's own. The transport's reason can quote what the relay sent (a line that is not HTTP, or the
// names in its certificate), so it is kept in the details alone, as reason, and as the core keeps a
// node's text there: escaped and cut to 200 characters.
func unavailable(what string, cause error) error {
	details, _ := json.Marshal(map[string]string{"reason": nodeText(cause.Error())})
	return &Error{Code: "NodeUnavailable", Message: what + " failed; the transport's reason is in the details", Details: details}
}

// maxNodeText is the most characters of a relay's own text an error keeps, as in the core.
const maxNodeText = 200

// nodeText is text a relay chose, as an error keeps it: control, format and separator characters
// and every space but the ASCII space, which can hide or reorder what is shown, written as escapes
// (\u{202e}), as are bytes that are not UTF-8 (\u{fffd}), and at most maxNodeText characters,
// escapes included, then "…".
func nodeText(text string) string {
	var out strings.Builder
	count := 0
	for i, r := range text {
		piece := string(r)
		// A byte that is not UTF-8 reads as the replacement character, which is kept as itself
		// where the text holds it.
		invalid := r == utf8.RuneError && !strings.HasPrefix(text[i:], "\uFFFD")
		if invalid || hidden(r) {
			piece = fmt.Sprintf("\\u{%x}", r)
		}
		width := utf8.RuneCountInString(piece)
		if count+width > maxNodeText {
			out.WriteString("…")
			break
		}
		count += width
		out.WriteString(piece)
	}
	return out.String()
}

// hidden reports whether r can hide or reorder what is shown: a control or format character
// (Unicode categories Cc and Cf), a line or paragraph separator (Zl, Zp), or a space other than the
// ASCII space (Zs).
func hidden(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) || (r != ' ' && unicode.Is(unicode.Zs, r))
}

// prepare has the core write the request of a node API operation.
func (n *Network) prepare(ctx context.Context, operation string, args any) (nodeRequest, error) {
	var request nodeRequest
	err := n.sdk.call(ctx, "apiPrepare", map[string]any{"seats": n.node.Seats, "operation": operation, "args": args}, &request)
	return request, err
}
func (n *Network) exchange(ctx context.Context, operation string, args any) (nodeResponse, error) {
	request, err := n.prepare(ctx, operation, args)
	if err != nil {
		return nodeResponse{}, err
	}
	return n.send(ctx, request)
}

// Read performs a shared SDK node operation and decodes into out. Amounts and wide integers
// use decimal strings. It exposes every read operation listed by the shared Rust binding.
func (n *Network) Read(ctx context.Context, operation string, args any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	response, err := n.exchange(ctx, operation, args)
	if err != nil {
		return err
	}
	return n.sdk.call(ctx, "apiDecode", map[string]any{"seats": n.node.Seats, "operation": operation, "args": args, "status": response.Status, "headers": response.Headers, "body": response.Body}, out)
}
func (n *Network) Account(ctx context.Context, address string) (AccountInfo, error) {
	var a AccountInfo
	err := n.Read(ctx, "account", map[string]any{"address": address}, &a)
	return a, err
}
func (n *Network) Status(ctx context.Context) (NodeStatus, error) {
	var s NodeStatus
	err := n.Read(ctx, "nodeStatus", nil, &s)
	return s, err
}
func (n *Network) Transaction(ctx context.Context, id string) (*Transaction, error) {
	var t *Transaction
	err := n.Read(ctx, "transaction", map[string]any{"id": id}, &t)
	if err == nil && t != nil && t.ID != id {
		return nil, &Error{Code: "BadResponse", Message: "node returned another transaction"}
	}
	return t, err
}

// Build reads nonce and next height and resolves fees under the connected chain's rules.
func (n *Network) Build(ctx context.Context, publicKey string, request BuildRequest) (Draft, error) {
	address, err := n.sdk.AddressFromPublicKey(ctx, n.profile, publicKey)
	if err != nil {
		return Draft{}, err
	}
	account, err := n.Account(ctx, address)
	var accountValue any = account
	if err != nil {
		var e *Error
		if !errors.As(err, &e) || e.Code != "NotFound" {
			return Draft{}, err
		}
		accountValue = nil
	}
	status, err := n.Status(ctx)
	if err != nil {
		return Draft{}, err
	}
	var facts OnlineFacts
	err = n.sdk.call(ctx, "onlineFacts", map[string]any{"profile": n.profile, "configuration": n.configuration, "sender": publicKey, "account": accountValue, "status": status}, &facts)
	if err != nil {
		return Draft{}, err
	}
	return n.sdk.BuildOffline(ctx, n.profile, n.configuration, request, facts)
}

// Submit sends only transactions revalidated by the core, in batches within the node's pool
// limits. Outcomes include accepted, already-known and refused transactions with node codes.
func (n *Network) Submit(ctx context.Context, transactions ...SignedTransaction) (json.RawMessage, error) {
	serialized := make([]string, len(transactions))
	for i, t := range transactions {
		serialized[i] = t.Serialized
	}
	args := map[string]any{"profile": n.profile, "transactions": serialized, "maxTransactions": n.node.Pool.MaxTransactionsPerRequest, "maxBytes": n.node.Pool.MaxTransactionBytes}
	var plan struct {
		Requests []nodeRequest `json:"requests"`
	}
	if err := n.sdk.call(ctx, "submitPrepare", args, &plan); err != nil {
		return nil, err
	}
	responses := make([]nodeResponse, 0, len(plan.Requests))
	for _, request := range plan.Requests {
		response, err := n.send(ctx, request)
		if err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	args["responses"] = responses
	var result json.RawMessage
	err := n.sdk.call(ctx, "submitDecode", args, &result)
	return result, err
}

// WaitFinal requires the finality capability and returns only finalized:true. Confirmations
// on the classical devnet never count as finality.
func (n *Network) WaitFinal(ctx context.Context, id string) (*Transaction, error) {
	capabilities, err := n.sdk.Capabilities(ctx, n.profile)
	if err != nil {
		return nil, err
	}
	supported := false
	for _, c := range capabilities {
		if c == "finality" {
			supported = true
		}
	}
	if !supported {
		return nil, &Error{Code: "UnsupportedOnNetwork", Message: "finality is unavailable", Details: json.RawMessage(`{"capability":"finality"}`)}
	}
	return n.waitTransaction(ctx, id, true)
}

// WaitConfirmed waits for inclusion only. It must not be used as a finality crediting rule.
// A transaction the pool drops is never reported, so give ctx a deadline.
func (n *Network) WaitConfirmed(ctx context.Context, id string) (*Transaction, error) {
	return n.waitTransaction(ctx, id, false)
}
func (n *Network) waitTransaction(ctx context.Context, id string, final bool) (*Transaction, error) {
	for {
		t, err := n.Transaction(ctx, id)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Code != "NotFound" {
				return nil, err
			}
		}
		if t != nil && ((final && t.Finalized) || (!final && t.Status == "confirmed")) {
			return t, nil
		}
		if err = wait(ctx, n.options.PollInterval); err != nil {
			return nil, err
		}
	}
}

// DecodeJSON preserves wide integers in arbitrary node responses.
func DecodeJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(out)
}
