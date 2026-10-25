package main

import (
	"bytes"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/node"
	"github.com/vaultchain/vaultchain/pkg/client"
	"github.com/vaultchain/vaultchain/pkg/readauth"
	"github.com/vaultchain/vaultchain/pkg/vaultcrypt"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/storage"
)

func (a acct) record(name string) vcrypto.KeyRecord {
	return vcrypto.KeyRecord{Name: name, Address: a.addr, PubKeyHex: a.pub, PrivKeyHex: hex.EncodeToString(a.priv.Bytes())}
}

// TestEncryptedRoundTripGrantAndRevokeAgainstRealChain drives the real engine,
// the real provider and pkg/client: client-side encryption, ciphertext stored
// at the provider, key wrapping to a grantee, decryption by the grantee, and
// revocation closing both the provider and the on-chain key envelope.
func TestEncryptedRoundTripGrantAndRevokeAgainstRealChain(t *testing.T) {
	old := accessCacheTTL
	accessCacheTTL = 100 * time.Millisecond
	t.Cleanup(func() { accessCacheTTL = old })

	alice, bob, carol, prov := newAcct(t), newAcct(t), newAcct(t), newAcct(t)
	coins := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 1_000_000_000))
	g := &node.Genesis{ChainID: "e2e-enc-1", GenesisTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Storage: storage.DefaultParams()}
	for _, a := range []acct{alice, bob, carol, prov} {
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
	cl := client.New(rest.URL)
	fee := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 10))

	signer := func(a acct) client.Signer {
		return client.Signer{Address: a.addr, PubKey: a.pub, Sign: func(b []byte) ([]byte, error) { return a.priv.Sign(b) }}
	}
	// do submits msg through pkg/client, seals a block, and waits for the result.
	do := func(a acct, msg any) error {
		t.Helper()
		h, err := cl.Send(g.ChainID, signer(a), fee, msg)
		if err != nil {
			return err
		}
		if err := eng.ProduceBlock(); err != nil {
			t.Fatal(err)
		}
		return cl.WaitTx(h, 5*time.Second)
	}
	must := func(a acct, msg any) {
		t.Helper()
		if err := do(a, msg); err != nil {
			t.Fatal(err)
		}
	}

	must(prov, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider, Endpoint: "http://p", Capacity: 1 << 30, Collateral: sdk.NewInt64Coin(vtypes.NativeDenom, 1000)})
	srv, err := newServer(prov.addr, rest.URL, g.ChainID, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := httptest.NewServer(srv.handler())
	t.Cleanup(ps.Close)

	// --- alice encrypts, commits, opens a deal, uploads the ciphertext ---
	secret := []byte(strings.Repeat("the launch code is 0000 - keep it secret. ", 4000)) // ~170 KB
	cipherPath := filepath.Join(t.TempDir(), "secret.vle")
	msg, err := client.EncryptToFile(bytes.NewReader(secret), cipherPath, alice.record("alice"), "text/plain", "secret.txt", 4096)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("launch code")) || uint64(len(ct)) != msg.Size {
		t.Fatalf("ciphertext leaks plaintext or manifest size (%d) != ciphertext size (%d)", msg.Size, len(ct))
	}
	root := msg.Root
	must(alice, msg)
	must(alice, storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: root, Provider: prov.addr, Duration: 1000, Denom: vtypes.NativeDenom})
	resp, err := http.Post(ps.URL+"/v1/objects/"+root, "application/octet-stream", bytes.NewReader(ct))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload ciphertext: %v %v", err, resp)
	}
	resp.Body.Close()
	if m, err := cl.Manifest(root); err != nil || !m.Encrypted() || m.Owner != alice.addr {
		t.Fatalf("manifest on chain: %+v %v", m, err)
	}

	fetch := func(a acct) (int, []byte) {
		t.Helper()
		tok, err := readauth.Sign(prov.addr, root, time.Now().Add(time.Hour), a.pub, func(b []byte) ([]byte, error) { return a.priv.Sign(b) })
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("GET", ps.URL+"/v1/objects/"+root, nil)
		readauth.Attach(req, tok)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, b
	}

	// --- owner downloads ciphertext and decrypts ---
	code, got := fetch(alice)
	if code != 200 || !bytes.Equal(got, ct) {
		t.Fatalf("owner download: %d", code)
	}
	pt, err := cl.DecryptBytes(got, root, alice.record("alice"))
	if err != nil || !bytes.Equal(pt, secret) {
		t.Fatalf("owner decrypt: %v", err)
	}

	// --- bob: no access, no key ---
	if code, _ := fetch(bob); code != 403 {
		t.Fatalf("bob without grant: %d", code)
	}
	if _, err := cl.FileKey(root, bob.record("bob")); err == nil {
		t.Fatal("bob obtained a key without a grant")
	}
	// bob has not signed anything, so alice cannot find his public key...
	if _, err := cl.GrantEnvelope(root, alice.record("alice"), bob.addr, ""); err == nil || !strings.Contains(err.Error(), "public key") {
		t.Fatalf("grant envelope for an unknown key: %v", err)
	}
	// ...unless she is given it out of band, and a wrong key is rejected.
	if _, err := cl.GrantEnvelope(root, alice.record("alice"), bob.addr, carol.pub); err == nil {
		t.Fatal("a public key of another account was accepted")
	}
	// Once bob has sent a transaction his key is on chain.
	must(bob, node.MsgBankSend{Type: node.MsgTypeBankSend, To: carol.addr, Amount: sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 1))})
	env, err := cl.GrantEnvelope(root, alice.record("alice"), bob.addr, "")
	if err != nil {
		t.Fatal(err)
	}
	// carol cannot be the owner's grantee-by-proxy: bob's envelope is useless to others.
	must(alice, access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: root, Grantee: bob.addr, Capability: access.CapabilityRead, KeyEnvelope: env})

	code, got = fetch(bob)
	if code != 200 {
		t.Fatalf("bob with grant: %d", code)
	}
	if pt, err = cl.DecryptBytes(got, root, bob.record("bob")); err != nil || !bytes.Equal(pt, secret) {
		t.Fatalf("grantee decrypt: %v", err)
	}
	if _, err := cl.FileKey(root, carol.record("carol")); err == nil {
		t.Fatal("carol obtained a key without a grant")
	}
	// bob keeps a copy of the key and of the ciphertext before being revoked.
	bobKey, err := cl.FileKey(root, bob.record("bob"))
	if err != nil {
		t.Fatal(err)
	}
	saved := append([]byte(nil), got...)

	// --- tampering with the stored ciphertext is detected by the client ---
	bad := append([]byte(nil), got...)
	bad[len(bad)/2] ^= 0x01
	if _, err := cl.DecryptBytes(bad, root, bob.record("bob")); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}

	// --- alice revokes bob ---
	must(alice, access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: root, Grantee: bob.addr})
	time.Sleep(accessCacheTTL + 100*time.Millisecond)
	if code, _ := fetch(bob); code != 403 {
		t.Fatalf("revoked bob still served: %d", code)
	}
	if _, err := cl.FileKey(root, bob.record("bob")); err == nil {
		t.Fatal("revoked bob can still fetch the key envelope from chain")
	}
	if grants, err := cl.Grants(root); err != nil || len(grants) != 0 {
		t.Fatalf("grants after revoke: %v %v", grants, err)
	}
	// The documented limit: a key and ciphertext bob already holds keep working.
	var out bytes.Buffer
	if _, err := vaultcrypt.Decrypt(&out, bytes.NewReader(saved), bobKey); err != nil || !bytes.Equal(out.Bytes(), secret) {
		t.Fatalf("saved key no longer decrypts saved ciphertext: %v", err)
	}
	// The owner is unaffected.
	if code, _ := fetch(alice); code != 200 {
		t.Fatalf("owner after revoke: %d", code)
	}
}
