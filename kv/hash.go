package kv

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"sort"
)

// The application hash is an incremental set hash (AdHash): the sum modulo
// 2^256 of H(key,value) over every hashed entry. Updating it costs O(changed
// keys), independent of state size, and it is independent of write order.
// It commits to exactly the hashed state, but it is lab-grade: unlike a
// Merkle tree it has no inclusion proofs and its collision resistance is
// weaker (generalized-birthday attacks). Swap for an IAVL/SMT before any
// adversarial deployment.

var modulus = new(big.Int).Lsh(big.NewInt(1), 256)

func entryHash(key, value []byte) *big.Int {
	h := sha256.New()
	h.Write([]byte("vaultchain/kv/entry/v1\x00"))
	var l [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(l[:], uint64(len(key)))
	h.Write(l[:n])
	h.Write(key)
	h.Write(value)
	return new(big.Int).SetBytes(h.Sum(nil))
}

func accBytes(a *big.Int) []byte {
	out := make([]byte, 32)
	a.FillBytes(out)
	return out
}

// AppHashOf turns an accumulator into the published application hash.
func appHashOf(acc *big.Int) []byte {
	h := sha256.New()
	h.Write([]byte("vaultchain/apphash/v1\x00"))
	h.Write(accBytes(acc))
	return h.Sum(nil)
}

// dedupe sorts ops by key keeping the last write per key.
func dedupe(ops []Op) []Op {
	idx := map[string]int{}
	out := make([]Op, 0, len(ops))
	for _, op := range ops {
		if i, ok := idx[string(op.Key)]; ok {
			out[i] = op
			continue
		}
		idx[string(op.Key)] = len(out)
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i].Key) < string(out[j].Key) })
	return out
}

// applyOps returns acc updated for ops, reading previous values through get.
func applyOps(acc *big.Int, ops []Op, get func(key []byte) []byte) *big.Int {
	res := new(big.Int).Set(acc)
	for _, op := range ops {
		if string(op.Key) == ReservedAccKey {
			panic("kv: " + ReservedAccKey + " is reserved")
		}
		if !Hashed(op.Key) {
			continue
		}
		if old := get(op.Key); old != nil {
			res.Sub(res, entryHash(op.Key, old))
		}
		if !op.Delete {
			res.Add(res, entryHash(op.Key, op.Value))
		}
	}
	res.Mod(res, modulus)
	return res
}
