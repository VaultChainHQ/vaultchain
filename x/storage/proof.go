package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/x/bank"
)

var (
	keyEpoch = []byte("storage/epoch")

	ErrDealNotFound  = errors.New("deal not found")
	ErrNotChallenged = errors.New("deal is not challenged yet")
	slashDivisor     = int64(10) // placeholder: a failed deal slashes 1/10 of collateral
)

// Epoch is the current challenge window. A new one starts every
// Params.BlockInterval blocks; its seed is the app hash before that block, so
// the challenged chunk of every deal is unknown until the epoch begins.
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

// Challenge is the question a provider must answer for one deal this epoch.
type Challenge struct {
	DealID uint64 `json:"deal_id"`
	Epoch  int64  `json:"epoch"`
	Index  uint64 `json:"chunk_index"`
	Root   string `json:"root"`
	Seed   string `json:"seed"`
}

// ChallengeIndex picks the chunk to prove, deterministically from the seed.
func ChallengeIndex(seed string, dealID uint64, numChunks uint64) uint64 {
	h := sha256.New()
	h.Write([]byte("vaultchain/challenge/v1"))
	h.Write([]byte(seed))
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], dealID)
	h.Write(id[:])
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8]) % numChunks
}

// Challenge returns the current challenge for an open deal. Deals opened at or
// after the epoch start are not challenged until the next epoch.
func (k Keeper) Challenge(dealID uint64) (Challenge, error) {
	d, ok := k.Deal(dealID)
	if !ok {
		return Challenge{}, ErrDealNotFound
	}
	if d.Status != "open" {
		return Challenge{}, fmt.Errorf("deal %d is %s", dealID, d.Status)
	}
	ep, ok := k.Epoch()
	if !ok {
		return Challenge{}, fmt.Errorf("%w: no epoch has started", ErrNotChallenged)
	}
	if d.StartH >= ep.Height {
		return Challenge{}, fmt.Errorf("%w: opened at height %d, epoch began at %d", ErrNotChallenged, d.StartH, ep.Height)
	}
	man, ok := k.Manifest(d.CID)
	if !ok {
		return Challenge{}, fmt.Errorf("manifest %s missing", d.CID)
	}
	n := uint64(man.Chunk().NumChunks())
	return Challenge{DealID: dealID, Epoch: ep.Height, Index: ChallengeIndex(ep.Seed, dealID, n), Root: d.CID, Seed: ep.Seed}, nil
}

// SubmitProof verifies a Merkle inclusion proof of the challenged chunk
// against the deal's committed manifest root.
func (k Keeper) SubmitProof(msg MsgSubmitProof, height int64) error {
	if msg.From == "" || msg.CID == "" || msg.DealID == 0 {
		return fmt.Errorf("invalid submit proof fields")
	}
	deal, ok := k.Deal(msg.DealID)
	if !ok {
		return fmt.Errorf("deal %d not found", msg.DealID)
	}
	if deal.Status != "open" {
		return fmt.Errorf("deal not open")
	}
	if deal.CID != msg.CID {
		return fmt.Errorf("cid mismatch")
	}
	if deal.Provider != msg.From {
		return fmt.Errorf("only assigned provider may submit proof")
	}
	ch, err := k.Challenge(msg.DealID)
	if err != nil {
		return err
	}
	if msg.Epoch != ch.Epoch {
		return fmt.Errorf("stale or wrong epoch %d (current %d)", msg.Epoch, ch.Epoch)
	}
	if deal.LastProvenEpoch == ch.Epoch {
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
	deal.LastProvenH = height
	deal.LastProvenEpoch = ch.Epoch
	deal.MissedProofs = 0
	k.SetDeal(deal)
	k.SetProof(Proof{
		ID: k.nextID("proof"), DealID: deal.ID, CID: deal.CID, Provider: msg.From,
		Height: height, Epoch: ch.Epoch, ChunkIndex: ch.Index,
	})
	return nil
}

// EndBlock runs after a block's txs: it settles expired deals, counts missed
// challenges when an epoch rolls over and fails deals that miss too many, then
// starts the new epoch. seed is the app hash before this block.
func (k Keeper) EndBlock(height int64, seed string) error {
	p := k.Params()
	rollover := height%p.Interval() == 0
	prev, hadPrev := k.Epoch()
	for _, d := range k.Deals() { // sorted by id: deterministic
		if d.Status != "open" {
			continue
		}
		switch {
		case height >= d.ExpireH:
			if d.LastProvenEpoch > 0 {
				if err := k.release(d, "closed"); err != nil {
					return err
				}
			} else if err := k.fail(d); err != nil {
				return err
			}
		case rollover && hadPrev && d.StartH < prev.Height && d.LastProvenEpoch != prev.Height:
			d.MissedProofs++
			if d.MissedProofs >= p.MaxMissed() {
				if err := k.fail(d); err != nil {
					return err
				}
			} else {
				k.SetDeal(d)
			}
		}
	}
	if rollover {
		kv.SetJSON(k.S, keyEpoch, Epoch{Height: height, Seed: seed})
	}
	return nil
}

func (k Keeper) freeProvider(d Deal) (Provider, bool) {
	prov, ok := k.Provider(d.Provider)
	if ok {
		prov.FreeBytes += d.Size
		if prov.FreeBytes > prov.Capacity {
			prov.FreeBytes = prov.Capacity
		}
	}
	return prov, ok
}

// release pays the provider and closes the deal.
func (k Keeper) release(d Deal, status string) error {
	if err := k.Bank.SendCoins(bank.EscrowAddress(), d.Provider, sdk.NewCoins(d.Payment)); err != nil {
		return err
	}
	if prov, ok := k.freeProvider(d); ok {
		k.SetProvider(prov)
	}
	d.Status = status
	k.SetDeal(d)
	return nil
}

// fail refunds the owner and slashes part of the provider's collateral to them.
func (k Keeper) fail(d Deal) error {
	if err := k.Bank.SendCoins(bank.EscrowAddress(), d.Owner, sdk.NewCoins(d.Payment)); err != nil {
		return err
	}
	if prov, ok := k.freeProvider(d); ok {
		slash := prov.Collateral.Amount.QuoRaw(slashDivisor)
		if slash.IsPositive() {
			coin := sdk.NewCoin(prov.Collateral.Denom, slash)
			if err := k.Bank.SendCoins(bank.EscrowAddress(), d.Owner, sdk.NewCoins(coin)); err != nil {
				return err
			}
			prov.Collateral = prov.Collateral.Sub(coin)
		}
		k.SetProvider(prov)
	}
	d.Status = "failed"
	k.SetDeal(d)
	return nil
}
