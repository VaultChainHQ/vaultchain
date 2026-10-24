// Package kv is the tiny ordered key-value layer all chain state lives in.
//
// Keys are "<bucket>/<subkey>". Buckets whose name starts with '_' (for
// example _meta, _blocks, _txs) are node bookkeeping and are NOT part of the
// application hash; every other bucket is consensus state and is.
//
// A Cache layers uncommitted writes over a parent Reader, which is how the
// state machine gets per-block and per-transaction atomicity: writes go to a
// cache and are applied to the parent only on success.
package kv

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Reader is read access to an ordered key-value space. A missing key reads as
// nil; empty values are therefore not allowed.
type Reader interface {
	Get(key []byte) []byte
	// Iterate calls fn for every key with the given prefix in ascending order
	// until fn returns false. The prefix must contain the bucket separator.
	// Slices passed to fn must not be modified or retained.
	Iterate(prefix []byte, fn func(key, value []byte) bool)
}

// Store is a writable Reader.
type Store interface {
	Reader
	Set(key, value []byte)
	Delete(key []byte)
}

// Op is one write in a write-set.
type Op struct {
	Key    []byte
	Value  []byte // nil when Delete
	Delete bool
}

// DB is a durable (or in-memory) committed store that maintains the
// application hash over the hashed buckets.
type DB interface {
	Reader
	// View runs fn against one consistent snapshot.
	View(fn func(Reader) error) error
	// Commit atomically applies ops and updates the application hash.
	Commit(ops []Op) error
	// AppHash is the application hash of the committed state.
	AppHash() []byte
	// AppHashAfter is the application hash that would result from ops.
	AppHashAfter(ops []Op) []byte
	Close() error
}

// ReservedAccKey holds the hash accumulator; applications must not write it.
const ReservedAccKey = "_meta/acc"

// SplitKey splits "<bucket>/<sub>". It panics on malformed keys: keys are
// built by code, never from user input.
func SplitKey(key []byte) (bucket, sub []byte) {
	i := bytes.IndexByte(key, '/')
	if i <= 0 || i == len(key)-1 {
		panic(fmt.Sprintf("kv: malformed key %q (want bucket/sub)", key))
	}
	return key[:i], key[i+1:]
}

// Hashed reports whether key belongs to a bucket covered by the app hash.
func Hashed(key []byte) bool {
	b, _ := SplitKey(key)
	return b[0] != '_'
}

// K joins key parts with '/'.
func K(parts ...string) []byte {
	n := len(parts) - 1
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			b = append(b, '/')
		}
		b = append(b, p...)
	}
	return b
}

// U64 is an 8-byte big-endian encoding so numeric keys sort numerically.
func U64(n uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	return string(b[:])
}

// DecodeU64 reverses U64 on a value or key suffix.
func DecodeU64(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// GetU64 / SetU64 store counters as 8-byte values.
func GetU64(r Reader, key []byte) uint64 { return DecodeU64(r.Get(key)) }

func SetU64(s Store, key []byte, n uint64) { s.Set(key, []byte(U64(n))) }

// GetJSON loads and decodes a JSON value. It reports whether the key exists.
func GetJSON(r Reader, key []byte, v any) bool {
	b := r.Get(key)
	if b == nil {
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		panic(fmt.Sprintf("kv: corrupt value at %q: %v", key, err))
	}
	return true
}

// SetJSON stores v as JSON (Go's encoder is deterministic for structs).
func SetJSON(s Store, key []byte, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("kv: marshal %q: %v", key, err))
	}
	s.Set(key, b)
}

// IterateJSON decodes every value under prefix into a new T.
func IterateJSON[T any](r Reader, prefix []byte, fn func(key []byte, v T) bool) {
	r.Iterate(prefix, func(k, raw []byte) bool {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			panic(fmt.Sprintf("kv: corrupt value at %q: %v", k, err))
		}
		return fn(k, v)
	})
}

// ReadOnly wraps a Reader as a Store whose writes panic. It lets module
// keepers serve queries from a read snapshot.
func ReadOnly(r Reader) Store { return roStore{r} }

type roStore struct{ Reader }

func (roStore) Set(_, _ []byte) { panic("kv: write to read-only store") }
func (roStore) Delete(_ []byte) { panic("kv: write to read-only store") }

func validate(key, value []byte, del bool) {
	SplitKey(key)
	if !del && len(value) == 0 {
		panic(fmt.Sprintf("kv: empty value for %q", key))
	}
}
