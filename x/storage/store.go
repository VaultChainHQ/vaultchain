package storage

import "github.com/vaultchain/vaultchain/kv"

var (
	keyParams   = []byte("storage/params")
	prefixProv  = []byte("storage/prov/")
	prefixPlan  = []byte("storage/plan/")
	prefixDeal  = []byte("storage/deal/")
	prefixProof = []byte("storage/proof/")
	prefixSeq   = "storage/seq/"
)

func (k Keeper) Params() Params {
	var p Params
	if !kv.GetJSON(k.S, keyParams, &p) {
		return DefaultParams()
	}
	return p
}

func (k Keeper) SetParams(p Params) { kv.SetJSON(k.S, keyParams, p) }

// nextID returns the next id (starting at 1) for a counter and advances it.
func (k Keeper) nextID(kind string) uint64 {
	key := []byte(prefixSeq + kind)
	id := kv.GetU64(k.S, key) + 1
	kv.SetU64(k.S, key, id)
	return id
}

func idKey(prefix []byte, id uint64) []byte {
	return append(append([]byte(nil), prefix...), kv.U64(id)...)
}

func (k Keeper) Provider(addr string) (Provider, bool) {
	var p Provider
	ok := kv.GetJSON(k.S, append(append([]byte(nil), prefixProv...), addr...), &p)
	return p, ok
}

func (k Keeper) SetProvider(p Provider) {
	kv.SetJSON(k.S, append(append([]byte(nil), prefixProv...), p.Address...), p)
}

func (k Keeper) Providers() []Provider {
	out := []Provider{}
	kv.IterateJSON(k.S, prefixProv, func(_ []byte, p Provider) bool { out = append(out, p); return true })
	return out
}

func (k Keeper) Plan(id uint64) (CapacityPlan, bool) {
	var p CapacityPlan
	ok := kv.GetJSON(k.S, idKey(prefixPlan, id), &p)
	return p, ok
}

func (k Keeper) SetPlan(p CapacityPlan) { kv.SetJSON(k.S, idKey(prefixPlan, p.ID), p) }

func (k Keeper) Plans() []CapacityPlan {
	out := []CapacityPlan{}
	kv.IterateJSON(k.S, prefixPlan, func(_ []byte, p CapacityPlan) bool { out = append(out, p); return true })
	return out
}

func (k Keeper) Deal(id uint64) (Deal, bool) {
	var d Deal
	ok := kv.GetJSON(k.S, idKey(prefixDeal, id), &d)
	return d, ok
}

func (k Keeper) SetDeal(d Deal) { kv.SetJSON(k.S, idKey(prefixDeal, d.ID), d) }

func (k Keeper) Deals() []Deal {
	out := []Deal{}
	kv.IterateJSON(k.S, prefixDeal, func(_ []byte, d Deal) bool { out = append(out, d); return true })
	return out
}

func (k Keeper) Proofs() []Proof {
	out := []Proof{}
	kv.IterateJSON(k.S, prefixProof, func(_ []byte, p Proof) bool { out = append(out, p); return true })
	return out
}

func (k Keeper) SetProof(p Proof) { kv.SetJSON(k.S, idKey(prefixProof, p.ID), p) }

// OwnerOfCID returns the owner of the earliest deal for cid (lowest deal id,
// so the result is deterministic). Replaced by manifest ownership in M5.
func (k Keeper) OwnerOfCID(cid string) (string, bool) {
	owner, found := "", false
	kv.IterateJSON(k.S, prefixDeal, func(_ []byte, d Deal) bool {
		if d.CID == cid {
			owner, found = d.Owner, true
			return false // ascending id order: first match is the lowest id
		}
		return true
	})
	return owner, found
}
