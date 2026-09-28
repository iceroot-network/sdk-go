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
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTPDoer allows an application to inject its transport.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
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

// Network is a node client pinned to one chain. Use Profile to pass the pinned identity to an
// offline signer. Nonce reservation across pending withdrawals is the custodian's responsibility.
type Network struct {
	sdk           *SDK
	profile       Profile
	configuration json.RawMessage
	info          ChainInfo
	node          NodeConfiguration
	relay         string
	options       ConnectOptions
	mu            sync.Mutex
	nextRequest   time.Time
	infoMu        sync.RWMutex
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

func (s *SDK) Connect(ctx context.Context, p Profile, o ConnectOptions) (*Network, error) {
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
	n := &Network{sdk: s, profile: p, options: o}
	var last error
	for _, relay := range p.API.Relays {
		if err := s.call(ctx, "relay", map[string]any{"url": relay}, &n.relay); err != nil {
			return nil, err
		}
		crypto, err := n.exchange(ctx, "cryptoConfiguration", map[string]any{})
		if err != nil {
			last = err
			continue
		}
		args := map[string]any{"profile": p, "status": crypto.Status, "headers": crypto.Headers, "body": crypto.Body}
		if err = s.call(ctx, "chainNode", args, &n.info); err != nil {
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
			return nil, err
		}
		if _, err = n.Refresh(ctx); err != nil {
			last = err
			continue
		}
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
	n.nextRequest = at.Add(610 * time.Millisecond)
	n.mu.Unlock()
	return wait(ctx, time.Until(at))
}
func (n *Network) send(ctx context.Context, request nodeRequest) (nodeResponse, error) {
	for attempt := 0; attempt < 4; attempt++ {
		if err := n.throttle(ctx); err != nil {
			return nodeResponse{}, err
		}
		var body io.Reader
		if request.Body != nil {
			body = strings.NewReader(*request.Body)
		}
		req, err := http.NewRequestWithContext(ctx, request.Method, n.relay+request.Target, body)
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
		res, err := n.options.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nodeResponse{}, ctx.Err()
			}
			return nodeResponse{}, &Error{Code: "NodeUnavailable", Message: err.Error()}
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, maxJSON+1))
		_ = res.Body.Close()
		if readErr != nil {
			return nodeResponse{}, readErr
		}
		if len(data) > maxJSON {
			return nodeResponse{}, &Error{Code: "BadResponse", Message: "node response exceeds 16 MiB"}
		}
		response := nodeResponse{Status: uint16(res.StatusCode), Body: string(data), Headers: make([][2]string, 0, len(res.Header))}
		for k, values := range res.Header {
			for _, v := range values {
				response.Headers = append(response.Headers, [2]string{k, v})
			}
		}
		if res.StatusCode != 429 || attempt == 3 {
			return response, nil
		}
		delay := time.Duration(2<<attempt) * time.Second
		if seconds, err := strconv.ParseUint(res.Header.Get("Retry-After"), 10, 32); err == nil {
			if d := time.Duration(seconds) * time.Second; d > delay {
				delay = d
			}
		} else if at, err := http.ParseTime(res.Header.Get("Retry-After")); err == nil {
			if d := time.Until(at); d > delay {
				delay = d
			}
		}
		if err = wait(ctx, delay); err != nil {
			return nodeResponse{}, err
		}
	}
	return nodeResponse{}, fmt.Errorf("request retry limit")
}
func (n *Network) exchange(ctx context.Context, operation string, args any) (nodeResponse, error) {
	var request nodeRequest
	if err := n.sdk.call(ctx, "apiPrepare", map[string]any{"seats": n.node.Seats, "operation": operation, "args": args}, &request); err != nil {
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
