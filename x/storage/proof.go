package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
)

var (
	keyEpoch = []byte("storage/epoch")

	ErrDealNotFound  = errors.New("deal not found")
	ErrNotChallenged = errors.New("not challengeable yet")
)

// Epoch is the current challenge window. A new one starts every
// Params.BlockInterval blocks; its seed is the app hash before that block, so
// the challenged chunk of every replica is unknown until the epoch begins.
// (Lab-grade randomness: the app hash is public one block earlier, and a
// proposer can influence it through the txs it includes.)
type Epoch struct {
	Height int64  `json:"height"`
	Seed   string `json:"seed"`
}

func (k Keeper) Epoch() (Epoch, bool) {
	var e Epoch
	ok := kv.GetJSON(k.S, keyEpoch, &e)
	return e, ok
}

// Challenge is the question one provider must answer for one deal this epoch.
type Challenge struct {
	DealID   uint64 `json:"deal_id"`
	Provider string `json:"provider"`
	Epoch    int64  `json:"epoch"`
	Index    uint64 `json:"chunk_index"`
	Root     string `json:"root"`
	Seed     string `json:"seed"`
}

// ChallengeIndex picks the chunk a provider must prove, deterministically from
// the seed. It depends on the provider, so replicas cannot proxy each other.
func ChallengeIndex(seed string, dealID uint64, provider string, numChunks uint64) uint64 {
	h := sha256.New()
	h.Write([]byte("vaultchain/challenge/v2"))
	h.Write([]byte(seed))
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], dealID)
	h.Write(id[:])
	h.Write([]byte(provider))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8]) % numChunks
}

// Challenge returns the current challenge for provider's active replica of an
// open deal. Replicas that accepted at or after the epoch start are not
// challenged until the next epoch.
func (k Keeper) Challenge(dealID uint64, provider string) (Challenge, error) {
	d, ok := k.Deal(dealID)
	if !ok {
		return Challenge{}, ErrDealNotFound
	}
	if d.Status != DealOpen {
		return Challenge{}, fmt.Errorf("deal %d is %s", dealID, d.Status)
	}
	i, ok := d.ReplicaIndex(provider)
	if !ok || d.Replicas[i].Status != ReplicaActive {
		return Challenge{}, fmt.Errorf("%s has no active replica in deal %d", provider, dealID)
	}
	ep, ok := k.Epoch()
	if !ok {
		return Challenge{}, fmt.Errorf("%w: no epoch has started", ErrNotChallenged)
	}
	if d.Replicas[i].AcceptedH >= ep.Height {
		return Challenge{}, fmt.Errorf("%w: accepted at height %d, epoch began at %d", ErrNotChallenged, d.Replicas[i].AcceptedH, ep.Height)
	}
	man, ok := k.Manifest(d.CID)
	if !ok {
		return Challenge{}, fmt.Errorf("manifest %s missing", d.CID)
	}
	n := uint64(man.Chunk().NumChunks())
	return Challenge{DealID: dealID, Provider: provider, Epoch: ep.Height,
		Index: ChallengeIndex(ep.Seed, dealID, provider, n), Root: d.CID, Seed: ep.Seed}, nil
}

// SubmitProof verifies a Merkle inclusion proof of the challenged chunk
// against the deal's committed manifest root, for the sender's replica.
func (k Keeper) SubmitProof(msg MsgSubmitProof, height int64) error {
	if msg.From == "" || msg.CID == "" || msg.DealID == 0 {
		return fmt.Errorf("invalid submit proof fields")
	}
	deal, ok := k.Deal(msg.DealID)
	if !ok {
		return fmt.Errorf("deal %d not found", msg.DealID)
	}
	if deal.CID != msg.CID {
		return fmt.Errorf("cid mismatch")
	}
	ch, err := k.Challenge(msg.DealID, msg.From)
	if err != nil {
		return err
	}
	if msg.Epoch != ch.Epoch {
		return fmt.Errorf("stale or wrong epoch %d (current %d)", msg.Epoch, ch.Epoch)
	}
	ri, _ := deal.ReplicaIndex(msg.From)
	rep := &deal.Replicas[ri]
	if rep.LastProvenEpoch == ch.Epoch {
		return fmt.Errorf("epoch %d already proven", ch.Epoch)
	}
	if len(msg.Chunk) == 0 || len(msg.Chunk) > MaxChunkSize {
		return fmt.Errorf("chunk must be 1..%d bytes", MaxChunkSize)
	}
	if len(msg.Proof) > 64 {
		return fmt.Errorf("proof too long")
	}
	path := make(chunk.Proof, 0, len(msg.Proof))
	for _, s := range msg.Proof {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != 32 {
			return fmt.Errorf("bad proof element %q", s)
		}
		path = append(path, b)
	}
	man, _ := k.Manifest(deal.CID)
	if err := chunk.VerifyProof(man.Chunk(), int(ch.Index), msg.Chunk, path); err != nil {
		return fmt.Errorf("proof for chunk %d invalid: %w", ch.Index, err)
	}
	rep.LastProvenH = height
	rep.LastProvenEpoch = ch.Epoch
	rep.MissedProofs = 0
	k.SetDeal(deal)
	k.SetProof(Proof{
		ID: k.nextID("proof"), DealID: deal.ID, CID: deal.CID, Provider: msg.From,
		Height: height, Epoch: ch.Epoch, ChunkIndex: ch.Index,
	})
	return nil
}

// EndBlock runs after a block's txs, deterministically, in deal-id order:
//   - pending deals: unaccepted assignments past their deadline are declined;
//     the deal opens at quorum or is cancelled (refund) when none can accept;
//   - open deals: settle at expiry; on epoch rollover count missed proofs,
//     fail (slash) replicas over the limit and repair empty slots;
//   - on rollover, start the new epoch (seed = app hash before this block).
func (k Keeper) EndBlock(height int64, seed string) error {
	p := k.Params()
	rollover := height%p.Interval() == 0
	prev, hadPrev := k.Epoch()
	for _, d := range k.Deals() {
		if d.Status != DealPending && d.Status != DealOpen {
			continue
		}
		if d.Status == DealPending && height >= d.ExpireH {
			if err := k.cancel(d); err != nil {
				return err
			}
			continue
		}
		if d.Status == DealOpen && height >= d.ExpireH {
			if err := k.settle(d); err != nil {
				return err
			}
			continue
		}
		changed := false
		assigned := 0
		for i := range d.Replicas {
			r := &d.Replicas[i]
			switch {
			case r.Status == ReplicaAssigned && height >= r.AcceptDeadlineH:
				r.Status = ReplicaDeclined
				k.release(r.Provider, d.Size)
				changed = true
			case r.Status == ReplicaAssigned:
				assigned++
			case r.Status == ReplicaActive && d.Status == DealOpen && rollover && hadPrev &&
				r.AcceptedH < prev.Height && r.LastProvenEpoch != prev.Height:
				r.MissedProofs++
				changed = true
				if r.MissedProofs >= p.MaxMissed() {
					if err := k.failReplica(&d, i); err != nil {
						return err
					}
				}
			}
		}
		if d.Status == DealPending && assigned == 0 {
			if d.activeCount() >= int(d.MinReplicas) {
				d.Status = DealOpen
				changed = true
			} else {
				if err := k.cancel(d); err != nil {
					return err
				}
				continue
			}
		}
		if d.Status == DealOpen && rollover {
			n := len(d.Replicas)
			k.repair(&d, height, seed)
			changed = changed || len(d.Replicas) != n
			live := false
			for _, r := range d.Replicas {
				live = live || r.Reserved()
			}
			if !live { // every replica failed and none could be replaced: refund now
				if err := k.settle(d); err != nil {
					return err
				}
				continue
			}
		}
		if changed {
			k.SetDeal(d)
		}
	}
	if rollover {
		kv.SetJSON(k.S, keyEpoch, Epoch{Height: height, Seed: seed})
	}
	return nil
}
