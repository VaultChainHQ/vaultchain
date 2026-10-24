package kv

import (
	"bytes"
	"fmt"
	"math/big"
	"time"

	bolt "go.etcd.io/bbolt"
)

// BoltDB persists each bucket ("bank", "storage", ...) as a bbolt bucket and
// each key as one bbolt key, so a block's disk writes are proportional to the
// keys it changed. I/O failures panic: continuing with unknown state would be
// worse than stopping the node.
type BoltDB struct {
	db *bolt.DB
}

var accBucket, accKey = []byte("_meta"), []byte("acc")

func OpenBolt(path string) (*BoltDB, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	return &BoltDB{db: db}, nil
}

func (b *BoltDB) Close() error { return b.db.Close() }

// Bolt exposes the underlying handle (for tests and tooling).
func (b *BoltDB) Bolt() *bolt.DB { return b.db }

type boltReader struct{ tx *bolt.Tx }

func (r boltReader) Get(key []byte) []byte {
	bk, sub := SplitKey(key)
	bu := r.tx.Bucket(bk)
	if bu == nil {
		return nil
	}
	v := bu.Get(sub)
	if v == nil {
		return nil
	}
	return append([]byte(nil), v...)
}

func (r boltReader) Iterate(prefix []byte, fn func(key, value []byte) bool) {
	i := bytes.IndexByte(prefix, '/')
	if i <= 0 {
		panic(fmt.Sprintf("kv: iterate prefix %q must include bucket/", prefix))
	}
	bk, sub := prefix[:i], prefix[i+1:]
	bu := r.tx.Bucket(bk)
	if bu == nil {
		return
	}
	type ent struct{ k, v []byte }
	var entries []ent
	c := bu.Cursor()
	for k, v := c.Seek(sub); k != nil && bytes.HasPrefix(k, sub); k, v = c.Next() {
		full := make([]byte, 0, len(bk)+1+len(k))
		full = append(append(append(full, bk...), '/'), k...)
		entries = append(entries, ent{full, append([]byte(nil), v...)})
	}
	for _, e := range entries {
		if !fn(e.k, e.v) {
			return
		}
	}
}

func (r boltReader) acc() *big.Int {
	bu := r.tx.Bucket(accBucket)
	if bu == nil {
		return new(big.Int)
	}
	v := bu.Get(accKey)
	if v == nil {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(v)
}

func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("kv: bolt: %v", err))
	}
}

func (b *BoltDB) Get(key []byte) []byte {
	var out []byte
	must(b.db.View(func(tx *bolt.Tx) error { out = boltReader{tx}.Get(key); return nil }))
	return out
}

func (b *BoltDB) Iterate(prefix []byte, fn func(key, value []byte) bool) {
	type ent struct{ k, v []byte }
	var entries []ent
	must(b.db.View(func(tx *bolt.Tx) error {
		boltReader{tx}.Iterate(prefix, func(k, v []byte) bool {
			entries = append(entries, ent{k, v})
			return true
		})
		return nil
	}))
	for _, e := range entries {
		if !fn(e.k, e.v) {
			return
		}
	}
}

func (b *BoltDB) View(fn func(Reader) error) error {
	return b.db.View(func(tx *bolt.Tx) error { return fn(boltReader{tx}) })
}

func (b *BoltDB) Commit(ops []Op) error {
	ops = dedupe(ops)
	return b.db.Update(func(tx *bolt.Tx) error {
		r := boltReader{tx}
		acc := applyOps(r.acc(), ops, r.Get)
		for _, op := range ops {
			bk, sub := SplitKey(op.Key)
			if op.Delete {
				if bu := tx.Bucket(bk); bu != nil {
					if err := bu.Delete(sub); err != nil {
						return err
					}
				}
				continue
			}
			bu, err := tx.CreateBucketIfNotExists(bk)
			if err != nil {
				return err
			}
			if err := bu.Put(sub, op.Value); err != nil {
				return err
			}
		}
		meta, err := tx.CreateBucketIfNotExists(accBucket)
		if err != nil {
			return err
		}
		return meta.Put(accKey, accBytes(acc))
	})
}

func (b *BoltDB) AppHash() []byte {
	var out []byte
	must(b.db.View(func(tx *bolt.Tx) error { out = appHashOf(boltReader{tx}.acc()); return nil }))
	return out
}

func (b *BoltDB) AppHashAfter(ops []Op) []byte {
	ops = dedupe(ops)
	var out []byte
	must(b.db.View(func(tx *bolt.Tx) error {
		r := boltReader{tx}
		out = appHashOf(applyOps(r.acc(), ops, r.Get))
		return nil
	}))
	return out
}
