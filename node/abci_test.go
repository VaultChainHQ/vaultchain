package node

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/x/bank"
)

type abciHarness struct {
	t     *testing.T
	app   *App
	abci  *ABCIApp
	val   ed25519.PubKey
	valAc testAcct // fee account of the validator
	h     int64
}

func newABCIHarness(t *testing.T, minFee sdk.Coins, funded ...testAcct) *abciHarness {
	t.Helper()
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	app := NewApp(db, AppConfig{})
	hn := &abciHarness{t: t, app: app, abci: NewABCIApp(app, minFee), val: ed25519.GenPrivKey().PubKey().(ed25519.PubKey), valAc: newAcct(t)}
	g := testGenesis(false, funded...)
	g.Validators = []GenesisValidator{{
		Name: "v1", PubKey: base64.StdEncoding.EncodeToString(hn.val), Power: 10, Account: hn.valAc.addr,
	}}
	hn.initChain(g)
	return hn
}

func (hn *abciHarness) initChain(g *Genesis) {
	hn.t.Helper()
	state, err := json.Marshal(g)
	if err != nil {
		hn.t.Fatal(err)
	}
	if _, err := hn.abci.InitChain(context.Background(), &abci.RequestInitChain{ChainId: g.ChainID, Time: g.GenesisTime, AppStateBytes: state}); err != nil {
		hn.t.Fatal(err)
	}
}

func (hn *abciHarness) check(raw []byte) *abci.ResponseCheckTx {
	hn.t.Helper()
	res, err := hn.abci.CheckTx(context.Background(), &abci.RequestCheckTx{Tx: raw})
	if err != nil {
		hn.t.Fatal(err)
	}
	return res
}

// commit finalizes and commits one block at the next height.
func (hn *abciHarness) commit(at time.Time, txs ...[]byte) *abci.ResponseFinalizeBlock {
	hn.t.Helper()
	res, err := hn.finalize(at, txs...)
	if err != nil {
		hn.t.Fatal(err)
	}
	if _, err := hn.abci.Commit(context.Background(), &abci.RequestCommit{}); err != nil {
		hn.t.Fatal(err)
	}
	hn.h++
	return res
}

func (hn *abciHarness) finalize(at time.Time, txs ...[]byte) (*abci.ResponseFinalizeBlock, error) {
	return hn.abci.FinalizeBlock(context.Background(), &abci.RequestFinalizeBlock{
		Height: hn.h + 1, Time: at, Txs: txs, ProposerAddress: hn.val.Address(),
	})
}

func send(t *testing.T, from testAcct, to string, nonce uint64, fee, amt int64) []byte {
	t.Helper()
	return from.tx(t, testChain, nonce, uv(fee), MsgBankSend{Type: MsgTypeBankSend, To: to, Amount: uv(amt)})
}

func TestValidatorAddress(t *testing.T) {
	pub := ed25519.GenPrivKey().PubKey()
	got, err := ValidatorAddress(base64.StdEncoding.EncodeToString(pub.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if want := hex.EncodeToString(pub.Address()); got != want {
		t.Fatalf("address %s want %s", got, want)
	}
	for _, bad := range []string{"", "!!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := ValidatorAddress(bad); err == nil {
			t.Fatalf("accepted bad key %q", bad)
		}
	}
}

func TestABCIInitChainIdempotentAtHeightZero(t *testing.T) {
	alice := newAcct(t)
	hn := newABCIHarness(t, nil, alice)
	info, _ := hn.abci.Info(context.Background(), &abci.RequestInfo{})
	if info.LastBlockHeight != 0 || len(info.LastBlockAppHash) == 0 {
		t.Fatalf("info after init: %+v", info)
	}
	h0 := hex.EncodeToString(info.LastBlockAppHash)
	// A crash before block 1 makes CometBFT call InitChain again.
	hn.initChain(testGenesis(false, alice))
	if got := hn.app.AppHash(); got != h0 {
		t.Fatalf("re-init changed app hash: %s -> %s", h0, got)
	}
	state, _ := json.Marshal(testGenesis(false, alice))
	if _, err := hn.abci.InitChain(context.Background(), &abci.RequestInitChain{ChainId: "other", AppStateBytes: state}); err == nil {
		t.Fatal("accepted InitChain with a different chain id")
	}
	hn.commit(blockT0)
	if _, err := hn.abci.InitChain(context.Background(), &abci.RequestInitChain{ChainId: testChain, AppStateBytes: state}); err == nil {
		t.Fatal("InitChain accepted on a chain at height 1")
	}
}

func TestABCICheckTx(t *testing.T) {
	alice, bob, poor := newAcct(t), newAcct(t), newAcct(t)
	hn := newABCIHarness(t, mustCoins(t, "100uvault"), alice)
	expect := func(name string, raw []byte, ok bool, substr string) {
		t.Helper()
		res := hn.check(raw)
		if (res.Code == 0) != ok || (!ok && !strings.Contains(res.Log, substr)) {
			t.Fatalf("%s: code=%d log=%q (want ok=%v %q)", name, res.Code, res.Log, ok, substr)
		}
	}
	expect("first", send(t, alice, bob.addr, 0, 100, 1), true, "")
	expect("duplicate nonce", send(t, alice, bob.addr, 0, 100, 2), false, "bad nonce")
	expect("next nonce queued", send(t, alice, bob.addr, 1, 100, 1), true, "")
	expect("nonce gap", send(t, alice, bob.addr, 5, 100, 1), false, "bad nonce")
	expect("below min fee", send(t, alice, bob.addr, 2, 99, 1), false, "minimum")
	expect("wrong chain", alice.tx(t, "other", 2, uv(100), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}), false, "wrong chain")
	expect("cannot pay fee", send(t, poor, bob.addr, 0, 100, 1), false, "insufficient")
	expect("garbage", []byte("not json"), false, "")
	// Tampered body: signature no longer matches.
	good := send(t, alice, bob.addr, 2, 100, 1)
	bad := []byte(strings.Replace(string(good), `"nonce":2`, `"nonce":3`, 1))
	if string(bad) == string(good) {
		t.Fatal("test bug: tamper had no effect")
	}
	expect("bad signature", bad, false, "")
	if got := hn.abci.PendingNonce(alice.addr); got != 2 {
		t.Fatalf("pending nonce %d want 2", got)
	}
	// After a commit the pending view restarts from committed state.
	hn.commit(blockT0, send(t, alice, bob.addr, 0, 100, 1))
	if got := hn.abci.PendingNonce(alice.addr); got != 1 {
		t.Fatalf("pending nonce after commit %d want 1", got)
	}
	expect("recheck of included tx", send(t, alice, bob.addr, 0, 100, 1), false, "bad nonce")
	expect("recheck of next tx", send(t, alice, bob.addr, 1, 100, 1), true, "")
}

func TestABCIFinalizeCommitAppliesBlock(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	hn := newABCIHarness(t, nil, alice)
	ok := send(t, alice, bob.addr, 0, 10, 500)
	badNonce := send(t, alice, bob.addr, 7, 10, 1)
	res := hn.commit(blockT0, ok, badNonce)
	if len(res.TxResults) != 2 || res.TxResults[0].Code != 0 || res.TxResults[1].Code == 0 {
		t.Fatalf("tx results: %+v", res.TxResults)
	}
	if hn.app.Height() != 1 {
		t.Fatalf("height %d", hn.app.Height())
	}
	if got := hex.EncodeToString(res.AppHash); got != hn.app.AppHash() || got != hn.app.LatestHeader().AppHash {
		t.Fatalf("finalize app hash %s != committed %s", got, hn.app.AppHash())
	}
	info, _ := hn.abci.Info(context.Background(), &abci.RequestInfo{})
	if info.LastBlockHeight != 1 || hex.EncodeToString(info.LastBlockAppHash) != hn.app.AppHash() {
		t.Fatalf("info %+v", info)
	}
	_ = hn.app.View(func(s kv.Store) error {
		bk := bank.NewKeeper(s)
		if got := bk.GetBalance(bob.addr); got.AmountOf("uvault").Int64() != 500 {
			t.Fatalf("bob %s", got)
		}
		// The fee went to the proposing validator's account, found via its consensus address.
		if got := bk.GetBalance(hn.valAc.addr); got.AmountOf("uvault").Int64() != 10 {
			t.Fatalf("proposer fee account %s", got)
		}
		return nil
	})
	if hn.app.LatestHeader().Proposer != hn.valAc.addr {
		t.Fatalf("header proposer %q", hn.app.LatestHeader().Proposer)
	}
}

func TestABCIBlockTimeClamp(t *testing.T) {
	hn := newABCIHarness(t, nil)
	// Block 1 carries the genesis time, and equal times must not halt the chain.
	hn.commit(genesisTime)
	t1 := hn.app.LastBlockTime()
	if !t1.After(genesisTime) {
		t.Fatalf("block 1 time %s not after genesis %s", t1, genesisTime)
	}
	hn.commit(t1)
	t2 := hn.app.LastBlockTime()
	if !t2.After(t1) {
		t.Fatalf("block 2 time %s not after %s", t2, t1)
	}
	hn.commit(blockT0)
	if got := hn.app.LastBlockTime(); !got.Equal(blockT0) {
		t.Fatalf("normal block time %s want %s", got, blockT0)
	}
}

func TestABCICommitRequiresFinalize(t *testing.T) {
	hn := newABCIHarness(t, nil)
	if _, err := hn.abci.Commit(context.Background(), &abci.RequestCommit{}); err == nil {
		t.Fatal("commit without finalize succeeded")
	}
	// A finalized-but-uncommitted block leaves committed state untouched.
	if _, err := hn.finalize(blockT0); err != nil {
		t.Fatal(err)
	}
	if hn.app.Height() != 0 {
		t.Fatalf("finalize wrote state: height %d", hn.app.Height())
	}
	// FinalizeBlock for the wrong height is refused.
	if _, err := hn.abci.FinalizeBlock(context.Background(), &abci.RequestFinalizeBlock{Height: 5, Time: blockT0}); err == nil {
		t.Fatal("finalize accepted a height gap")
	}
}

// Two nodes fed the same blocks must reach the same app hash at every height,
// including storage end-block work (deals, challenges) that depends on state.
func TestABCITwoNodesAgree(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	a := newABCIHarness(t, nil, alice)
	g := testGenesis(false, alice)
	g.Validators = []GenesisValidator{{Name: "v1", PubKey: base64.StdEncoding.EncodeToString(a.val), Power: 10, Account: a.valAc.addr}}
	// Same validator key and fee account on both, so proposer routing matches.
	b := newABCIHarnessWithGenesis(t, g, a.val, a.valAc)
	m := manifestFor(t, objA, "text/plain", "a.txt")
	blocks := [][][]byte{
		{send(t, alice, bob.addr, 0, 10, 1000)},
		{alice.tx(t, testChain, 1, uv(10), m)},
		{},
		{send(t, alice, bob.addr, 2, 10, 1), send(t, alice, bob.addr, 3, 10, 1)},
	}
	for i, txs := range blocks {
		at := blockT0.Add(time.Duration(i) * 3 * time.Second)
		ra, rb := a.commit(at, txs...), b.commit(at, txs...)
		if string(ra.AppHash) != string(rb.AppHash) {
			t.Fatalf("height %d: app hashes diverge %x vs %x", i+1, ra.AppHash, rb.AppHash)
		}
	}
}

func newABCIHarnessWithGenesis(t *testing.T, g *Genesis, val ed25519.PubKey, valAc testAcct) *abciHarness {
	t.Helper()
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	app := NewApp(db, AppConfig{})
	hn := &abciHarness{t: t, app: app, abci: NewABCIApp(app, nil), val: val, valAc: valAc}
	hn.initChain(g)
	return hn
}
