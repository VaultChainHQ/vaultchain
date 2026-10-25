package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/x/bank"
)

// slashDivisor: a failed replica forfeits 1/10 of its provider's collateral to
// the deal owner.
const slashDivisor = int64(10)

// pickProviders deterministically chooses n providers for a deal: active
// providers with enough free bytes and not in exclude, ranked by
// sha256(seed || deal id || address). pinned (optional) takes the first place.
// The seed is the app hash before the block, so no single party picks.
func (k Keeper) pickProviders(seed string, dealID, size uint64, n int, exclude map[string]bool, pinned string) ([]string, error) {
	skip := map[string]bool{}
	for a := range exclude {
		skip[a] = true
	}
	var out []string
	if pinned != "" {
		p, ok := k.Provider(pinned)
		if !ok || p.Status != "active" {
			return nil, fmt.Errorf("provider not active: %s", pinned)
		}
		if p.FreeBytes < size {
			return nil, fmt.Errorf("provider lacks capacity")
		}
		if skip[pinned] {
			return nil, fmt.Errorf("provider %s already in this deal", pinned)
		}
		out = append(out, pinned)
		skip[pinned] = true
	}
	type cand struct {
		addr  string
		score [32]byte
	}
	var cands []cand
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], dealID)
	for _, p := range k.Providers() {
		if p.Status != "active" || p.FreeBytes < size || skip[p.Address] {
			continue
		}
		h := sha256.New()
		h.Write([]byte("vaultchain/assign/v1"))
		h.Write([]byte(seed))
		h.Write(id[:])
		h.Write([]byte(p.Address))
		var c cand
		c.addr = p.Address
		copy(c.score[:], h.Sum(nil))
		cands = append(cands, c)
	}
	sort.Slice(cands, func(i, j int) bool {
		if c := bytes.Compare(cands[i].score[:], cands[j].score[:]); c != 0 {
			return c < 0
		}
		return cands[i].addr < cands[j].addr
	})
	for _, c := range cands {
		if len(out) >= n {
			break
		}
		out = append(out, c.addr)
	}
	if len(out) < n {
		return nil, fmt.Errorf("only %d eligible providers (active, %d bytes free) for %d requested", len(out), size, n)
	}
	return out, nil
}

func (k Keeper) reserve(addr string, size uint64) {
	if p, ok := k.Provider(addr); ok {
		p.FreeBytes -= size
		k.SetProvider(p)
	}
}

func (k Keeper) release(addr string, size uint64) {
	if p, ok := k.Provider(addr); ok {
		p.FreeBytes += size
		if p.FreeBytes > p.Capacity {
			p.FreeBytes = p.Capacity
		}
		k.SetProvider(p)
	}
}

// OpenDeal assigns providers to a committed manifest and escrows
// PerReplica*Replication. The deal is pending until providers accept.
func (k Keeper) OpenDeal(msg MsgOpenDeal, height int64, seed string) error {
	if msg.From == "" || msg.CID == "" || msg.Duration <= 0 {
		return fmt.Errorf("invalid open deal fields")
	}
	man, ok := k.Manifest(msg.CID)
	if !ok {
		return fmt.Errorf("manifest %s not committed (commit-manifest first)", msg.CID)
	}
	if man.Owner != msg.From {
		return fmt.Errorf("only the manifest owner may open a deal for %s", msg.CID)
	}
	if msg.Size == 0 {
		msg.Size = man.Size
	}
	if msg.Size != man.Size {
		return fmt.Errorf("deal size %d does not match manifest size %d", msg.Size, man.Size)
	}
	if msg.Class != ClassMarket && msg.Class != ClassArchive {
		return fmt.Errorf("class must be market or archive")
	}
	if err := k.EnsurePaymentDenom(msg.Denom); err != nil {
		return err
	}
	replication := msg.Replication
	if replication == 0 {
		replication = 1
		if msg.Class == ClassArchive {
			replication = 3
		}
	}
	if replication > MaxReplication {
		return fmt.Errorf("replication must be <= %d", MaxReplication)
	}
	minRep := msg.MinReplicas
	if minRep == 0 {
		minRep = replication
	}
	if minRep > replication {
		return fmt.Errorf("min_replicas %d exceeds replication %d", minRep, replication)
	}
	id := k.nextID("deal")
	provs, err := k.pickProviders(seed, id, msg.Size, int(replication), nil, msg.Provider)
	if err != nil {
		return err
	}
	per := k.priceForBytes(msg.Size, msg.Duration, msg.Denom)
	total := sdk.NewCoin(per.Denom, per.Amount.MulRaw(int64(replication)))
	if err := k.Bank.SendCoins(msg.From, bank.EscrowAddress(), sdk.NewCoins(total)); err != nil {
		return err
	}
	expire := height + msg.Duration
	d := Deal{
		ID: id, Class: msg.Class, Owner: msg.From, CID: msg.CID, Size: msg.Size,
		StartH: height, ExpireH: expire, Payment: total, PerReplica: per,
		Replication: replication, MinReplicas: minRep, Status: DealPending,
	}
	for slot, addr := range provs {
		k.reserve(addr, d.Size)
		d.Replicas = append(d.Replicas, k.newReplica(d, slot, addr, height))
	}
	k.SetDeal(d)
	return nil
}

func (k Keeper) newReplica(d Deal, slot int, addr string, height int64) Replica {
	deadline := height + k.Params().AcceptTimeout()
	if deadline > d.ExpireH {
		deadline = d.ExpireH
	}
	return Replica{Slot: slot, Provider: addr, Status: ReplicaAssigned, AssignedH: height, AcceptDeadlineH: deadline}
}

// AcceptDeal lets an assigned provider take on its replica before the accept
// deadline. The deal opens once MinReplicas providers have accepted.
func (k Keeper) AcceptDeal(msg MsgAcceptDeal, height int64) error {
	d, ok := k.Deal(msg.DealID)
	if !ok {
		return ErrDealNotFound
	}
	if d.Status != DealPending && d.Status != DealOpen {
		return fmt.Errorf("deal %d is %s", d.ID, d.Status)
	}
	i, ok := d.ReplicaIndex(msg.From)
	if !ok || d.Replicas[i].Status != ReplicaAssigned {
		return fmt.Errorf("no pending assignment for %s in deal %d", msg.From, d.ID)
	}
	if height >= d.Replicas[i].AcceptDeadlineH {
		return fmt.Errorf("accept deadline (height %d) passed", d.Replicas[i].AcceptDeadlineH)
	}
	d.Replicas[i].Status = ReplicaActive
	d.Replicas[i].AcceptedH = height
	if d.Status == DealPending && d.activeCount() >= int(d.MinReplicas) {
		d.Status = DealOpen
	}
	k.SetDeal(d)
	return nil
}

// slash moves 1/10 of the provider's collateral to the owner.
func (k Keeper) slash(provider, owner string) error {
	p, ok := k.Provider(provider)
	if !ok {
		return nil
	}
	amt := p.Collateral.Amount.QuoRaw(slashDivisor)
	if amt.IsPositive() {
		coin := sdk.NewCoin(p.Collateral.Denom, amt)
		if err := k.Bank.SendCoins(bank.EscrowAddress(), owner, sdk.NewCoins(coin)); err != nil {
			return err
		}
		p.Collateral = p.Collateral.Sub(coin)
		k.SetProvider(p)
	}
	return nil
}

func (k Keeper) refund(d Deal, c sdk.Coin) error {
	return k.Bank.SendCoins(bank.EscrowAddress(), d.Owner, sdk.NewCoins(c))
}

// cancel refunds a pending deal that did not reach quorum.
func (k Keeper) cancel(d Deal) error {
	if err := k.refund(d, d.Payment); err != nil {
		return err
	}
	for i := range d.Replicas {
		r := &d.Replicas[i]
		if r.Reserved() {
			k.release(r.Provider, d.Size)
			if r.Status == ReplicaAssigned {
				r.Status = ReplicaDeclined
			} else {
				r.Status = ReplicaDone
			}
		}
	}
	d.Status = DealCancelled
	k.SetDeal(d)
	return nil
}

// settle ends an open deal at expiry. Each slot's share goes to its active
// replica if that replica proved at least once; otherwise it is refunded to the
// owner (and an active replica that never proved is slashed).
func (k Keeper) settle(d Deal) error {
	paid := 0
	for slot := 0; slot < int(d.Replication); slot++ {
		idx := -1
		for i, r := range d.Replicas {
			if r.Slot == slot && r.Status == ReplicaActive {
				idx = i
			}
		}
		if idx >= 0 && d.Replicas[idx].LastProvenEpoch > 0 {
			r := &d.Replicas[idx]
			if err := k.Bank.SendCoins(bank.EscrowAddress(), r.Provider, sdk.NewCoins(d.PerReplica)); err != nil {
				return err
			}
			r.Status = ReplicaDone
			k.release(r.Provider, d.Size)
			paid++
			continue
		}
		if err := k.refund(d, d.PerReplica); err != nil {
			return err
		}
		if idx >= 0 {
			if err := k.slash(d.Replicas[idx].Provider, d.Owner); err != nil {
				return err
			}
			d.Replicas[idx].Status = ReplicaFailed
			k.release(d.Replicas[idx].Provider, d.Size)
		}
	}
	for i := range d.Replicas {
		if r := &d.Replicas[i]; r.Status == ReplicaAssigned { // unaccepted repair candidates
			k.release(r.Provider, d.Size)
			r.Status = ReplicaDeclined
		}
	}
	d.Status = DealFailed
	if paid > 0 {
		d.Status = DealClosed
	}
	k.SetDeal(d)
	return nil
}

// failReplica slashes a provider that missed too many proofs and frees its
// capacity; its slot becomes repairable.
func (k Keeper) failReplica(d *Deal, i int) error {
	r := &d.Replicas[i]
	if err := k.slash(r.Provider, d.Owner); err != nil {
		return err
	}
	k.release(r.Provider, d.Size)
	r.Status = ReplicaFailed
	return nil
}

// repair assigns a replacement provider to every slot of an open deal that has
// no live replica, when an eligible provider exists. Replacements start in
// "assigned" and must accept (after fetching the bytes from another replica).
func (k Keeper) repair(d *Deal, height int64, seed string) {
	for slot := 0; slot < int(d.Replication); slot++ {
		live := false
		for _, r := range d.Replicas {
			if r.Slot == slot && r.Reserved() {
				live = true
			}
		}
		if live {
			continue
		}
		exclude := map[string]bool{}
		for _, r := range d.Replicas {
			exclude[r.Provider] = true
		}
		picked, err := k.pickProviders(seed+fmt.Sprint(slot), d.ID, d.Size, 1, exclude, "")
		if err != nil {
			continue // no eligible provider now; retried at the next epoch
		}
		k.reserve(picked[0], d.Size)
		d.Replicas = append(d.Replicas, k.newReplica(*d, slot, picked[0], height))
	}
}
