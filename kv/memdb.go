package kv

import (
	"bytes"
	"math/big"
	"sort"
	"sync"
)

// MemDB is an in-memory DB, used by tests and tools.
type MemDB struct {
	mu     sync.RWMutex
	data   map[string][]byte
	sorted []string
	acc    *big.Int
}

func NewMemDB() *MemDB {
	return &MemDB{data: map[string][]byte{}, acc: new(big.Int)}
}

func (m *MemDB) Get(key []byte) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.get(key)
}

func (m *MemDB) get(key []byte) []byte { return m.data[string(key)] }

func (m *MemDB) Iterate(prefix []byte, fn func(key, value []byte) bool) {
	m.mu.RLock()
	entries := m.collect(prefix)
	m.mu.RUnlock()
	for _, e := range entries {
		if !fn([]byte(e.k), e.v) {
			return
		}
	}
}

type memEntry struct {
	k string
	v []byte
}

func (m *MemDB) collect(prefix []byte) []memEntry {
	p := string(prefix)
	i := sort.SearchStrings(m.sorted, p)
	var out []memEntry
	for ; i < len(m.sorted) && bytes.HasPrefix([]byte(m.sorted[i]), prefix); i++ {
		out = append(out, memEntry{m.sorted[i], m.data[m.sorted[i]]})
	}
	return out
}

type memView struct{ m *MemDB }

func (v memView) Get(key []byte) []byte { return v.m.get(key) }
func (v memView) Iterate(prefix []byte, fn func(key, value []byte) bool) {
	for _, e := range v.m.collect(prefix) {
		if !fn([]byte(e.k), e.v) {
			return
		}
	}
}

func (m *MemDB) View(fn func(Reader) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fn(memView{m})
}

func (m *MemDB) Commit(ops []Op) error {
	ops = dedupe(ops)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acc = applyOps(m.acc, ops, m.get)
	for _, op := range ops {
		k := string(op.Key)
		_, exists := m.data[k]
		if op.Delete {
			if exists {
				delete(m.data, k)
				i := sort.SearchStrings(m.sorted, k)
				m.sorted = append(m.sorted[:i], m.sorted[i+1:]...)
			}
			continue
		}
		m.data[k] = append([]byte(nil), op.Value...)
		if !exists {
			i := sort.SearchStrings(m.sorted, k)
			m.sorted = append(m.sorted, "")
			copy(m.sorted[i+1:], m.sorted[i:])
			m.sorted[i] = k
		}
	}
	return nil
}

func (m *MemDB) AppHash() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return appHashOf(m.acc)
}

func (m *MemDB) AppHashAfter(ops []Op) []byte {
	ops = dedupe(ops)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return appHashOf(applyOps(m.acc, ops, m.get))
}

func (m *MemDB) Close() error { return nil }

// Len is the number of stored keys.
func (m *MemDB) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.data)
}
