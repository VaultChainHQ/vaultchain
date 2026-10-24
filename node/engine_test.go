package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestEngine(t *testing.T, g *Genesis) *Engine {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteGenesis(home, g); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(Config{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestSubmitTxTracksPendingNonces(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	e := newTestEngine(t, testGenesis(false, alice))
	send := func(nonce uint64) error {
		_, err := e.SubmitTx(alice.tx(t, testChain, nonce, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}))
		return err
	}
	if err := send(0); err != nil {
		t.Fatal(err)
	}
	if err := send(0); err == nil || !strings.Contains(err.Error(), "bad nonce") {
		t.Fatalf("duplicate nonce accepted by mempool: %v", err)
	}
	if err := send(1); err != nil {
		t.Fatalf("queued follow-up nonce rejected: %v", err)
	}
	if n, p := e.AccountNonces(alice.addr); n != 0 || p != 2 {
		t.Fatalf("nonces %d/%d want 0/2", n, p)
	}
	if _, err := e.SubmitTx(alice.tx(t, "other", 2, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})); err == nil {
		t.Fatal("wrong chain accepted by mempool")
	}
	poor := newAcct(t)
	if _, err := e.SubmitTx(poor.tx(t, testChain, 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})); err == nil {
		t.Fatal("unfundable fee accepted by mempool")
	}

	if err := e.produceBlock(); err != nil {
		t.Fatal(err)
	}
	if e.App().Height() != 1 || bal(t, e.App(), bob.addr) != 2 {
		t.Fatalf("block not applied: height %d bob %d", e.App().Height(), bal(t, e.App(), bob.addr))
	}
	if n, p := e.AccountNonces(alice.addr); n != 2 || p != 2 {
		t.Fatalf("post-block nonces %d/%d want 2/2", n, p)
	}
}

func TestProduceBlockTimesStrictlyIncrease(t *testing.T) {
	e := newTestEngine(t, testGenesis(false))
	var last time.Time
	for i := 0; i < 5; i++ {
		if err := e.produceBlock(); err != nil {
			t.Fatal(err)
		}
		cur := e.App().LastBlockTime()
		if !cur.After(last) {
			t.Fatalf("block %d time %v not after %v", i+1, cur, last)
		}
		last = cur
	}
}

func TestEngineRestartKeepsStateAndHash(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	g := testGenesis(false, alice)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, "config"), 0o755)
	if err := WriteGenesis(home, g); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(Config{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SubmitTx(alice.tx(t, testChain, 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(42)})); err != nil {
		t.Fatal(err)
	}
	if err := e.produceBlock(); err != nil {
		t.Fatal(err)
	}
	h, hash := e.App().Height(), e.App().AppHash()
	_ = e.Close()

	e2, err := NewEngine(Config{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if e2.App().Height() != h || e2.App().AppHash() != hash || bal(t, e2.App(), bob.addr) != 42 {
		t.Fatal("state or app hash changed across restart")
	}
}

func TestEngineRefusesLegacyDatabase(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, "config"), 0o755)
	if err := WriteGenesis(home, testGenesis(false)); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(home, "data"), 0o755)
	if err := os.WriteFile(filepath.Join(home, "data", legacyDBFile), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewEngine(Config{Home: home})
	if err == nil || !strings.Contains(err.Error(), "fresh devnet") {
		t.Fatalf("legacy db not refused with guidance: %v", err)
	}
}
