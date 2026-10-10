package iceroot

import (
	"context"
	"encoding/hex"
	"encoding/json"
)

// Draft is portable and contains no secrets. Serialized is hex of the core's versioned format.
// The core revalidates the serialized draft on every signing call.
type Draft struct {
	Serialized string       `json:"serialized"`
	Summary    DraftSummary `json:"summary"`
}
type SignedTransaction struct {
	Serialized string          `json:"serialized"`
	ID         string          `json:"id"`
	Bytes      string          `json:"bytes"`
	JSON       json.RawMessage `json:"json"`
	Verified   bool            `json:"verified"`
}

// LoadChain loads a chain definition (the data of a node's crypto configuration) offline and
// returns the token and the rules, economics and vote rules in force at height.
func (s *SDK) LoadChain(ctx context.Context, p Profile, configuration json.RawMessage, height uint32) (ChainInfo, error) {
	var out ChainInfo
	err := s.call(ctx, "chainLoad", map[string]any{"profile": p, "configuration": configuration, "height": height}, &out)
	return out, err
}

// BuildOffline builds using explicitly supplied online facts. Applications normally use
// Network.Build to read the current nonce, height and fee rules from the node.
func (s *SDK) BuildOffline(ctx context.Context, p Profile, configuration json.RawMessage, request BuildRequest, facts OnlineFacts) (Draft, error) {
	var out Draft
	err := s.call(ctx, "draftBuild", map[string]any{"profile": p, "configuration": configuration, "request": request, "facts": facts}, &out)
	return out, err
}

// DeserializeDraft reads a serialized draft for p, whose network hash must be pinned; a draft for
// another profile or network is refused. The summary is computed again from the transaction's own
// fields. p is all the reader has, so the fee's floor is computed under the network configuration
// the draft carries, which the pinned network hash does not cover: a fee the draft calls the floor
// reads "unverified", never "floor", and the floor beside it is for display only. Show such a fee
// as an amount, never as the network's minimum. A reader connected to the network uses
// Network.DeserializeDraft instead.
func (s *SDK) DeserializeDraft(ctx context.Context, p Profile, data []byte) (Draft, error) {
	return s.readDraft(ctx, p, nil, data)
}

// DeserializeDraft reads a serialized draft on the connection's chain: a draft built under another
// network configuration (another fee table, other rules or other labels) is refused with
// NetworkMismatch, whose details name the reason "configuration". The floor is computed at the
// draft's height, which the builder chose, and a fee at it reads "floor" only when the floor of
// the network's next block is the same. That block is the one after the node's height as of the
// connection's last status read (Connect, Refresh, Status or Build). When a milestone between the
// two heights changes the fee table, the fee reads "unverified", with the floor at the draft's
// height kept for display: show it as an amount, never as the network's minimum.
func (n *Network) DeserializeDraft(ctx context.Context, data []byte) (Draft, error) {
	return n.sdk.readDraft(ctx, n.profile, n, data)
}

// readDraft reads data for p, on the chain of the connection on when it is given.
func (s *SDK) readDraft(ctx context.Context, p Profile, on *Network, data []byte) (Draft, error) {
	d := Draft{Serialized: hex.EncodeToString(data)}
	args := map[string]any{"profile": p, "serialized": d.Serialized}
	on.chain(args)
	err := s.call(ctx, "draftRead", args, &d.Summary)
	return d, err
}

// chain adds to the arguments of a draft's read the connection's chain and the height of the
// network's next block, at which the core judges the draft's floor. A nil connection adds nothing:
// the draft is read with the profile alone.
func (n *Network) chain(args map[string]any) {
	if n == nil {
		return
	}
	args["configuration"] = n.configuration
	args["height"] = n.nextHeight()
}
func (d Draft) Serialize() ([]byte, error) { return hex.DecodeString(d.Serialized) }

// Sign reads the draft again for p and signs what it read with key, and with second when the
// sender has a second key.
func (d Draft) Sign(ctx context.Context, p Profile, key, second *Account) (SignedTransaction, error) {
	return d.sign(ctx, p, nil, key, second)
}

// SignDraft signs d as Draft.Sign does, reading it on the connection's chain as DeserializeDraft
// does: a draft built under another network configuration is refused with NetworkMismatch before
// anything is signed. The keys may belong to another runtime than the connection's.
func (n *Network) SignDraft(ctx context.Context, d Draft, key, second *Account) (SignedTransaction, error) {
	return d.sign(ctx, n.profile, n, key, second)
}

// sign signs d for p, on the chain of the connection on when it is given.
func (d Draft) sign(ctx context.Context, p Profile, on *Network, key, second *Account) (SignedTransaction, error) {
	var out SignedTransaction
	if key == nil {
		return out, &Error{Code: "InvalidKey", Message: "signing account is required"}
	}
	args := map[string]any{"profile": p, "serialized": d.Serialized, "key": key.handle}
	on.chain(args)
	if second != nil {
		if second.sdk != key.sdk {
			return out, &Error{Code: "InvalidKey", Message: "second key belongs to another runtime"}
		}
		args["second"] = second.handle
	}
	err := key.sdk.call(ctx, "draftSign", args, &out)
	return out, err
}
