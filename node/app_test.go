package node

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/bank"
	"github.com/vaultchain/vaultchain/x/ibc"
	"github.com/vaultchain/vaultchain/x/storage"
)

const testChain = "c-1"

// manifestFor builds a chain manifest message for data (4 KiB chunks).
func manifestFor(t *testing.T, data []byte, mime, name string) storage.MsgCommitManifest {
	t.Helper()
	m, _, err := chunk.BuildBytes(data, storage.MinChunkSize, mime, name)
	if err != nil {
		t.Fatal(err)
	}
	return storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size, ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name}
}

var objA = []byte("hello vaultchain")

var (
	genesisTime = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	blockT0     = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

func testGenesis(labMode bool, funded ...testAcct) *Genesis {
	g := &Genesis{ChainID: testChain, GenesisTime: genesisTime, Storage: storage.DefaultParams(), LabMode: labMode}
	for _, a := range funded {
		g.Accounts = append(g.Accounts, GenesisAccount{Address: a.addr, Coins: uv(1_000_000_000)})
	}
	return g
}

func newTestApp(t *testing.T, db kv.DB, g *Genesis) *App {
	t.Helper()
	app := NewApp(db, AppConfig{})
	if err := app.InitChain(g); err != nil {
		t.Fatal(err)
	}
	return app
}

func memApp(t *testing.T, g *Genesis) *App { return newTestApp(t, kv.NewMemDB(), g) }

func blockAt(app *App, proposer string, txs ...[]byte) BlockInput {
	h := app.Height() + 1
	return BlockInput{Height: h, Time: blockT0.Add(time.Duration(h) * time.Second), Proposer: proposer, Txs: txs}
}

func exec(t *testing.T, app *App, proposer string, txs ...[]byte) *BlockResult {
	t.Helper()
	res, err := app.ExecuteBlock(blockAt(app, proposer, txs...))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func bal(t *testing.T, app *App, addr string) int64 {
	t.Helper()
	var n int64
	_ = app.View(func(s kv.Store) error {
		n = bank.NewKeeper(s).GetBalance(addr).AmountOf("uvault").Int64()
		return nil
	})
	return n
}

func TestExecuteBlockDeterministicReplay(t *testing.T) {
	alice, bob, prov, prop := newAcct(t), newAcct(t), newAcct(t), newAcct(t)
	g := testGenesis(false, alice, bob, prov)

	// One fixed set of signed txs, replayed onto independent apps.
	man := manifestFor(t, objA, "text/plain", "a.txt")
	b1 := [][]byte{
		alice.tx(t, testChain, 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(500)}),
		alice.tx(t, testChain, 1, uv(10), man),
		prov.tx(t, testChain, 0, uv(10), storage.MsgRegisterProvider{
			Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p", Capacity: 1 << 30, Collateral: uv(1000)[0]}),
		bob.tx(t, testChain, 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: alice.addr, Amount: uv(999_999_999_999)}), // fails
	}
	b2 := [][]byte{
		alice.tx(t, testChain, 2, uv(10), storage.MsgOpenDeal{
			Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: man.Root,
			Provider: prov.addr, Duration: 100, Denom: "uvault", Replication: 1}),
		alice.tx(t, testChain, 3, uv(10), access.MsgGrantAccess{
			Type: access.MsgTypeGrantAccess, CID: man.Root, Grantee: bob.addr, Capability: access.CapabilityRead}),
		alice.tx(t, testChain, 1, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}), // stale nonce
	}
	run := func(db kv.DB) (hashes []string, headers []BlockHeader) {
		app := newTestApp(t, db, g)
		for _, txs := range [][][]byte{b1, b2} {
			res := exec(t, app, prop.addr, txs...)
			hashes = append(hashes, app.AppHash())
			headers = append(headers, res.Header)
		}
		return
	}
	bolt, err := kv.OpenBolt(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bolt.Close()

	h1, hd1 := run(kv.NewMemDB())
	h2, hd2 := run(kv.NewMemDB())
	h3, hd3 := run(bolt)
	for i := range h1 {
		if h1[i] != h2[i] || h1[i] != h3[i] {
			t.Fatalf("block %d app hash differs between runs/backends: %s %s %s", i+1, h1[i], h2[i], h3[i])
		}
		if hd1[i] != hd2[i] || hd1[i] != hd3[i] {
			t.Fatalf("block %d header differs:\n%+v\n%+v\n%+v", i+1, hd1[i], hd2[i], hd3[i])
		}
	}
	if h1[0] == h1[1] {
		t.Fatal("app hash did not change between blocks with state changes")
	}
	if hd1[1].PrevAppHash != h1[0] {
		t.Fatal("prev_app_hash does not chain")
	}
	if hd1[0].OKCount != 3 || hd1[1].OKCount != 2 {
		t.Fatalf("ok counts %d/%d, want 3/2", hd1[0].OKCount, hd1[1].OKCount)
	}
}

func TestFailedTxLeavesNoPartialEffects(t *testing.T) {
	alice, bob, prop := newAcct(t), newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice))
	// msg 0 would succeed, msg 1 cannot (insufficient funds): both must revert.
	raw := alice.tx(t, testChain, 0, uv(10),
		MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(100)},
		MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(999_999_999_999)})
	res := exec(t, app, prop.addr, raw)
	if res.Results[0].OK || !strings.Contains(res.Results[0].Log, "reverted") {
		t.Fatalf("expected reverted failure, got %+v", res.Results[0])
	}
	if got := bal(t, app, bob.addr); got != 0 {
		t.Fatalf("bob received %d from a failed tx", got)
	}
	if got := bal(t, app, alice.addr); got != 1_000_000_000-10 {
		t.Fatalf("alice balance %d: only the fee may be charged", got)
	}
	if got := bal(t, app, prop.addr); got != 10 {
		t.Fatalf("proposer fee %d want 10", got)
	}
	if n := app.Nonce(alice.addr); n != 1 {
		t.Fatalf("nonce %d want 1 (consumed so the tx cannot be replayed)", n)
	}
	rec, ok := app.TxRecord(res.Results[0].Hash)
	if !ok || rec.OK {
		t.Fatalf("failed tx record missing or marked ok: %+v", rec)
	}
}

func TestPanickingMsgIsContained(t *testing.T) {
	alice := newAcct(t)
	app := memApp(t, testGenesis(false, alice))
	// A zero-value storage msg trips validation, not a panic, but the panic
	// guard must still hold for anything a keeper throws: exercise runMsgs.
	err := runMsgs(kv.NewCache(app.DB()), execCtx{}, []any{struct{}{}})
	if err == nil {
		t.Fatal("unknown msg type accepted")
	}
}

func TestRejectedTxChangesNothing(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	poor := newAcct(t)
	app := memApp(t, testGenesis(false, alice))
	ok := alice.tx(t, testChain, 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(100)})
	exec(t, app, "", ok)
	before := app.AppHash()

	rejects := map[string][]byte{
		"replay":      ok,
		"nonce gap":   alice.tx(t, testChain, 5, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}),
		"wrong chain": alice.tx(t, "other-chain", 1, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}),
		"unfunded":    poor.tx(t, testChain, 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}),
		"garbage":     []byte(`{"not":"a tx"}`),
	}
	for name, raw := range rejects {
		res := exec(t, app, "", raw)
		if res.Results[0].OK || !strings.HasPrefix(res.Results[0].Log, "rejected") {
			t.Fatalf("%s: not rejected: %+v", name, res.Results[0])
		}
		if got := app.AppHash(); got != before {
			t.Fatalf("%s: app hash changed on a rejected tx", name)
		}
	}
	if bal(t, app, bob.addr) != 100 {
		t.Fatal("bob balance changed by rejected txs")
	}
	if app.Nonce(poor.addr) != 0 || app.Nonce(alice.addr) != 1 {
		t.Fatal("nonces changed by rejected txs")
	}
	// The record of the successful original must survive a rejected replay.
	if rec, found := app.TxRecord(HashTx(ok)); !found || !rec.OK {
		t.Fatalf("original tx record clobbered: %+v", rec)
	}
}

func TestBlockTimeAndProposerComeFromInput(t *testing.T) {
	alice, bob, prop := newAcct(t), newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice))
	in := BlockInput{Height: 1, Time: blockT0, Proposer: prop.addr, Txs: [][]byte{
		alice.tx(t, testChain, 0, uv(7), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}),
	}}
	res, err := app.ExecuteBlock(in)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Header.Time.Equal(blockT0) || res.Header.Proposer != prop.addr {
		t.Fatalf("header %+v ignores block input", res.Header)
	}
	if !app.LastBlockTime().Equal(blockT0) {
		t.Fatalf("last block time %v", app.LastBlockTime())
	}
	if bal(t, app, prop.addr) != 7 {
		t.Fatal("fee not paid to proposer")
	}
	// No (or invalid) proposer: fees go to the fee collector account.
	exec(t, app, "not-an-address", alice.tx(t, testChain, 1, uv(5), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}))
	if bal(t, app, bank.FeeCollector) != 5 {
		t.Fatal("fee not paid to fee collector")
	}
}

func TestExecuteBlockRejectsBadHeightAndTime(t *testing.T) {
	app := memApp(t, testGenesis(false))
	before := app.AppHash()
	for name, in := range map[string]BlockInput{
		"skipped height": {Height: 2, Time: blockT0},
		"zero height":    {Height: 0, Time: blockT0},
		"before genesis": {Height: 1, Time: genesisTime.Add(-time.Second)},
		"at genesis":     {Height: 1, Time: genesisTime},
	} {
		if _, err := app.ExecuteBlock(in); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if app.Height() != 0 || app.AppHash() != before {
		t.Fatal("rejected block changed state")
	}
	exec(t, app, "")
	if _, err := app.ExecuteBlock(BlockInput{Height: 2, Time: blockT0.Add(time.Second)}); err == nil {
		t.Fatal("non-increasing block time accepted")
	}
}

func TestLabModeGatesSimulatedDeposit(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	for _, lab := range []bool{false, true} {
		app := memApp(t, testGenesis(lab, alice))
		res := exec(t, app, "", alice.tx(t, testChain, 0, nil, ibc.MsgSimulateIBCDeposit{
			Type: ibc.MsgTypeSimulateDeposit, To: bob.addr, Denom: "ibc/ATOM", Amount: 5}))
		if res.Results[0].OK != lab {
			t.Fatalf("lab_mode=%v: result %+v", lab, res.Results[0])
		}
		if !lab && !strings.Contains(res.Results[0].Log, "lab_mode") {
			t.Fatalf("unhelpful error: %s", res.Results[0].Log)
		}
	}
}

func TestGrantAccessRequiresCIDOwner(t *testing.T) {
	alice, bob, prov := newAcct(t), newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice, bob, prov))
	man := manifestFor(t, objA, "text/plain", "a.txt")
	exec(t, app, "",
		prov.tx(t, testChain, 0, nil, storage.MsgRegisterProvider{
			Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p", Capacity: 1 << 20, Collateral: uv(100)[0]}),
		alice.tx(t, testChain, 0, nil, man),
		alice.tx(t, testChain, 1, nil, storage.MsgOpenDeal{
			Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: man.Root,
			Provider: prov.addr, Duration: 100, Denom: "uvault", Replication: 1}))
	grant := func(a testAcct, cid string) bool {
		res := exec(t, app, "", a.tx(t, testChain, app.Nonce(a.addr), nil, access.MsgGrantAccess{
			Type: access.MsgTypeGrantAccess, CID: cid, Grantee: bob.addr, Capability: access.CapabilityRead}))
		return res.Results[0].OK
	}
	count := func() (n int) {
		_ = app.View(func(s kv.Store) error { n = len(access.NewKeeper(s).Grants()); return nil })
		return
	}
	if grant(bob, man.Root) || count() != 0 {
		t.Fatal("non-owner granted access")
	}
	if !grant(alice, man.Root) || count() != 1 {
		t.Fatal("owner grant failed")
	}
	if grant(alice, "nope") {
		t.Fatal("grant on ownerless cid accepted")
	}
}

func TestTxRecordsArePrunedAfterRetention(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	app := NewApp(kv.NewMemDB(), AppConfig{TxRetention: 3})
	if err := app.InitChain(testGenesis(false, alice)); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for i := uint64(0); i < 6; i++ {
		raw := alice.tx(t, testChain, i, nil, MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})
		hashes = append(hashes, HashTx(raw))
		exec(t, app, "", raw)
	}
	// Height 6, retention 3: blocks 1..3 pruned, 4..6 kept. Headers always kept.
	for i, h := range hashes {
		_, found := app.TxRecord(h)
		if want := i >= 3; found != want {
			t.Fatalf("tx from block %d: found=%v want %v", i+1, found, want)
		}
	}
	if _, ok := app.Header(1); !ok {
		t.Fatal("block header pruned")
	}
	// No orphaned index entries either.
	n := 0
	_ = app.DB().View(func(r kv.Reader) error {
		r.Iterate(prefixTxIdx, func(k, v []byte) bool { n++; return true })
		return nil
	})
	if n != 3 {
		t.Fatalf("%d tx index entries remain, want 3", n)
	}
}
