package kv

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

func dbs(t *testing.T) map[string]DB {
	t.Helper()
	b, err := OpenBolt(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return map[string]DB{"mem": NewMemDB(), "bolt": b}
}

func set(c *Cache, k, v string) { c.Set([]byte(k), []byte(v)) }

func TestCacheDiscardAndWrite(t *testing.T) {
	for name, db := range dbs(t) {
		t.Run(name, func(t *testing.T) {
			base := NewCache(db)
			set(base, "bank/a", "1")
			if err := db.Commit(base.Ops()); err != nil {
				t.Fatal(err)
			}
			branch := NewCache(db)
			set(branch, "bank/a", "2")
			set(branch, "bank/b", "3")
			if string(branch.Get([]byte("bank/a"))) != "2" || string(db.Get([]byte("bank/a"))) != "1" {
				t.Fatal("branch must shadow without touching parent")
			}
			// Dropping the branch leaves the parent untouched.
			if db.Get([]byte("bank/b")) != nil {
				t.Fatal("uncommitted write visible in db")
			}
			// Nested cache: child writes reach parent only via WriteTo.
			child := NewCache(branch)
			child.Delete([]byte("bank/a"))
			if branch.Get([]byte("bank/a")) == nil {
				t.Fatal("child delete leaked to parent cache")
			}
			child.WriteTo(branch)
			if branch.Get([]byte("bank/a")) != nil {
				t.Fatal("child delete not applied by WriteTo")
			}
		})
	}
}

func TestIterateMergesAndSorts(t *testing.T) {
	for name, db := range dbs(t) {
		t.Run(name, func(t *testing.T) {
			c := NewCache(db)
			for _, k := range []string{"x/b", "x/d", "x/a", "y/z"} {
				set(c, k, "v")
			}
			if err := db.Commit(c.Ops()); err != nil {
				t.Fatal(err)
			}
			o := NewCache(db)
			set(o, "x/c", "v")
			o.Delete([]byte("x/d"))
			var got []string
			o.Iterate([]byte("x/"), func(k, v []byte) bool { got = append(got, string(k)); return true })
			if fmt.Sprint(got) != "[x/a x/b x/c]" {
				t.Fatalf("got %v", got)
			}
			got = nil
			db.Iterate([]byte("x/"), func(k, v []byte) bool { got = append(got, string(k)); return len(got) < 2 })
			if fmt.Sprint(got) != "[x/a x/b]" {
				t.Fatalf("early stop got %v", got)
			}
		})
	}
}

func TestAppHashOrderIndependentAndBackendIndependent(t *testing.T) {
	d := dbs(t)
	mem, blt := d["mem"], d["bolt"]
	// Same final content via different histories must give the same hash.
	a := NewCache(mem)
	set(a, "bank/a", "1")
	set(a, "bank/b", "2")
	set(a, "storage/x", "9")
	mem.Commit(a.Ops())

	b1 := NewCache(blt)
	set(b1, "storage/x", "9")
	set(b1, "bank/b", "tmp")
	blt.Commit(b1.Ops())
	b2 := NewCache(blt)
	set(b2, "bank/b", "2")
	set(b2, "bank/a", "1")
	blt.Commit(b2.Ops())

	if !bytes.Equal(mem.AppHash(), blt.AppHash()) {
		t.Fatal("same state, different app hash across histories/backends")
	}
	// A different value changes it; restoring brings it back.
	h0 := mem.AppHash()
	c := NewCache(mem)
	set(c, "bank/a", "other")
	if bytes.Equal(mem.AppHashAfter(c.Ops()), h0) {
		t.Fatal("hash ignores value change")
	}
	c2 := NewCache(mem)
	c2.Delete([]byte("bank/a"))
	set(c2, "bank/a", "1")
	if !bytes.Equal(mem.AppHashAfter(c2.Ops()), h0) {
		t.Fatal("idempotent rewrite changed hash")
	}
	// Preview must equal the post-commit hash.
	pre := mem.AppHashAfter(c.Ops())
	mem.Commit(c.Ops())
	if !bytes.Equal(pre, mem.AppHash()) {
		t.Fatal("AppHashAfter != AppHash after Commit")
	}
	// Key/value boundary: ("a/bc","d") vs ("a/b","cd") must differ.
	m1, m2 := NewMemDB(), NewMemDB()
	x := NewCache(m1)
	set(x, "a/bc", "d")
	m1.Commit(x.Ops())
	y := NewCache(m2)
	set(y, "a/b", "cd")
	m2.Commit(y.Ops())
	if bytes.Equal(m1.AppHash(), m2.AppHash()) {
		t.Fatal("key/value boundary ambiguity")
	}
}

func TestUnderscoreBucketsNotHashed(t *testing.T) {
	for name, db := range dbs(t) {
		t.Run(name, func(t *testing.T) {
			h0 := db.AppHash()
			c := NewCache(db)
			set(c, "_blocks/1", "hdr")
			set(c, "_meta/height", "1")
			if err := db.Commit(c.Ops()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(h0, db.AppHash()) {
				t.Fatal("bookkeeping bucket changed app hash")
			}
			c = NewCache(db)
			set(c, "bank/a", "1")
			db.Commit(c.Ops())
			if bytes.Equal(h0, db.AppHash()) {
				t.Fatal("state bucket did not change app hash")
			}
		})
	}
}

func TestBoltPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	b, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCache(b)
	set(c, "bank/a", "1")
	set(c, "storage/deal/1", "{}")
	b.Commit(c.Ops())
	h := b.AppHash()
	b.Close()
	b, err = OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if string(b.Get([]byte("bank/a"))) != "1" || !bytes.Equal(h, b.AppHash()) {
		t.Fatal("state or app hash lost on reopen")
	}
}

func TestCommitTouchesOnlyChangedKeys(t *testing.T) {
	b, err := OpenBolt(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := NewCache(b)
	for i := 0; i < 2000; i++ {
		set(c, fmt.Sprintf("bank/%05d", i), "v")
	}
	b.Commit(c.Ops())
	c = NewCache(b)
	set(c, "bank/00007", "w")
	if c.Len() != 1 || len(c.Ops()) != 1 {
		t.Fatal("write-set not proportional to changes")
	}
	// Commit cost is independent of the 2000 existing keys: the hash is updated
	// incrementally from the single op.
	b.Commit(c.Ops())
	if string(b.Get([]byte("bank/00007"))) != "w" || string(b.Get([]byte("bank/00008"))) != "v" {
		t.Fatal("bad commit")
	}
}

func TestMalformedKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewCache(NewMemDB()).Set([]byte("nobucket"), []byte("v"))
}
