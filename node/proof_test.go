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
	app    *App
	alice  testAcct
	provs  []testAcct
	prov   testAcct // provs[0]
	man    storage.MsgCommitManifest
	data   []byte
	leaves []chunk.Hash
}

type envOpts struct {
	providers   int
	replication uint32
	minReplicas uint32
	duration    int64
	pin         bool  // pin provs[0] on the open-deal message
	accept      bool  // every assigned provider accepts at block 3
	acceptTO    int64 // AcceptTimeoutBlocks (0 = default)
}

// newProofEnv is the classic single-provider deal (pinned, accepted at block 3).
func newProofEnv(t *testing.T, duration int64) *proofEnv {
	return newEnv(t, envOpts{providers: 1, replication: 1, duration: duration, pin: true, accept: true})
}

// newEnv commits a 5-chunk object and registers providers (block 1), opens a
// deal (block 2), and optionally has every assigned provider accept (block 3).
// Epochs are 5 blocks; 2 consecutive misses fail a replica.
func newEnv(t *testing.T, o envOpts) *proofEnv {
	t.Helper()
	alice := newAcct(t)
	var provs []testAcct
	for i := 0; i < o.providers; i++ {
		provs = append(provs, newAcct(t))
	}
	return newEnvWith(t, o, alice, provs)
}

func newEnvWith(t *testing.T, o envOpts, alice testAcct, provs []testAcct) *proofEnv {
	t.Helper()
	e := &proofEnv{alice: alice, provs: provs}
	accts := append([]testAcct{alice}, provs...)
	e.prov = e.provs[0]
	g := testGenesis(false, accts...)
	g.Storage.BlockInterval = 5
	g.Storage.MaxMissedProofs = 2
	g.Storage.AcceptTimeoutBlocks = o.acceptTO
	e.app = memApp(t, g)
	e.data = bytes.Repeat([]byte("vaultchain proof data "), 1000)[:5*storage.MinChunkSize-17]
	m, leaves, err := chunk.BuildBytes(e.data, storage.MinChunkSize, "text/plain", "p.txt")
	if err != nil {
		t.Fatal(err)
	}
	e.leaves = leaves
	e.man = storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size, ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name}
	txs := [][]byte{alice.tx(t, testChain, 0, nil, e.man)}
	for _, p := range e.provs {
		txs = append(txs, p.tx(t, testChain, 0, nil, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: "http://" + p.addr[:8], Capacity: 1 << 30, Collateral: uv(1_000_000)[0]}))
	}
	exec(t, e.app, "", txs...) // block 1
	open := storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: e.man.Root,
		Duration: o.duration, Denom: "uvault", Replication: o.replication, MinReplicas: o.minReplicas}
	if o.pin {
		open.Provider = e.prov.addr
	}
	r := exec(t, e.app, "", alice.tx(t, testChain, 1, nil, open)).Results[0] // block 2
	if !r.OK {
		t.Fatalf("open deal: %s", r.Log)
	}
	if o.accept {
		e.acceptAll(t)
	}
	return e
}

func (e *proofEnv) acct(addr string) testAcct {
	for _, p := range e.provs {
		if p.addr == addr {
			return p
		}
	}
	panic("unknown provider " + addr)
}

// acceptAll sends MsgAcceptDeal from every provider with an assigned replica,
// in one block.
func (e *proofEnv) acceptAll(t *testing.T) {
	t.Helper()
	var txs [][]byte
	for _, r := range e.deal(t).Replicas {
		if r.Status == storage.ReplicaAssigned {
			p := e.acct(r.Provider)
			txs = append(txs, p.tx(t, testChain, e.app.Nonce(p.addr), nil, storage.MsgAcceptDeal{Type: storage.MsgTypeAcceptDeal, DealID: 1}))
		}
	}
	for _, res := range exec(t, e.app, "", txs...).Results {
		if !res.OK {
			t.Fatalf("accept failed: %s", res.Log)
		}
	}
}

func (e *proofEnv) accept(t *testing.T, p testAcct) TxResult {
	t.Helper()
	return runOne(t, e.app, p, storage.MsgAcceptDeal{Type: storage.MsgTypeAcceptDeal, DealID: 1})
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

// replica returns provider's most recent replica in deal 1.
func (e *proofEnv) replica(t *testing.T, addr string) (r storage.Replica) {
	t.Helper()
	for _, x := range e.deal(t).Replicas {
		if x.Provider == addr {
			r = x
		}
	}
	return
}

func (e *proofEnv) challenge(t *testing.T) (storage.Challenge, error) {
	return e.challengeFor(t, e.prov.addr)
}

func (e *proofEnv) challengeFor(t *testing.T, addr string) (ch storage.Challenge, err error) {
	t.Helper()
	_ = e.app.View(func(s kv.Store) error { ch, err = storage.NewKeeper(s).Challenge(1, addr); return nil })
	return
}

func (e *proofEnv) providerRec(t *testing.T, addr string) (p storage.Provider) {
	t.Helper()
	_ = e.app.View(func(s kv.Store) error { p, _ = storage.NewKeeper(s).Provider(addr); return nil })
	return
}

func (e *proofEnv) provider(t *testing.T) storage.Provider { return e.providerRec(t, e.prov.addr) }

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

// proveAll has every active replica (except those in skip) answer its current
// challenge, all in one block. It is a no-op when nobody has a pending challenge.
func (e *proofEnv) proveAll(t *testing.T, skip ...string) {
	t.Helper()
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	var txs [][]byte
	for _, r := range e.deal(t).Replicas {
		if r.Status != storage.ReplicaActive || skipSet[r.Provider] {
			continue
		}
		ch, err := e.challengeFor(t, r.Provider)
		if err != nil || r.LastProvenEpoch == ch.Epoch {
			continue
		}
		p := e.acct(r.Provider)
		txs = append(txs, p.tx(t, testChain, e.app.Nonce(p.addr), nil, e.proofMsg(t, ch, false)))
	}
	if len(txs) == 0 {
		return
	}
	for _, res := range exec(t, e.app, "", txs...).Results {
		if !res.OK {
			t.Fatalf("proof rejected: %s", res.Log)
		}
	}
}

func TestChallengeIsDeterministicAndSpreads(t *testing.T) {
	seen := map[uint64]bool{}
	for i := 0; i < 64; i++ {
		seed := hex.EncodeToString([]byte{byte(i), 1, 2})
		a := storage.ChallengeIndex(seed, 7, "vault1aaa", 5)
		if a != storage.ChallengeIndex(seed, 7, "vault1aaa", 5) || a >= 5 {
			t.Fatalf("index not deterministic/in range: %d", a)
		}
		seen[a] = true
	}
	if len(seen) != 5 {
		t.Fatalf("indexes do not cover all chunks: %v", seen)
	}
	// Different providers get different questions (for most seeds).
	diff := 0
	for i := 0; i < 64; i++ {
		seed := hex.EncodeToString([]byte{byte(i), 9})
		if storage.ChallengeIndex(seed, 7, "vault1aaa", 50) != storage.ChallengeIndex(seed, 7, "vault1bbb", 50) {
			diff++
		}
	}
	if diff < 50 {
		t.Fatalf("challenge does not depend on the provider (%d/64 differ)", diff)
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
	rep := e.replica(t, e.prov.addr)
	if rep.LastProvenEpoch != 5 || rep.LastProvenH == 0 || rep.MissedProofs != 0 {
		t.Fatalf("replica not updated: %+v", rep)
	}
	if r := e.submit(t, good); r.OK {
		t.Fatal("second proof in the same epoch accepted")
	}
}

func TestMissedProofsFailReplicaRefundAndSlash(t *testing.T) {
	e := newProofEnv(t, 1000)
	d := e.deal(t)
	ownerBefore := bal(t, e.app, e.alice.addr)
	collBefore := e.provider(t).Collateral.Amount.Int64()
	e.advanceTo(t, 5) // epoch 5
	e.advanceTo(t, 10)
	if r := e.replica(t, e.prov.addr); r.MissedProofs != 1 || e.deal(t).Status != "open" {
		t.Fatalf("after one missed epoch: %+v", r)
	}
	e.advanceTo(t, 15)
	d = e.deal(t)
	if d.Status != "failed" || d.Replicas[0].Status != storage.ReplicaFailed {
		t.Fatalf("deal not failed (no replacement available): %+v", d)
	}
	slash := collBefore / 10
	if got := bal(t, e.app, e.alice.addr); got != ownerBefore+d.Payment.Amount.Int64()+slash {
		t.Fatalf("owner balance %d, want %d", got, ownerBefore+d.Payment.Amount.Int64()+slash)
	}
	p := e.provider(t)
	if p.Collateral.Amount.Int64() != collBefore-slash || p.FreeBytes != p.Capacity {
		t.Fatalf("provider not slashed/freed exactly once: %+v", p)
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
	if r := e.replica(t, e.prov.addr); r.MissedProofs != 0 {
		t.Fatalf("a proven epoch counted as missed: %+v", r)
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

// --- M7: multi-provider replication -----------------------------------------

func assignedSet(d storage.Deal) map[string]bool {
	out := map[string]bool{}
	for _, r := range d.Replicas {
		out[r.Provider] = true
	}
	return out
}

func TestReplicationAssignsDistinctProvidersAndEscrowsPerReplica(t *testing.T) {
	e := newEnv(t, envOpts{providers: 5, replication: 3, duration: 1000})
	d := e.deal(t)
	if d.Status != storage.DealPending || len(d.Replicas) != 3 || len(assignedSet(d)) != 3 {
		t.Fatalf("want 3 distinct pending replicas: %+v", d)
	}
	if d.Payment.Amount.Int64() != 3*d.PerReplica.Amount.Int64() || d.Replication != 3 || d.MinReplicas != 3 {
		t.Fatalf("payment/replication wrong: %+v", d)
	}
	reserved := 0
	for _, p := range e.provs {
		if e.providerRec(t, p.addr).FreeBytes < 1<<30 {
			reserved++
			if !assignedSet(d)[p.addr] {
				t.Fatal("capacity reserved on an unassigned provider")
			}
		}
	}
	if reserved != 3 {
		t.Fatalf("capacity reserved on %d providers, want 3", reserved)
	}
	// Accepting 2 of 3 keeps the deal pending; the third opens it.
	for i, r := range d.Replicas {
		res := e.accept(t, e.acct(r.Provider))
		if !res.OK {
			t.Fatalf("accept %d: %s", i, res.Log)
		}
		want := storage.DealPending
		if i == 2 {
			want = storage.DealOpen
		}
		if got := e.deal(t).Status; got != want {
			t.Fatalf("after %d accepts: %s, want %s", i+1, got, want)
		}
	}
}

func TestAssignmentIsDeterministic(t *testing.T) {
	// Same keys, same blocks => same app hash seed => same providers chosen, so
	// every validator replaying the chain agrees on who stores what.
	alice := newAcct(t)
	var provs []testAcct
	for i := 0; i < 8; i++ {
		provs = append(provs, newAcct(t))
	}
	o := envOpts{providers: 8, replication: 3, duration: 100}
	a := newEnvWith(t, o, alice, provs).deal(t)
	b := newEnvWith(t, o, alice, provs).deal(t)
	for i := range a.Replicas {
		if a.Replicas[i].Provider != b.Replicas[i].Provider {
			t.Fatalf("assignment differs: %+v vs %+v", a.Replicas, b.Replicas)
		}
	}
}
func TestPinnedProviderAndInsufficientProviders(t *testing.T) {
	e := newEnv(t, envOpts{providers: 4, replication: 2, duration: 100, pin: true})
	if d := e.deal(t); d.Replicas[0].Provider != e.prov.addr {
		t.Fatalf("pinned provider must hold the first replica: %+v", d.Replicas)
	}
	// 3 replicas requested from 2 registered providers fails atomically.
	alice := newAcct(t)
	p1, p2 := newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice, p1, p2))
	man := manifestFor(t, objA, "text/plain", "a.txt")
	exec(t, app, "", alice.tx(t, testChain, 0, nil, man),
		p1.tx(t, testChain, 0, nil, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p1", Capacity: 1 << 30, Collateral: uv(1000)[0]}),
		p2.tx(t, testChain, 0, nil, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p2", Capacity: 1 << 30, Collateral: uv(1000)[0]}))
	before := bal(t, app, alice.addr)
	r := runOne(t, app, alice, storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: man.Root, Duration: 50, Denom: "uvault", Replication: 3})
	if r.OK {
		t.Fatal("deal opened without enough providers")
	}
	if got := bal(t, app, alice.addr); got != before {
		t.Fatalf("failed open-deal changed balance: %d -> %d", before, got)
	}
}

func TestOnlyAssignedProviderMayAcceptAndNotAfterDeadline(t *testing.T) {
	e := newEnv(t, envOpts{providers: 4, replication: 1, duration: 100, acceptTO: 4}) // deadline = 2+4 = 6
	d := e.deal(t)
	var outsider testAcct
	for _, p := range e.provs {
		if !assignedSet(d)[p.addr] {
			outsider = p
		}
	}
	if r := e.accept(t, outsider); r.OK {
		t.Fatal("unassigned provider accepted a deal")
	}
	if r := e.accept(t, e.alice); r.OK {
		t.Fatal("non-provider accepted a deal")
	}
	e.advanceTo(t, 5)
	if r := e.accept(t, e.acct(d.Replicas[0].Provider)); r.OK { // executes at block 6 = deadline
		t.Fatal("accept at the deadline succeeded")
	}
}

func TestAcceptTimeoutRefundsAndFreesCapacity(t *testing.T) {
	e := newEnv(t, envOpts{providers: 3, replication: 2, duration: 100, acceptTO: 4}) // deadline 6
	before := bal(t, e.app, e.alice.addr)
	d := e.deal(t)
	e.advanceTo(t, 5)
	if e.deal(t).Status != storage.DealPending {
		t.Fatal("deal should still be pending before the deadline")
	}
	e.advanceTo(t, 6)
	d = e.deal(t)
	if d.Status != storage.DealCancelled {
		t.Fatalf("deal not cancelled: %+v", d)
	}
	if got := bal(t, e.app, e.alice.addr); got != before+d.Payment.Amount.Int64() {
		t.Fatalf("owner not refunded: %d vs %d", got, before+d.Payment.Amount.Int64())
	}
	for _, p := range e.provs {
		if r := e.providerRec(t, p.addr); r.FreeBytes != r.Capacity {
			t.Fatalf("capacity not freed: %+v", r)
		}
	}
	for _, r := range d.Replicas {
		if r.Status != storage.ReplicaDeclined {
			t.Fatalf("replica not declined: %+v", r)
		}
	}
}

func TestQuorumAtTimeoutOpensDealAndRefundsMissingSlot(t *testing.T) {
	e := newEnv(t, envOpts{providers: 3, replication: 2, minReplicas: 1, duration: 20, acceptTO: 4}) // expires 22
	d := e.deal(t)
	good, slow := e.acct(d.Replicas[0].Provider), e.acct(d.Replicas[1].Provider)
	if r := e.accept(t, good); !r.OK {
		t.Fatal(r.Log)
	}
	if e.deal(t).Status != storage.DealOpen {
		t.Fatal("deal should open at quorum (1 of 2)")
	}
	e.advanceTo(t, 6) // slow replica's deadline
	if r := e.replica(t, slow.addr); r.Status != storage.ReplicaDeclined {
		t.Fatalf("late replica not declined: %+v", r)
	}
	if got := e.providerRec(t, slow.addr); got.FreeBytes != got.Capacity {
		t.Fatal("declined replica kept capacity")
	}
	// The open deal tries to repair the empty slot at the next epoch (the third
	// provider is eligible); it never accepts, so it ends up declined too.
	for h := int64(5); h <= 20; h += 5 {
		e.advanceTo(t, h)
		e.proveAll(t)
	}
	goodBefore, ownerBefore := bal(t, e.app, good.addr), bal(t, e.app, e.alice.addr)
	e.advanceTo(t, 22)
	d = e.deal(t)
	if d.Status != storage.DealClosed {
		t.Fatalf("deal not closed: %+v", d)
	}
	if bal(t, e.app, good.addr) != goodBefore+d.PerReplica.Amount.Int64() {
		t.Fatal("proving provider not paid its share")
	}
	if bal(t, e.app, e.alice.addr) != ownerBefore+d.PerReplica.Amount.Int64() {
		t.Fatal("owner not refunded the unfilled slot")
	}
}

func TestPerReplicaChallengesAndPaymentSplit(t *testing.T) {
	e := newEnv(t, envOpts{providers: 3, replication: 3, duration: 12, accept: true}) // expires 14
	d := e.deal(t)
	if d.Status != storage.DealOpen {
		t.Fatalf("deal not open after all accepted: %+v", d)
	}
	a, b, c := d.Replicas[0].Provider, d.Replicas[1].Provider, d.Replicas[2].Provider
	e.advanceTo(t, 5)
	chA, _ := e.challengeFor(t, a)
	// A provider cannot answer another provider's challenge slot: proofs are checked
	// against the sender's own replica and challenge.
	if r := runOne(t, e.app, e.acct(b), e.proofMsg(t, chA, false)); r.OK {
		chB, _ := e.challengeFor(t, b)
		if chB.Index != chA.Index {
			t.Fatal("provider B satisfied provider A's challenge")
		}
	}
	e.proveAll(t, c) // C stays silent
	ownerBefore := bal(t, e.app, e.alice.addr)
	aBefore, bBefore, cBefore := bal(t, e.app, a), bal(t, e.app, b), bal(t, e.app, c)
	collC := e.providerRec(t, c).Collateral.Amount.Int64()
	e.advanceTo(t, 14)
	d = e.deal(t)
	if d.Status != storage.DealClosed {
		t.Fatalf("deal not closed: %+v", d)
	}
	per := d.PerReplica.Amount.Int64()
	if bal(t, e.app, a) != aBefore+per || bal(t, e.app, b) != bBefore+per {
		t.Fatal("proving providers not paid their share")
	}
	slash := collC / 10
	if bal(t, e.app, c) != cBefore {
		t.Fatal("silent provider was paid")
	}
	if got := bal(t, e.app, e.alice.addr); got != ownerBefore+per+slash {
		t.Fatalf("owner got %d, want %d (share + slash)", got-ownerBefore, per+slash)
	}
	if r := e.replica(t, c); r.Status != storage.ReplicaFailed {
		t.Fatalf("silent replica: %+v", r)
	}
	for _, p := range e.provs {
		if r := e.providerRec(t, p.addr); r.FreeBytes != r.Capacity {
			t.Fatalf("capacity not released for %s: %+v", p.addr, r)
		}
	}
}

func TestFailedReplicaIsReplacedAndReplacementIsPaid(t *testing.T) {
	e := newEnv(t, envOpts{providers: 4, replication: 3, duration: 40, accept: true}) // expires 42
	d := e.deal(t)
	bad := d.Replicas[0].Provider
	var spare string
	for _, p := range e.provs {
		if !assignedSet(d)[p.addr] {
			spare = p.addr
		}
	}
	collBad := e.providerRec(t, bad).Collateral.Amount.Int64()
	for h := int64(5); h <= 15; h += 5 {
		e.advanceTo(t, h)
		e.proveAll(t, bad)
	}
	// After the 2nd consecutive miss (height 15) the bad replica failed and the
	// spare provider was assigned to the slot.
	if r := e.replica(t, bad); r.Status != storage.ReplicaFailed {
		t.Fatalf("bad replica not failed: %+v", r)
	}
	repl := e.replica(t, spare)
	if repl.Status != storage.ReplicaAssigned || repl.Slot != d.Replicas[0].Slot {
		t.Fatalf("spare not assigned to the failed slot: %+v", repl)
	}
	if got := e.providerRec(t, bad); got.Collateral.Amount.Int64() != collBad-collBad/10 || got.FreeBytes != got.Capacity {
		t.Fatalf("bad provider not slashed/freed: %+v", got)
	}
	// The spare accepts (the provider binary copies the bytes first), then proves.
	if r := e.accept(t, e.acct(spare)); !r.OK {
		t.Fatalf("replacement accept: %s", r.Log)
	}
	spareBefore := bal(t, e.app, spare)
	for h := int64(20); h <= 40; h += 5 {
		e.advanceTo(t, h)
		e.proveAll(t, bad)
	}
	e.advanceTo(t, 42)
	d = e.deal(t)
	if d.Status != storage.DealClosed {
		t.Fatalf("deal not closed: %+v", d)
	}
	if bal(t, e.app, spare) != spareBefore+d.PerReplica.Amount.Int64() {
		t.Fatal("replacement not paid its slot's share")
	}
	if r := e.replica(t, spare); r.Status != storage.ReplicaDone || r.LastProvenEpoch == 0 {
		t.Fatalf("replacement: %+v", r)
	}
}

func TestDealExpiresWhilePendingWithoutAcceptCancels(t *testing.T) {
	e := newEnv(t, envOpts{providers: 1, replication: 1, duration: 3, acceptTO: 100}) // deadline clamped to expiry 5
	if r := e.deal(t).Replicas[0]; r.AcceptDeadlineH != 5 {
		t.Fatalf("deadline not clamped to expiry: %+v", r)
	}
	before := bal(t, e.app, e.alice.addr)
	e.advanceTo(t, 5)
	d := e.deal(t)
	if d.Status != storage.DealCancelled || bal(t, e.app, e.alice.addr) != before+d.Payment.Amount.Int64() {
		t.Fatalf("pending deal at expiry: %+v", d)
	}
}
