package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/VaultChainHQ/vaultchain/pkg/readauth"
)

func (e *env) token(a acct, audience, root string, ttl time.Duration) string {
	e.t.Helper()
	tok, err := readauth.Sign(audience, root, time.Now().Add(ttl), a.pub, func(b []byte) ([]byte, error) { return a.priv.Sign(b) })
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *env) get(path string, hdr ...string) *http.Response { return e.do("GET", path, nil, hdr...) }

func auth(tok string) []string { return []string{"Authorization", readauth.Scheme + " " + tok} }

func statusOf(r *http.Response) int {
	r.Body.Close()
	return r.StatusCode
}

// privateEnv has one stored object owned by owner, readable by nobody else.
func privateEnv(t *testing.T) (*env, acct, string, []byte) {
	t.Helper()
	e := newEnv(t)
	owner := newAcct(t)
	e.chain.owner = owner.addr
	e.chain.publicAll = false
	data := testData(10000)
	m, _ := e.commit(data, "video/mp4", "clip.mp4", true)
	if r := e.upload(m, data); r.StatusCode != 200 {
		t.Fatalf("upload: %d", r.StatusCode)
	}
	return e, owner, m.Root, data
}

func TestPrivateObjectNeedsASignedToken(t *testing.T) {
	e, owner, root, data := privateEnv(t)
	obj, chunk0 := "/v1/objects/"+root, "/v1/chunks/"+root+"/0"

	r := e.get(obj)
	if r.StatusCode != 401 || !strings.HasPrefix(r.Header.Get("WWW-Authenticate"), readauth.Scheme) {
		t.Fatalf("anonymous: %d %v", r.StatusCode, r.Header)
	}
	r.Body.Close()
	for _, p := range []string{obj, chunk0} {
		if c := statusOf(e.do("HEAD", p, nil)); c != 401 {
			t.Fatalf("anonymous HEAD %s: %d", p, c)
		}
	}

	tok := e.token(owner, provAddr, root, time.Minute)
	r = e.get(obj, auth(tok)...)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "private, max-age=300" {
		t.Fatalf("owner: %d %q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	if got := readBody(t, r); string(got) != string(data) {
		t.Fatal("owner got wrong bytes")
	}
	// Range and chunk reads work with the same token; so does ?vault_token= for browser links.
	if r = e.get(obj, append(auth(tok), "Range", "bytes=10-19")...); r.StatusCode != 206 {
		t.Fatalf("range: %d", r.StatusCode)
	}
	r.Body.Close()
	if c := statusOf(e.get(chunk0, auth(tok)...)); c != 200 {
		t.Fatalf("chunk: %d", c)
	}
	if c := statusOf(e.get(obj + "?" + readauth.QueryParam + "=" + tok)); c != 200 {
		t.Fatalf("query token: %d", c)
	}
	// Status stays open (it reveals only chunk coverage of a root you already know).
	if c := statusOf(e.get(obj + "/status")); c != 200 {
		t.Fatalf("status: %d", c)
	}
}

func TestBadTokensAreRefused(t *testing.T) {
	e, owner, root, _ := privateEnv(t)
	obj := "/v1/objects/" + root
	other := strings.Repeat("c", 64)
	for name, tok := range map[string]string{
		"other provider": e.token(owner, "vault1elsewhere", root, time.Minute),
		"other root":     e.token(owner, provAddr, other, time.Minute),
		"expired":        e.token(owner, provAddr, root, -time.Minute),
		"too long":       e.token(owner, provAddr, root, 3*time.Hour),
		"garbage":        "not.a.token",
	} {
		if c := statusOf(e.get(obj, auth(tok)...)); c != 401 {
			t.Errorf("%s: got %d, want 401", name, c)
		}
	}
}

func TestStrangerNeedsAGrant(t *testing.T) {
	e, _, root, _ := privateEnv(t)
	stranger := newAcct(t)
	obj := "/v1/objects/" + root
	tok := e.token(stranger, provAddr, root, time.Minute)
	if c := statusOf(e.get(obj, auth(tok)...)); c != 403 {
		t.Fatalf("stranger without grant: %d", c)
	}
	grant := func(grantee, cap string, expiry int64) {
		e.chain.grants = append(e.chain.grants, map[string]any{"cid": root, "grantee": grantee, "capability": cap, "expiry_height": expiry})
	}
	// Someone else's grant does not help; an expired one does not either.
	grant("vault1someoneelse", "read", 0)
	grant(stranger.addr, "read", 5) // chain height is 10
	if c := statusOf(e.get(obj, auth(tok)...)); c != 403 {
		t.Fatalf("expired/foreign grants: %d", c)
	}
	grant(stranger.addr, "read", 50)
	if c := statusOf(e.get(obj, auth(tok)...)); c != 200 {
		t.Fatalf("live grant: %d", c)
	}
	// A grant on another object is irrelevant.
	stranger2 := newAcct(t)
	e.chain.grants = []map[string]any{{"cid": strings.Repeat("d", 64), "grantee": stranger2.addr, "capability": "read"}}
	if c := statusOf(e.get(obj, auth(e.token(stranger2, provAddr, root, time.Minute))...)); c != 403 {
		t.Fatalf("grant for another cid: %d", c)
	}
}

func TestPublicGrantAllowsAnonymousReads(t *testing.T) {
	e, _, root, _ := privateEnv(t)
	obj := "/v1/objects/" + root
	if c := statusOf(e.get(obj)); c != 401 {
		t.Fatalf("before: %d", c)
	}
	e.chain.grants = append(e.chain.grants, map[string]any{"cid": root, "grantee": "*", "capability": "read", "expiry_height": 0})
	r := e.get(obj)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Cache-Control"), "public") {
		t.Fatalf("public: %d %q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	r.Body.Close()
}

func TestReplicaMayReadForRepair(t *testing.T) {
	e, _, root, _ := privateEnv(t)
	peer := newAcct(t)
	obj := "/v1/objects/" + root
	tok := e.token(peer, provAddr, root, time.Minute)
	if c := statusOf(e.get(obj, auth(tok)...)); c != 403 {
		t.Fatalf("peer without deal: %d", c)
	}
	e.chain.deals = append(e.chain.deals, map[string]string{"cid": root, "provider": peer.addr, "status": "open"})
	if c := statusOf(e.get(obj, auth(tok)...)); c != 200 {
		t.Fatalf("peer with replica: %d", c)
	}
}

func TestDeniedReadsAreNotCached(t *testing.T) {
	e, _, root, _ := privateEnv(t)
	stranger := newAcct(t)
	tok := e.token(stranger, provAddr, root, time.Minute)
	for i := 0; i < 2; i++ {
		if c := statusOf(e.get("/v1/objects/"+root, auth(tok)...)); c != 403 {
			t.Fatalf("round %d: %d", i, c)
		}
	}
	e.chain.grants = append(e.chain.grants, map[string]any{"cid": root, "grantee": stranger.addr, "capability": "write"})
	if c := statusOf(e.get("/v1/objects/"+root, auth(tok)...)); c != 200 {
		t.Fatal(fmt.Sprint("grant not honoured immediately: ", c))
	}
}

func TestRevokedGrantStopsServingAfterTheCacheWindow(t *testing.T) {
	old := accessCacheTTL
	accessCacheTTL = 150 * time.Millisecond
	t.Cleanup(func() { accessCacheTTL = old })

	e, owner, root, data := privateEnv(t)
	reader := newAcct(t)
	obj := "/v1/objects/" + root
	// One long-lived token: revocation must work on chain state, not on token expiry.
	tok := e.token(reader, provAddr, root, time.Hour)
	if c := statusOf(e.get(obj, auth(tok)...)); c != 403 {
		t.Fatalf("before grant: %d", c)
	}
	e.chain.grants = []map[string]any{{"cid": root, "grantee": reader.addr, "capability": "read"}}
	if c := statusOf(e.get(obj, auth(tok)...)); c != 200 {
		t.Fatalf("with grant: %d", c)
	}
	// The owner revokes on chain: the grant disappears from the list.
	e.chain.grants = nil
	time.Sleep(accessCacheTTL + 100*time.Millisecond)
	if c := statusOf(e.get(obj, auth(tok)...)); c != 403 {
		t.Fatalf("revoked reader still served: %d", c)
	}
	rr := e.get(obj, append(auth(tok), "Range", "bytes=0-9")...)
	if c := statusOf(rr); c != 403 {
		t.Fatalf("revoked reader, ranged: %d", c)
	}
	// The owner is unaffected.
	r := e.get(obj, auth(e.token(owner, provAddr, root, time.Minute))...)
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !bytes.Equal(body, data) {
		t.Fatalf("owner after revoking a grant: %d", r.StatusCode)
	}

	// Revoking the public grant makes the object private again.
	e.chain.grants = []map[string]any{{"cid": root, "grantee": "*", "capability": "read"}}
	if c := statusOf(e.get(obj)); c != 200 {
		t.Fatalf("public: %d", c)
	}
	e.chain.grants = nil
	time.Sleep(accessCacheTTL + 100*time.Millisecond)
	if c := statusOf(e.get(obj)); c != 401 {
		t.Fatalf("anonymous after the public grant was revoked: %d", c)
	}
}
