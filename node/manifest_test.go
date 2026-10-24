package node

import (
	"strings"
	"testing"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/x/storage"
)

func runOne(t *testing.T, app *App, a testAcct, msg any) TxResult {
	t.Helper()
	return exec(t, app, "", a.tx(t, testChain, app.Nonce(a.addr), nil, msg)).Results[0]
}

func manifestOf(t *testing.T, app *App, root string) (rec storage.ManifestRecord, ok bool) {
	t.Helper()
	_ = app.View(func(s kv.Store) error { rec, ok = storage.NewKeeper(s).Manifest(root); return nil })
	return
}

func TestCommitManifestOwnerIsSignerAndFirstWins(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice, bob))
	man := manifestFor(t, objA, "text/plain", "a.txt")
	if r := runOne(t, app, alice, man); !r.OK {
		t.Fatalf("commit failed: %s", r.Log)
	}
	rec, ok := manifestOf(t, app, man.Root)
	if !ok || rec.Owner != alice.addr || rec.Height != 1 || rec.Size != uint64(len(objA)) || rec.Mime != "text/plain" {
		t.Fatalf("bad record %+v", rec)
	}
	// A second commit of the same root (even by someone else) is refused and does not change ownership.
	if r := runOne(t, app, bob, man); r.OK || !strings.Contains(r.Log, "already committed") {
		t.Fatalf("duplicate commit: %+v", r)
	}
	if rec, _ := manifestOf(t, app, man.Root); rec.Owner != alice.addr {
		t.Fatal("ownership changed")
	}
}

func TestCommitManifestCaps(t *testing.T) {
	alice := newAcct(t)
	g := testGenesis(false, alice)
	g.Storage.MaxObjectBytes = 1000
	app := memApp(t, g)
	base := manifestFor(t, objA, "text/plain", "a.txt")
	root := base.Root
	tooBig := storage.MsgCommitManifest{Type: base.Type, Root: root, Size: 1001, ChunkSize: 4096, Mime: "text/plain"}
	cases := map[string]storage.MsgCommitManifest{
		"over size cap": tooBig,
		"zero size":     {Type: base.Type, Root: root, Size: 0, ChunkSize: 4096, Mime: "text/plain"},
		"tiny chunks":   {Type: base.Type, Root: root, Size: 10, ChunkSize: 64, Mime: "text/plain"},
		"huge chunks":   {Type: base.Type, Root: root, Size: 10, ChunkSize: chunk.MaxChunkSize + 1, Mime: "text/plain"},
		"bad mime":      {Type: base.Type, Root: root, Size: 10, ChunkSize: 4096, Mime: "Text/HTML; x=y"},
		"empty mime":    {Type: base.Type, Root: root, Size: 10, ChunkSize: 4096},
		"long mime":     {Type: base.Type, Root: root, Size: 10, ChunkSize: 4096, Mime: "a/" + strings.Repeat("b", 200)},
		"bad root":      {Type: base.Type, Root: "xyz", Size: 10, ChunkSize: 4096, Mime: "text/plain"},
		"path in name":  {Type: base.Type, Root: root, Size: 10, ChunkSize: 4096, Mime: "text/plain", Name: "../x"},
	}
	for name, m := range cases {
		if r := runOne(t, app, alice, m); r.OK {
			t.Errorf("%s accepted", name)
		}
	}
	if _, ok := manifestOf(t, app, root); ok {
		t.Fatal("rejected manifest was stored")
	}
	ok := storage.MsgCommitManifest{Type: base.Type, Root: root, Size: 10, ChunkSize: 4096, Mime: "text/plain"}
	if r := runOne(t, app, alice, ok); !r.OK {
		t.Fatalf("valid manifest rejected: %s", r.Log)
	}
}

func TestOpenDealRequiresCommittedManifestOfSameOwner(t *testing.T) {
	alice, bob, prov := newAcct(t), newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice, bob, prov))
	exec(t, app, "", prov.tx(t, testChain, 0, nil, storage.MsgRegisterProvider{
		Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p", Capacity: 1 << 20, Collateral: uv(100)[0]}))
	man := manifestFor(t, objA, "text/plain", "a.txt")
	deal := func(a testAcct, cid string, size uint64) TxResult {
		return runOne(t, app, a, storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: cid,
			Size: size, Provider: prov.addr, Duration: 100, Denom: "uvault"})
	}
	if r := deal(alice, man.Root, 0); r.OK || !strings.Contains(r.Log, "not committed") {
		t.Fatalf("deal without manifest: %+v", r)
	}
	if r := runOne(t, app, alice, man); !r.OK {
		t.Fatal(r.Log)
	}
	if r := deal(bob, man.Root, 0); r.OK || !strings.Contains(r.Log, "manifest owner") {
		t.Fatalf("non-owner deal: %+v", r)
	}
	if r := deal(alice, man.Root, 5); r.OK || !strings.Contains(r.Log, "does not match") {
		t.Fatalf("wrong size deal: %+v", r)
	}
	if r := deal(alice, man.Root, 0); !r.OK { // size defaults to the manifest size
		t.Fatalf("owner deal: %+v", r)
	}
	_ = app.View(func(s kv.Store) error {
		ds := storage.NewKeeper(s).Deals()
		if len(ds) != 1 || ds[0].Size != man.Size || ds[0].CID != man.Root {
			t.Fatalf("deals %+v", ds)
		}
		return nil
	})
}
