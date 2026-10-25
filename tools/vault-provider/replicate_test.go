package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/node"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/storage"
)

// chainFixture is a real engine behind a REST handler, one block per send.
type chainFixture struct {
	t    *testing.T
	eng  *node.Engine
	rest *httptest.Server
	g    *node.Genesis
}

func newChainFixture(t *testing.T, accts ...acct) *chainFixture {
	t.Helper()
	coins := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 1_000_000_000))
	g := &node.Genesis{ChainID: "e2e-1", GenesisTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Storage: storage.DefaultParams()}
	for _, a := range accts {
		g.Accounts = append(g.Accounts, node.GenesisAccount{Address: a.addr, Coins: coins})
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := node.WriteGenesis(home, g); err != nil {
		t.Fatal(err)
	}
	eng, err := node.NewEngine(node.Config{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	rest := httptest.NewServer(eng.RESTHandler())
	t.Cleanup(rest.Close)
	return &chainFixture{t: t, eng: eng, rest: rest, g: g}
}

// send signs and submits msg as a, then seals a block; it fails the test if the tx fails.
func (c *chainFixture) send(a acct, msg any) {
	c.t.Helper()
	_, pending := c.eng.AccountNonces(a.addr)
	raw, err := node.NewSignedTx(c.g.ChainID, pending, []any{msg}, sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 10)), a.pub,
		func(b []byte) ([]byte, error) { return a.priv.Sign(b) })
	if err != nil {
		c.t.Fatal(err)
	}
	resp, err := http.Post(c.rest.URL+"/vaultchain/txs", "application/json", bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		c.t.Fatalf("submit: %d %s", resp.StatusCode, body)
	}
	c.block()
}

func (c *chainFixture) block() {
	c.t.Helper()
	if err := c.eng.ProduceBlock(); err != nil {
		c.t.Fatal(err)
	}
}

// TestReplicatedDealRepairFetchAndBothProve: two providers are assigned; the
// owner uploads to only one; the other copies the object from the first
// replica once it is active, accepts, and both then answer their own challenges.
func TestReplicatedDealRepairFetchAndBothProve(t *testing.T) {
	alice, pa, pb := newAcct(t), newAcct(t), newAcct(t)
	c := newChainFixture(t, alice, pa, pb)
	fee := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 10))

	mk := func(a acct) (*server, *prover, *httptest.Server) {
		srv, err := newServer(a.addr, c.rest.URL, c.g.ChainID, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		hs := httptest.NewServer(srv.handler())
		t.Cleanup(hs.Close)
		return srv, newProver(srv, "k", a.pub, func(b []byte) ([]byte, error) { return a.priv.Sign(b) }, fee), hs
	}
	srvA, prA, hsA := mk(pa)
	srvB, prB, hsB := mk(pb)
	c.send(pa, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: hsA.URL, Capacity: 1 << 30, Collateral: sdk.NewInt64Coin(vtypes.NativeDenom, 1000)})
	c.send(pb, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: hsB.URL, Capacity: 1 << 30, Collateral: sdk.NewInt64Coin(vtypes.NativeDenom, 1000)})

	data := testData(600_000)
	m, _, err := chunk.BuildBytes(data, chunk.DefaultChunkSize, "video/mp4", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	c.send(alice, storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size, ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name})
	c.send(alice, storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: m.Root, Duration: 1000, Denom: vtypes.NativeDenom, Replication: 2})

	// Owner uploads to provider A only.
	resp, err := http.Post(hsA.URL+"/v1/objects/"+m.Root, "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload to A: %d", resp.StatusCode)
	}

	// B has nothing and no active replica to copy from yet: it must not accept.
	if n, err := prB.tick(); err != nil || n != 0 {
		t.Fatalf("B tick before A accepted: n=%d err=%v", n, err)
	}
	if n, err := prA.tick(); err != nil || n != 1 {
		t.Fatalf("A accept tick: n=%d err=%v", n, err)
	}
	c.block()
	// Now A is an active replica: B copies the object from it, verifies it, and accepts.
	if n, err := prB.tick(); err != nil || n != 1 {
		t.Fatalf("B accept tick: n=%d err=%v", n, err)
	}
	c.block()
	if !srvB.complete(m) {
		t.Fatal("B did not store the object")
	}
	_ = srvA

	var deals struct {
		Deals []storage.Deal `json:"deals"`
	}
	check := func(f func(d storage.Deal) bool, what string) {
		t.Helper()
		if code, err := srvA.chainGet("/vaultchain/storage/deals", &deals); err != nil || code != 200 || len(deals.Deals) != 1 {
			t.Fatalf("deals: %d %v", code, err)
		}
		if !f(deals.Deals[0]) {
			t.Fatalf("%s: %+v", what, deals.Deals[0])
		}
	}
	check(func(d storage.Deal) bool { return d.Status == storage.DealOpen && activeReplicas(d) == 2 }, "deal not open with 2 active replicas")

	for i := 0; i < 40; i++ {
		c.block()
		if _, err := prA.tick(); err != nil {
			t.Fatal(err)
		}
		if _, err := prB.tick(); err != nil {
			t.Fatal(err)
		}
	}
	c.block()
	check(func(d storage.Deal) bool {
		for _, r := range d.Replicas {
			if r.LastProvenEpoch == 0 || r.MissedProofs != 0 || r.Status != storage.ReplicaActive {
				return false
			}
		}
		return true
	}, "both replicas must be proven")
}
func activeReplicas(d storage.Deal) int {
	n := 0
	for _, r := range d.Replicas {
		if r.Status == storage.ReplicaActive {
			n++
		}
	}
	return n
}
