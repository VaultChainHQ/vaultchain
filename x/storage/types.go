package storage

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	vtypes "github.com/vaultchain/vaultchain/types"
)

const (
	ClassPersonal = "personal"
	ClassMarket   = "market"
	ClassArchive  = "archive"

	ModuleName = "storage"
)

// Params configures storage economics and payment policy.
type Params struct {
	PaymentDenomWhitelist []string `json:"payment_denom_whitelist"`
	// IBCPolicyNote documents the payments-only IBC stance (enforced via whitelist).
	IBCPolicyNote string `json:"ibc_policy_note"`
	PricePerGB    int64  `json:"price_per_gb_uvault"` // reference pricing for personal
	BlockInterval int64  `json:"proof_interval_blocks"`
}

func DefaultParams() Params {
	return Params{
		PaymentDenomWhitelist: []string{vtypes.NativeDenom},
		IBCPolicyNote:         "ICS-20 tokens may fund deals; all storage infrastructure is native to Vaultchain.",
		PricePerGB:            1_000_000, // 1 vault per GB reference
		BlockInterval:         10,
	}
}

func (p Params) IsPaymentDenomAllowed(denom string) bool {
	for _, d := range p.PaymentDenomWhitelist {
		if d == denom {
			return true
		}
	}
	return false
}

type Provider struct {
	Address    string   `json:"address"`
	Endpoint   string   `json:"endpoint"`
	Capacity   uint64   `json:"capacity"`
	FreeBytes  uint64   `json:"free_bytes"`
	Collateral sdk.Coin `json:"collateral"`
	Status     string   `json:"status"` // active | shutdown
}

type CapacityPlan struct {
	ID        uint64   `json:"id"`
	Owner     string   `json:"owner"`
	Bytes     uint64   `json:"bytes"`
	BytesUsed uint64   `json:"bytes_used"`
	StartH    int64    `json:"start_height"`
	EndH      int64    `json:"end_height"`
	Payment   sdk.Coin `json:"payment"`
}

type Deal struct {
	ID           uint64   `json:"id"`
	Class        string   `json:"class"` // market | archive
	Owner        string   `json:"owner"`
	Provider     string   `json:"provider"`
	CID          string   `json:"cid"`
	Size         uint64   `json:"size"`
	StartH       int64    `json:"start_height"`
	ExpireH      int64    `json:"expire_height"`
	Payment      sdk.Coin `json:"payment"`
	Replication  uint32   `json:"replication"`
	LastProvenH  int64    `json:"last_proven_height"`
	MissedProofs uint32   `json:"missed_proofs"`
	Status       string   `json:"status"` // open | closed | failed
}

type Proof struct {
	ID          uint64 `json:"id"`
	DealID      uint64 `json:"deal_id"`
	CID         string `json:"cid"`
	Provider    string `json:"provider"`
	Height      int64  `json:"height"`
	Attestation string `json:"attestation"`
}
