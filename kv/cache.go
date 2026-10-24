package kv

import (
	"bytes"
	"sort"
)

type cacheEntry struct {
	value   []byte
	deleted bool
}

// Cache buffers writes over a parent Reader. Nothing reaches the parent until
// WriteTo or Ops is used, so dropping the Cache discards every change.
type Cache struct {
	parent Reader
	dirty  map[string]cacheEntry
}

func NewCache(parent Reader) *Cache {
	return &Cache{parent: parent, dirty: map[string]cacheEntry{}}
}

func (c *Cache) Get(key []byte) []byte {
	if e, ok := c.dirty[string(key)]; ok {
		if e.deleted {
			return nil
		}
		return e.value
	}
	return c.parent.Get(key)
}

func (c *Cache) Set(key, value []byte) {
	validate(key, value, false)
	c.dirty[string(key)] = cacheEntry{value: append([]byte(nil), value...)}
}

func (c *Cache) Delete(key []byte) {
	validate(key, nil, true)
	c.dirty[string(key)] = cacheEntry{deleted: true}
}

// Iterate merges the parent's entries with this cache's pending writes.
func (c *Cache) Iterate(prefix []byte, fn func(key, value []byte) bool) {
	merged := map[string][]byte{}
	c.parent.Iterate(prefix, func(k, v []byte) bool {
		merged[string(k)] = append([]byte(nil), v...)
		return true
	})
	for k, e := range c.dirty {
		if !bytes.HasPrefix([]byte(k), prefix) {
			continue
		}
		if e.deleted {
			delete(merged, k)
		} else {
			merged[k] = e.value
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !fn([]byte(k), merged[k]) {
			return
		}
	}
}

// Ops returns the pending write-set sorted by key.
func (c *Cache) Ops() []Op {
	keys := make([]string, 0, len(c.dirty))
	for k := range c.dirty {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ops := make([]Op, 0, len(keys))
	for _, k := range keys {
		e := c.dirty[k]
		ops = append(ops, Op{Key: []byte(k), Value: e.value, Delete: e.deleted})
	}
	return ops
}

// WriteTo applies the pending writes to s (typically the parent cache) and
// clears them.
func (c *Cache) WriteTo(s Store) {
	for _, op := range c.Ops() {
		if op.Delete {
			s.Delete(op.Key)
		} else {
			s.Set(op.Key, op.Value)
		}
	}
	c.dirty = map[string]cacheEntry{}
}

// Len is the number of pending writes.
func (c *Cache) Len() int { return len(c.dirty) }
