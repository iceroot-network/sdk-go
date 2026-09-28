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
func (s *SDK) DeserializeDraft(ctx context.Context, p Profile, data []byte) (Draft, error) {
	d := Draft{Serialized: hex.EncodeToString(data)}
	err := s.call(ctx, "draftRead", map[string]any{"profile": p, "serialized": d.Serialized}, &d.Summary)
	return d, err
}
func (d Draft) Serialize() ([]byte, error) { return hex.DecodeString(d.Serialized) }
func (d Draft) Sign(ctx context.Context, p Profile, key, second *Account) (SignedTransaction, error) {
	var out SignedTransaction
	if key == nil {
		return out, &Error{Code: "InvalidKey", Message: "signing account is required"}
	}
	args := map[string]any{"profile": p, "serialized": d.Serialized, "key": key.handle}
	if second != nil {
		if second.sdk != key.sdk {
			return out, &Error{Code: "InvalidKey", Message: "second key belongs to another runtime"}
		}
		args["second"] = second.handle
	}
	err := key.sdk.call(ctx, "draftSign", args, &out)
	return out, err
}
