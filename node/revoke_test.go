package node

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/pkg/vaultcrypt"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/storage"
)

// encryptedManifest encrypts data for owner and returns the commit message.
func encryptedManifest(t *testing.T, owner testAcct, data []byte) (storage.MsgCommitManifest, vaultcrypt.Key) {
	t.Helper()
	key, err := vaultcrypt.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	var ct bytes.Buffer
	if _, err := vaultcrypt.Encrypt(&ct, bytes.NewReader(data), key, 0); err != nil {
		t.Fatal(err)
	}
	m, _, err := chunk.BuildBytes(ct.Bytes(), storage.MinChunkSize, "text/plain", "secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := hex.DecodeString(owner.pub)
	env, err := vaultcrypt.SealKey(key, pub, m.Root, owner.addr)
	if err != nil {
		t.Fatal(err)
	}
	return storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size,
		ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name, Encryption: storage.EncryptionV1,
		KeyEnvelope: hex.EncodeToString(env)}, key
}

func grantCount(app *App, cid string) (n int) {
	_ = app.View(func(s kv.Store) error {
		for _, g := range access.NewKeeper(s).Grants() {
			if g.CID == cid {
				n++
			}
		}
		return nil
	})
	return
}

func TestRevokeAndEncryptedGrantsOnChain(t *testing.T) {
	alice, bob, carol := newAcct(t), newAcct(t), newAcct(t)
	app := memApp(t, testGenesis(false, alice, bob, carol))
	man, key := encryptedManifest(t, alice, []byte("top secret"))
	res := exec(t, app, "", alice.tx(t, testChain, 0, nil, man))
	if !res.Results[0].OK {
		t.Fatalf("encrypted manifest rejected: %+v", res.Results[0])
	}
	var rec storage.ManifestRecord
	_ = app.View(func(s kv.Store) error {
		var ok bool
		rec, ok = storage.NewKeeper(s).Manifest(man.Root)
		if !ok {
			t.Fatal("manifest missing")
		}
		return nil
	})
	if !rec.Encrypted() || rec.KeyEnvelope != man.KeyEnvelope {
		t.Fatalf("manifest lost its encryption marker/envelope: %+v", rec)
	}

	send := func(a testAcct, msg any) bool {
		return exec(t, app, "", a.tx(t, testChain, app.Nonce(a.addr), nil, msg)).Results[0].OK
	}
	grant := func(grantee, env string) any {
		return access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: man.Root, Grantee: grantee,
			Capability: access.CapabilityRead, KeyEnvelope: env}
	}
	revoke := func(grantee string, id uint64) any {
		return access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: man.Root, Grantee: grantee, GrantID: id}
	}

	// Seal the key to bob exactly as the CLI does.
	bobPub, _ := hex.DecodeString(bob.pub)
	envB, err := vaultcrypt.SealKey(key, bobPub, man.Root, bob.addr)
	if err != nil {
		t.Fatal(err)
	}
	if send(alice, grant(bob.addr, "")) {
		t.Fatal("grant on an encrypted object without an envelope accepted")
	}
	if send(alice, grant(access.Public, hex.EncodeToString(envB))) {
		t.Fatal("public grant on an encrypted object accepted")
	}
	if !send(alice, grant(bob.addr, hex.EncodeToString(envB))) || grantCount(app, man.Root) != 1 {
		t.Fatal("envelope grant failed")
	}

	// Only the owner may revoke; a grantee cannot revoke their own or others' grants.
	if send(bob, revoke(bob.addr, 0)) || send(carol, revoke(bob.addr, 0)) || grantCount(app, man.Root) != 1 {
		t.Fatal("non-owner revoked a grant")
	}
	if send(alice, revoke(carol.addr, 0)) {
		t.Fatal("revoking a grant that does not exist reported success")
	}
	if !send(alice, revoke(bob.addr, 0)) || grantCount(app, man.Root) != 0 {
		t.Fatal("owner revoke failed")
	}
	// Re-granting after revoking works and gets a new id.
	if !send(alice, grant(bob.addr, hex.EncodeToString(envB))) || grantCount(app, man.Root) != 1 {
		t.Fatal("re-grant failed")
	}
	var id uint64
	_ = app.View(func(s kv.Store) error { id = access.NewKeeper(s).Grants()[0].ID; return nil })
	if !send(alice, revoke("", id)) || grantCount(app, man.Root) != 0 {
		t.Fatal("revoke by grant id failed")
	}

	// A plaintext object takes no envelope.
	plain := manifestFor(t, objA, "text/plain", "a.txt")
	exec(t, app, "", alice.tx(t, testChain, app.Nonce(alice.addr), nil, plain))
	if send(alice, access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: plain.Root, Grantee: bob.addr,
		Capability: access.CapabilityRead, KeyEnvelope: hex.EncodeToString(envB)}) {
		t.Fatal("envelope on a plaintext object accepted")
	}
	// Half-marked manifests are rejected.
	bad := manifestFor(t, []byte("other"), "text/plain", "b.txt")
	bad.Encryption = storage.EncryptionV1
	if send(alice, bad) {
		t.Fatal("encrypted manifest without an envelope accepted")
	}
	bad2 := manifestFor(t, []byte("another"), "text/plain", "c.txt")
	bad2.KeyEnvelope = hex.EncodeToString(envB)
	if send(alice, bad2) {
		t.Fatal("envelope without the encryption marker accepted")
	}
}

func TestPubKeyRegistry(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	e := policyEngine(t, Config{}, false, alice, bob)
	get := func(addr string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		e.RESTHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/vaultchain/auth/pubkeys/"+addr, nil))
		return w
	}
	if w := get(alice.addr); w.Code != http.StatusNotFound {
		t.Fatalf("pubkey known before any tx: %d", w.Code)
	}
	// A tx that fails the ante handler (wrong chain id) registers nothing.
	if res, err := e.app.ExecuteBlock(blockAt(e.app, "", alice.tx(t, "other-chain", 0, nil, manifestFor(t, objA, "t/p", "a")))); err == nil && res.Results[0].OK {
		t.Fatal("wrong-chain tx accepted")
	}
	if w := get(alice.addr); w.Code != http.StatusNotFound {
		t.Fatalf("rejected tx registered a key: %d", w.Code)
	}
	exec(t, e.app, "", alice.tx(t, testChain, 0, nil, manifestFor(t, objA, "text/plain", "a.txt")))
	w := get(alice.addr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), alice.pub) {
		t.Fatalf("registered key not served: %d %s", w.Code, w.Body.String())
	}
	if w := get(bob.addr); w.Code != http.StatusNotFound {
		t.Fatalf("bob's key known although bob never signed: %d", w.Code)
	}
	if got, ok := PubKeyOf(e.app.DB(), alice.addr); !ok || got != alice.pub {
		t.Fatalf("PubKeyOf = %q, %v", got, ok)
	}
	// Manifests can be listed by owner.
	w = httptest.NewRecorder()
	e.RESTHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/vaultchain/storage/manifests?owner="+alice.addr, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "a.txt") {
		t.Fatalf("manifests?owner: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	e.RESTHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/vaultchain/storage/manifests?owner="+bob.addr, nil))
	if strings.Contains(w.Body.String(), "a.txt") {
		t.Fatalf("owner filter leaked: %s", w.Body.String())
	}
}
