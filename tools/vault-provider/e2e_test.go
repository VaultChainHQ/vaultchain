package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/node"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/pkg/readauth"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/storage"
)

type acct struct {
	priv *secp256k1.PrivKey
	pub  string
	addr string
}

func newAcct(t *testing.T) acct {
	t.Helper()
	priv := secp256k1.GenPrivKey()
	addr, err := vcrypto.AddressFromPubKey(priv.PubKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return acct{priv, hex.EncodeToString(priv.PubKey().Bytes()), addr}
}

// TestEndToEndAgainstRealChain drives the real engine (signed txs over its REST
// handler, one block per step) together with the real provider handler:
// commit manifest -> open deal -> upload -> ranged download.
func TestEndToEndAgainstRealChain(t *testing.T) {
	alice, bob, prov := newAcct(t), newAcct(t), newAcct(t)
	coins := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 1_000_000_000))
	g := &node.Genesis{ChainID: "e2e-1", GenesisTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Storage: storage.DefaultParams()}
	for _, a := range []acct{alice, bob, prov} {
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

	// send signs msgs with a's pending nonce, submits them over REST and seals a block.
	// It returns the tx result recorded on chain.
	send := func(a acct, msg any) (ok bool, result string) {
		t.Helper()
		_, pending := eng.AccountNonces(a.addr)
		raw, err := node.NewSignedTx(g.ChainID, pending, []any{msg}, sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 10)), a.pub,
			func(b []byte) ([]byte, error) { return a.priv.Sign(b) })
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(rest.URL+"/vaultchain/txs", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		var sub struct{ Hash string }
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("submit: %d %s", resp.StatusCode, body)
		}
		_ = json.Unmarshal(body, &sub)
		if sub.Hash == "" {
			var raw2 map[string]any
			_ = json.Unmarshal(body, &raw2)
			sub.Hash, _ = raw2["hash"].(string)
		}
		if err := eng.ProduceBlock(); err != nil {
			t.Fatal(err)
		}
		r, err := http.Get(rest.URL + "/vaultchain/txs/" + sub.Hash)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var rec struct {
			Tx struct {
				OK     bool   `json:"ok"`
				Result string `json:"result"`
			} `json:"tx"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			t.Fatal(err)
		}
		return rec.Tx.OK, rec.Tx.Result
	}
	mustOK := func(a acct, msg any) {
		t.Helper()
		if ok, res := send(a, msg); !ok {
			t.Fatalf("tx failed: %s", res)
		}
	}

	data := testData(700_000) // 3 chunks at 256 KiB
	m, _, err := chunk.BuildBytes(data, chunk.DefaultChunkSize, "video/mp4", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	commit := storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size, ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name}
	deal := storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: m.Root, Provider: prov.addr, Duration: 1000, Denom: vtypes.NativeDenom}

	mustOK(prov, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p", Capacity: 1 << 30, Collateral: sdk.NewInt64Coin(vtypes.NativeDenom, 1000)})

	// A deal before the manifest exists, and by a non-owner after, must fail.
	if ok, _ := send(alice, deal); ok {
		t.Fatal("deal accepted without a committed manifest")
	}
	mustOK(alice, commit)
	if ok, _ := send(bob, deal); ok {
		t.Fatal("non-owner opened a deal on alice's manifest")
	}

	srv, err := newServer(prov.addr, rest.URL, g.ChainID, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := httptest.NewServer(srv.handler())
	t.Cleanup(ps.Close)
	post := func(root string, body []byte) int {
		resp, err := http.Post(ps.URL+"/v1/objects/"+root, "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	// Manifest committed but no deal yet: the provider refuses the bytes.
	if c := post(m.Root, data); c != http.StatusForbidden {
		t.Fatalf("upload before deal: %d", c)
	}
	mustOK(alice, deal)
	if c := post(m.Root, data); c != http.StatusOK {
		t.Fatalf("upload after deal: %d", c)
	}

	readTok := func(a acct) string {
		tok, err := readauth.Sign(prov.addr, m.Root, time.Now().Add(time.Minute), a.pub, func(b []byte) ([]byte, error) { return a.priv.Sign(b) })
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	getAs := func(tok string) int {
		req, _ := http.NewRequest("GET", ps.URL+"/v1/objects/"+m.Root, nil)
		if tok != "" {
			readauth.Attach(req, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	// Access enforcement against the real chain: owner yes, anonymous and a
	// stranger no, a grant to the stranger yes, then a "*" grant opens it to all.
	if c := getAs(""); c != http.StatusUnauthorized {
		t.Fatalf("anonymous read of a private object: %d", c)
	}
	if c := getAs(readTok(bob)); c != http.StatusForbidden {
		t.Fatalf("stranger read: %d", c)
	}
	mustOK(alice, access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: m.Root, Grantee: bob.addr, Capability: access.CapabilityRead})
	if c := getAs(readTok(bob)); c != http.StatusOK {
		t.Fatalf("granted reader: %d", c)
	}
	req, _ := http.NewRequest("GET", ps.URL+"/v1/objects/"+m.Root, nil)
	readauth.Attach(req, readTok(alice))
	req.Header.Set("Range", "bytes=262000-263000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(got, data[262000:263001]) || resp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("ranged download: %d ct=%q len=%d", resp.StatusCode, resp.Header.Get("Content-Type"), len(got))
	}
	mustOK(alice, access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: m.Root, Grantee: access.Public, Capability: access.CapabilityRead})
	resp, err = http.Get(ps.URL + "/v1/objects/" + m.Root) // anonymous: now public
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("public full download: %d", resp.StatusCode)
	}
	// Different (never committed) content is refused by the provider.
	other, _, _ := chunk.BuildBytes([]byte("not committed"), chunk.DefaultChunkSize, "text/plain", "n")
	if c := post(other.Root, []byte("not committed")); c != http.StatusNotFound {
		t.Fatalf("uncommitted upload: %d", c)
	}

	// The provider holds the bytes, so its prover accepts the assigned deal; the
	// deal opens (pending -> open) once the accept tx is in a block.
	fee := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 10))
	pr := newProver(srv, "prov", prov.pub, func(b []byte) ([]byte, error) { return prov.priv.Sign(b) }, fee)
	if n, err := pr.tick(); err != nil || n != 1 {
		t.Fatalf("accept tick: n=%d err=%v", n, err)
	}
	if err := eng.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	// The chain challenges the provider (epoch = 10 blocks) and the prover
	// answers from its object store: the replica gets a verified proof.
	proved := false
	for i := 0; i < 30 && !proved; i++ {
		if err := eng.ProduceBlock(); err != nil {
			t.Fatal(err)
		}
		n, err := pr.tick()
		if err != nil {
			t.Fatal(err)
		}
		proved = n > 0
	}
	if !proved {
		t.Fatal("prover never found a challenge to answer")
	}
	if err := eng.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	var deals struct {
		Deals []storage.Deal `json:"deals"`
	}
	if code, err := srv.chainGet("/vaultchain/storage/deals?provider="+prov.addr, &deals); err != nil || code != 200 || len(deals.Deals) != 1 {
		t.Fatalf("deals: %d %v %+v", code, err, deals)
	}
	if d := deals.Deals[0]; d.Status != "open" || len(d.Replicas) != 1 || d.Replicas[0].LastProvenEpoch == 0 || d.Replicas[0].MissedProofs != 0 {
		t.Fatalf("deal not proven: %+v", d)
	}
	// Once proven, the same epoch is not answered twice.
	if n, err := pr.tick(); err != nil || n != 0 {
		t.Fatalf("second tick: n=%d err=%v", n, err)
	}
}
