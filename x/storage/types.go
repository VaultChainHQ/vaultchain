package storage

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	vtypes "github.com/VaultChainHQ/vaultchain/types"
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
	// MaxObjectBytes caps the size of a committed manifest (0 = unlimited).
	MaxObjectBytes uint64 `json:"max_object_bytes"`
	// MaxMissedProofs is how many consecutive challenge epochs a provider may
	// skip before the deal fails (0 = default 3).
	MaxMissedProofs uint32 `json:"max_missed_proofs"`
	// AcceptTimeoutBlocks is how long assigned providers have to accept a deal
	// (0 = default 20).
	AcceptTimeoutBlocks int64 `json:"accept_timeout_blocks"`
}

// AcceptTimeout returns the effective accept window in blocks.
func (p Params) AcceptTimeout() int64 {
	if p.AcceptTimeoutBlocks <= 0 {
		return 20
	}
	return p.AcceptTimeoutBlocks
}

// MaxMissed returns the effective consecutive-miss limit.
func (p Params) MaxMissed() uint32 {
	if p.MaxMissedProofs == 0 {
		return 3
	}
	return p.MaxMissedProofs
}

// Interval returns the effective challenge epoch length in blocks.
func (p Params) Interval() int64 {
	if p.BlockInterval <= 0 {
		return 10
	}
	return p.BlockInterval
}

func DefaultParams() Params {
	return Params{
		PaymentDenomWhitelist: []string{vtypes.NativeDenom},
		IBCPolicyNote:         "ICS-20 tokens may fund deals; all storage infrastructure is native to Vaultchain.",
		PricePerGB:            1_000_000, // 1 vault per GB reference
		BlockInterval:         10,
		MaxObjectBytes:        4 << 30, // 4 GiB
		MaxMissedProofs:       3,
		AcceptTimeoutBlocks:   20,
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

// Deal and replica lifecycle.
//
// A deal is pending until MinReplicas of its assigned providers accept it
// (MsgAcceptDeal), then open until ExpireH. A pending deal that cannot reach
// quorum by the accept deadline is cancelled and refunded.
const (
	DealPending   = "pending"
	DealOpen      = "open"
	DealClosed    = "closed" // expired; at least one provider was paid
	DealFailed    = "failed" // expired; nobody was paid
	DealCancelled = "cancelled"

	ReplicaAssigned = "assigned" // chosen by the chain, has not accepted yet
	ReplicaActive   = "active"   // accepted; challenged every epoch
	ReplicaFailed   = "failed"   // missed too many proofs (slashed) or never proved
	ReplicaDeclined = "declined" // did not accept in time
	ReplicaDone     = "done"     // served the deal to expiry and was paid

	// MaxReplication bounds the providers assigned to one deal.
	MaxReplication = 16
)

// Replica is one provider's obligation for one slot of a deal. A failed or
// declined replica may be replaced by a new Replica in the same slot.
type Replica struct {
	Slot            int    `json:"slot"`
	Provider        string `json:"provider"`
	Status          string `json:"status"`
	AssignedH       int64  `json:"assigned_height"`
	AcceptDeadlineH int64  `json:"accept_deadline_height"`
	AcceptedH       int64  `json:"accepted_height"`
	LastProvenH     int64  `json:"last_proven_height"`
	// LastProvenEpoch is the epoch (its start height) of the last verified proof.
	LastProvenEpoch int64 `json:"last_proven_epoch"`
	// MissedProofs counts consecutive missed challenge epochs.
	MissedProofs uint32 `json:"missed_proofs"`
}

// Reserved reports whether the replica still holds provider capacity.
func (r Replica) Reserved() bool { return r.Status == ReplicaAssigned || r.Status == ReplicaActive }

type Deal struct {
	ID    uint64 `json:"id"`
	Class string `json:"class"` // market | archive
	Owner string `json:"owner"`
	CID   string `json:"cid"`
	Size  uint64 `json:"size"`
	// StartH is the height the deal was opened; ExpireH is StartH + duration.
	StartH  int64 `json:"start_height"`
	ExpireH int64 `json:"expire_height"`
	// Payment is the total escrowed: PerReplica * Replication.
	Payment     sdk.Coin  `json:"payment"`
	PerReplica  sdk.Coin  `json:"per_replica_payment"`
	Replication uint32    `json:"replication"`
	MinReplicas uint32    `json:"min_replicas"`
	Replicas    []Replica `json:"replicas"`
	Status      string    `json:"status"`
}

// ReplicaIndex returns the index of provider's live (assigned or active)
// replica in the deal.
func (d Deal) ReplicaIndex(provider string) (int, bool) {
	for i, r := range d.Replicas {
		if r.Provider == provider && r.Reserved() {
			return i, true
		}
	}
	return 0, false
}

// HasProvider reports whether provider has ever held a replica of the deal.
func (d Deal) HasProvider(provider string) bool {
	for _, r := range d.Replicas {
		if r.Provider == provider {
			return true
		}
	}
	return false
}

func (d Deal) activeCount() int {
	n := 0
	for _, r := range d.Replicas {
		if r.Status == ReplicaActive {
			n++
		}
	}
	return n
}

type Proof struct {
	ID         uint64 `json:"id"`
	DealID     uint64 `json:"deal_id"`
	CID        string `json:"cid"`
	Provider   string `json:"provider"`
	Height     int64  `json:"height"`
	Epoch      int64  `json:"epoch"`
	ChunkIndex uint64 `json:"chunk_index"`
}
