package iceroot

import "encoding/json"

// Amount is an exact, nonnegative decimal integer in base units. The core validates its range.
// It is a string so JSON and callers never round a monetary value through floating point.
type Amount string

// Profile binds operations to a network. A connected Network pins the reported chain identity.
type Profile struct {
	ID        string        `json:"id"`
	Backend   string        `json:"backend"`
	API       Endpoints     `json:"api"`
	Chain     ChainIdentity `json:"chain"`
	KeyScheme string        `json:"keyScheme"`
	CoinType  uint32        `json:"coinType"`
}
type Endpoints struct {
	Relays  []string `json:"relays"`
	Indexer string   `json:"indexer,omitempty"`
}
type ChainIdentity struct {
	NetworkByte uint8  `json:"networkByte"`
	Nethash     string `json:"nethash,omitempty"`
	ChainID     string `json:"chainId,omitempty"`
	GenesisHash string `json:"genesisHash,omitempty"`
}

// Devnet returns the supported classical network profile. No node is contacted.
func Devnet(relays ...string) Profile {
	if relays == nil {
		relays = []string{}
	}
	return Profile{ID: "devnet", Backend: "solar-compat", API: Endpoints{Relays: relays}, Chain: ChainIdentity{NetworkByte: 90}, KeyScheme: "bip32-secp256k1", CoinType: 1}
}

type Recipient struct {
	Address string `json:"address"`
	Amount  Amount `json:"amount"`
}
type VoteEntry struct {
	Validator   string `json:"validator"`
	BasisPoints uint16 `json:"basisPoints"`
}

// Operation describes a transfer, vote (empty entries withdraw it), burn, second key
// registration, validator registration, or validator resignation.
type Operation struct {
	Kind        string      `json:"kind"`
	To          []Recipient `json:"to,omitempty"`
	Entries     []VoteEntry `json:"entries,omitempty"`
	Amount      Amount      `json:"amount,omitempty"`
	PublicKey   string      `json:"publicKey,omitempty"`
	Name        string      `json:"name,omitempty"`
	Resignation string      `json:"resignation,omitempty"`
}

// MarshalJSON keeps an empty vote explicit, as required to withdraw a vote.
func (o Operation) MarshalJSON() ([]byte, error) {
	type alias Operation
	if o.Kind == "vote" {
		e := o.Entries
		if e == nil {
			e = []VoteEntry{}
		}
		return json.Marshal(struct {
			alias
			Entries []VoteEntry `json:"entries"`
		}{alias(o), e})
	}
	return json.Marshal(alias(o))
}

type FeeChoice struct {
	Kind        string `json:"kind"`
	Amount      Amount `json:"amount,omitempty"`
	BasisPoints uint32 `json:"basisPoints,omitempty"`
}
type BuildRequest struct {
	Operation Operation  `json:"operation"`
	Memo      string     `json:"memo,omitempty"`
	Fee       *FeeChoice `json:"fee,omitempty"`
}
type OnlineFacts struct {
	Sender    string  `json:"sender"`
	Nonce     string  `json:"nonce"`
	Height    uint32  `json:"height"`
	SecondKey *string `json:"secondKey"`
}

// ResolvedFee is a draft's fee. Source is "floor" when the fee is the floor of a chain the reader
// loaded itself (a draft built by, or read on, a connection), "explicit" when it was chosen, and
// "unverified" when it equals the floor of the configuration a serialized draft carries, which a
// reader without the chain cannot trust: show that fee as an amount, never as the network's
// minimum. Floor is the floor at the draft's height, or nil where no floor is in force.
type ResolvedFee struct {
	Amount Amount  `json:"amount"`
	Source string  `json:"source"`
	Floor  *Amount `json:"floor"`
}
type DraftSummary struct {
	Profile         string      `json:"profile"`
	NetworkByte     uint8       `json:"networkByte"`
	Nethash         string      `json:"nethash"`
	SecondSignature bool        `json:"secondSignature"`
	Kind            string      `json:"kind"`
	Operation       Operation   `json:"operation"`
	From            string      `json:"from"`
	PublicKey       string      `json:"publicKey"`
	Nonce           string      `json:"nonce"`
	Fee             ResolvedFee `json:"fee"`
	Amount          Amount      `json:"amount"`
	Memo            *string     `json:"memo"`
	Height          uint32      `json:"height"`
	Size            uint32      `json:"size"`
}
type MessageSignature struct {
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
	Algorithm string `json:"algorithm"`
	Network   string `json:"network"`
}
type PhraseCheck struct {
	OK       bool    `json:"ok"`
	Words    uint32  `json:"words"`
	Reason   string  `json:"reason,omitempty"`
	Position *uint32 `json:"position,omitempty"`
}
type ChainInfo struct {
	Profile   Profile         `json:"profile"`
	Token     json.RawMessage `json:"token"`
	Rules     json.RawMessage `json:"rules"`
	Economics json.RawMessage `json:"economics"`
	VoteRules json.RawMessage `json:"voteRules"`
}
type AccountInfo struct {
	Address         string      `json:"address"`
	PublicKey       string      `json:"publicKey,omitempty"`
	Nonce           string      `json:"nonce"`
	SecondPublicKey string      `json:"secondPublicKey,omitempty"`
	Balances        []Balance   `json:"balances"`
	Vote            []VoteEntry `json:"vote"`
}
type Balance struct {
	Asset  string `json:"asset"`
	Amount Amount `json:"amount"`
}
type NodeStatus struct {
	Height       string `json:"height"`
	Synced       bool   `json:"synced"`
	BlocksBehind string `json:"blocksBehind"`
	ChainTime    string `json:"chainTime"`
}

// TransactionDetails preserves the core's kind-specific node response fields.
type TransactionDetails struct {
	Kind        string      `json:"kind"`
	Recipients  []Recipient `json:"recipients,omitempty"`
	Entries     []VoteEntry `json:"entries,omitempty"`
	Amount      Amount      `json:"amount,omitempty"`
	PublicKey   string      `json:"publicKey,omitempty"`
	Name        string      `json:"name,omitempty"`
	Resignation string      `json:"resignation,omitempty"`
	TypeGroup   uint32      `json:"typeGroup,omitempty"`
	TypeID      uint16      `json:"typeId,omitempty"`
	AssetJSON   *string     `json:"assetJson,omitempty"`
}

// Transaction is a transaction as a node reports it. Status is "pending" or "confirmed".
// Finalized can be true only on a network with the finality capability; today's devnet has
// none, so it is always false there and WaitFinal refuses.
type Transaction struct {
	ID              string             `json:"id"`
	Status          string             `json:"status"`
	Finalized       bool               `json:"finalized"`
	Sender          string             `json:"sender"`
	SenderPublicKey string             `json:"senderPublicKey"`
	Nonce           string             `json:"nonce"`
	Fee             Amount             `json:"fee"`
	BurnedFee       *Amount            `json:"burnedFee,omitempty"`
	Memo            *string            `json:"memo,omitempty"`
	Direction       string             `json:"direction,omitempty"`
	Block           json.RawMessage    `json:"block,omitempty"`
	SecondSigned    bool               `json:"secondSigned"`
	Version         uint8              `json:"version"`
	Details         TransactionDetails `json:"details"`
}
type SubmitOutcome struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Broadcast bool   `json:"broadcast,omitempty"`
	Reason    string `json:"reason,omitempty"`
	NodeCode  string `json:"nodeCode,omitempty"`
	Message   string `json:"message,omitempty"`
}
type SubmitReport struct {
	Outcomes []SubmitOutcome `json:"outcomes"`
}
type PoolLimits struct {
	MaxTransactionsPerRequest uint32 `json:"maxTransactionsPerRequest"`
	MaxTransactionBytes       uint32 `json:"maxTransactionBytes"`
}
type NodeConfiguration struct {
	Seats uint32     `json:"seats"`
	Pool  PoolLimits `json:"pool"`
}
