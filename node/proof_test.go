package node

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/x/storage"
)

type proofEnv struct {
	app         *App
	alice, prov testAcct
	man         storage.MsgCommitManifest
	data        []byte
	leaves      []chunk.Hash
}

// newProofEnv commits a 5-chunk object, registers a provider, and opens a deal
// of the given duration (block 2). Epochs are 5 blocks; 2 misses fail a deal.
func newProofEnv(t *testing.T, duration int64) *proofEnv {
	t.Helper()
	alice, prov := newAcct(t), newAcct(t)
	g := testGenesis(false, alice, prov)
	g.Storage.BlockInterval = 5
	g.Storage.MaxMissedProofs = 2
	app := memApp(t, g)
	data := bytes.Repeat([]byte("vaultchain proof data "), 1000)[:5*storage.MinChunkSize-17]
	m, leaves, err := chunk.BuildBytes(data, storage.MinChunkSize, "text/plain", "p.txt")
	if err != nil {
		t.Fatal(err)
	}
	man := storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size, ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name}
	exec(t, app, "", // block 1
		alice.tx(t, testChain, 0, nil, man),
		prov.tx(t, testChain, 0, nil, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p", Capacity: 1 << 30, Collateral: uv(1_000_000)[0]}))
	r := exec(t, app, "", alice.tx(t, testChain, 1, nil, storage.MsgOpenDeal{ // block 2
		Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: man.Root, Provider: prov.addr,
		Duration: duration, Denom: "uvault", Replication: 1})).Results[0]
	if !r.OK {
		t.Fatalf("open deal: %s", r.Log)
	}
	return &proofEnv{app: app, alice: alice, prov: prov, man: man, data: data, leaves: leaves}
}

func (e *proofEnv) advanceTo(t *testing.T, h int64) {
	t.Helper()
	for e.app.Height() < h {
		exec(t, e.app, "")
	}
}

func (e *proofEnv) deal(t *testing.T) (d storage.Deal) {
	t.Helper()
	_ = e.app.View(func(s kv.Store) error { d, _ = storage.NewKeeper(s).Deal(1); return nil })
	return
}

func (e *proofEnv) challenge(t *testing.T) (ch storage.Challenge, err error) {
	t.Helper()
	_ = e.app.View(func(s kv.Store) error { ch, err = storage.NewKeeper(s).Challenge(1); return nil })
	return
}

func (e *proofEnv) provider(t *testing.T) (p storage.Provider) {
	t.Helper()
	_ = e.app.View(func(s kv.Store) error { p, _ = storage.NewKeeper(s).Provider(e.prov.addr); return nil })
	return
}

// proofMsg answers ch honestly (or with a tampered chunk).
func (e *proofEnv) proofMsg(t *testing.T, ch storage.Challenge, tamper bool) storage.MsgSubmitProof {
	t.Helper()
	cs := storage.MinChunkSize
	lo := int(ch.Index) * cs
	hi := min(lo+cs, len(e.data))
	data := append([]byte(nil), e.data[lo:hi]...)
	path, err := chunk.Prove(e.leaves, int(ch.Index))
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		data[0] ^= 1
	}
	var hexPath []string
	for _, s := range path {
		hexPath = append(hexPath, hex.EncodeToString(s))
	}
	return storage.MsgSubmitProof{Type: storage.MsgTypeSubmitProof, DealID: 1, CID: e.man.Root, Epoch: ch.Epoch, Chunk: data, Proof: hexPath}
}

func (e *proofEnv) submit(t *testing.T, msg storage.MsgSubmitProof) TxResult {
	t.Helper()
	return runOne(t, e.app, e.prov, msg)
}

func TestChallengeIsDeterministicAndSpreads(t *testing.T) {
	seen := map[uint64]bool{}
	for i := 0; i < 64; i++ {
		seed := hex.EncodeToString([]byte{byte(i), 1, 2})
		a := storage.ChallengeIndex(seed, 7, 5)
		if a != storage.ChallengeIndex(seed, 7, 5) || a >= 5 {
			t.Fatalf("index not deterministic/in range: %d", a)
		}
		seen[a] = true
	}
	if len(seen) != 5 {
		t.Fatalf("indexes do not cover all chunks: %v", seen)
	}
}

func TestProofAcceptedAndRejections(t *testing.T) {
	e := newProofEnv(t, 1000)
	if _, err := e.challenge(t); err == nil {
		t.Fatal("deal must not be challengeable before the first epoch")
	}
	e.advanceTo(t, 5) // epoch 5 starts at the end of block 5
	ch, err := e.challenge(t)
	if err != nil || ch.Epoch != 5 || ch.Root != e.man.Root || ch.Index >= 5 {
		t.Fatalf("challenge: %+v err=%v", ch, err)
	}

	good := e.proofMsg(t, ch, false)
	if r := e.submit(t, e.proofMsg(t, ch, true)); r.OK {
		t.Fatal("tampered chunk accepted")
	}
	wrongIdx := e.proofMsg(t, storage.Challenge{Epoch: ch.Epoch, Index: (ch.Index + 1) % 5}, false)
	if r := e.submit(t, wrongIdx); r.OK {
		t.Fatal("proof of a different chunk accepted")
	}
	stale := good
	stale.Epoch = 4
	if r := e.submit(t, stale); r.OK {
		t.Fatal("wrong epoch accepted")
	}
	if r := runOne(t, e.app, e.alice, good); r.OK {
		t.Fatal("proof from a non-provider accepted")
	}
	if r := e.submit(t, good); !r.OK {
		t.Fatalf("valid proof rejected: %s", r.Log)
	}
	d := e.deal(t)
	if d.LastProvenEpoch != 5 || d.LastProvenH == 0 || d.MissedProofs != 0 {
		t.Fatalf("deal not updated: %+v", d)
	}
	if r := e.submit(t, good); r.OK {
		t.Fatal("second proof in the same epoch accepted")
	}
}

func TestMissedProofsFailDealRefundAndSlash(t *testing.T) {
	e := newProofEnv(t, 1000)
	d := e.deal(t)
	ownerBefore := bal(t, e.app, e.alice.addr)
	collBefore := e.provider(t).Collateral.Amount.Int64()
	e.advanceTo(t, 5) // epoch 5
	e.advanceTo(t, 10)
	if d := e.deal(t); d.MissedProofs != 1 || d.Status != "open" {
		t.Fatalf("after one missed epoch: %+v", d)
	}
	e.advanceTo(t, 15)
	d = e.deal(t)
	if d.Status != "failed" {
		t.Fatalf("deal not failed: %+v", d)
	}
	slash := collBefore / 10
	if got := bal(t, e.app, e.alice.addr); got != ownerBefore+d.Payment.Amount.Int64()+slash {
		t.Fatalf("owner balance %d, want %d", got, ownerBefore+d.Payment.Amount.Int64()+slash)
	}
	p := e.provider(t)
	if p.Collateral.Amount.Int64() != collBefore-slash || p.FreeBytes != p.Capacity {
		t.Fatalf("provider not slashed/freed: %+v", p)
	}
	if _, err := e.challenge(t); err == nil {
		t.Fatal("failed deal still challengeable")
	}
}

func TestProvingResetsMissesAndExpiryPaysProvider(t *testing.T) {
	e := newProofEnv(t, 12) // expires at height 14
	prove := func() {
		ch, err := e.challenge(t)
		if err != nil {
			t.Fatal(err)
		}
		if r := e.submit(t, e.proofMsg(t, ch, false)); !r.OK {
			t.Fatalf("proof rejected: %s", r.Log)
		}
	}
	e.advanceTo(t, 5)
	prove() // block 6
	e.advanceTo(t, 10)
	if d := e.deal(t); d.MissedProofs != 0 {
		t.Fatalf("a proven epoch counted as missed: %+v", d)
	}
	e.advanceTo(t, 13)
	provBefore := bal(t, e.app, e.prov.addr)
	e.advanceTo(t, 14)
	d := e.deal(t)
	if d.Status != "closed" {
		t.Fatalf("deal not closed at expiry: %+v", d)
	}
	if got := bal(t, e.app, e.prov.addr); got != provBefore+d.Payment.Amount.Int64() {
		t.Fatalf("provider paid %d, want %d", got-provBefore, d.Payment.Amount.Int64())
	}
	if p := e.provider(t); p.FreeBytes != p.Capacity {
		t.Fatalf("capacity not released: %+v", p)
	}
}

func TestExpiryWithoutProofRefundsOwner(t *testing.T) {
	e := newProofEnv(t, 8) // expires at height 10; never proven
	before := bal(t, e.app, e.alice.addr)
	e.advanceTo(t, 10)
	d := e.deal(t)
	if d.Status != "failed" || bal(t, e.app, e.alice.addr) < before+d.Payment.Amount.Int64() {
		t.Fatalf("unproven expiry must refund: %+v", d)
	}
}
