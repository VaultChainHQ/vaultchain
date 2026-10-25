package access

import (
	"strings"
	"testing"

	"github.com/vaultchain/vaultchain/kv"
)

func TestPublicGrantIsReadOnly(t *testing.T) {
	k := NewKeeper(kv.NewCache(kv.NewMemDB()))
	owner := func(string) (string, bool) { return "alice", true }
	g := MsgGrantAccess{From: "alice", CID: "c", Grantee: Public, Capability: CapabilityRead}
	if err := k.Grant(g, 1, owner); err != nil {
		t.Fatal(err)
	}
	g.Capability = CapabilityWrite
	if err := k.Grant(g, 1, owner); err == nil {
		t.Fatal("public write grant accepted")
	}
	if gs := k.Grants(); len(gs) != 1 || gs[0].Grantee != Public {
		t.Fatalf("grants: %+v", gs)
	}
}

func grantsKeeper(t *testing.T) (Keeper, OwnerResolver) {
	t.Helper()
	k := NewKeeper(kv.NewCache(kv.NewMemDB()))
	owner := func(cid string) (string, bool) {
		if cid == "c" || cid == "other" {
			return "alice", true
		}
		return "", false
	}
	return k, owner
}

func mustGrant(t *testing.T, k Keeper, owner OwnerResolver, cid, grantee string) {
	t.Helper()
	if err := k.Grant(MsgGrantAccess{From: "alice", CID: cid, Grantee: grantee, Capability: CapabilityRead}, 1, owner); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeByGrantee(t *testing.T) {
	k, owner := grantsKeeper(t)
	mustGrant(t, k, owner, "c", "bob")
	mustGrant(t, k, owner, "c", "bob") // grants stack: revoke must remove both
	mustGrant(t, k, owner, "c", "carol")
	mustGrant(t, k, owner, "c", Public)
	mustGrant(t, k, owner, "other", "bob")

	n, err := k.Revoke(MsgRevokeAccess{From: "alice", CID: "c", Grantee: "bob"}, owner)
	if err != nil || n != 2 {
		t.Fatalf("revoke bob: n=%d err=%v", n, err)
	}
	var left []string
	for _, g := range k.Grants() {
		left = append(left, g.CID+":"+g.Grantee)
	}
	if got := strings.Join(left, ","); got != "c:carol,c:*,other:bob" {
		t.Fatalf("remaining grants: %s", got)
	}
	if n, err = k.Revoke(MsgRevokeAccess{From: "alice", CID: "c", Grantee: Public}, owner); err != nil || n != 1 {
		t.Fatalf("revoke public: n=%d err=%v", n, err)
	}
}

func TestRevokeByIDIsScopedToTheCID(t *testing.T) {
	k, owner := grantsKeeper(t)
	mustGrant(t, k, owner, "c", "bob")     // id 1
	mustGrant(t, k, owner, "other", "bob") // id 2
	if _, err := k.Revoke(MsgRevokeAccess{From: "alice", CID: "c", GrantID: 2}, owner); err == nil {
		t.Fatal("revoked a grant that belongs to a different cid")
	}
	if _, err := k.Revoke(MsgRevokeAccess{From: "alice", CID: "c", GrantID: 1}, owner); err != nil {
		t.Fatal(err)
	}
	if gs := k.Grants(); len(gs) != 1 || gs[0].ID != 2 {
		t.Fatalf("grants: %+v", gs)
	}
	// Ids are never reused after a revoke.
	mustGrant(t, k, owner, "c", "dave")
	if gs := k.Grants(); gs[len(gs)-1].ID != 3 {
		t.Fatalf("grant id reused: %+v", gs)
	}
}

func TestRevokeIsOwnerOnlyAndNeverSilent(t *testing.T) {
	k, owner := grantsKeeper(t)
	mustGrant(t, k, owner, "c", "bob")
	cases := map[string]MsgRevokeAccess{
		"not the owner":           {From: "bob", CID: "c", Grantee: "bob"},
		"unknown cid":             {From: "alice", CID: "nope", Grantee: "bob"},
		"nothing selected":        {From: "alice", CID: "c"},
		"no such grantee":         {From: "alice", CID: "c", Grantee: "zed"},
		"no such grant id":        {From: "alice", CID: "c", GrantID: 99},
		"id and grantee mismatch": {From: "alice", CID: "c", GrantID: 1, Grantee: "carol"},
		"missing signer":          {CID: "c", Grantee: "bob"},
	}
	for name, m := range cases {
		if _, err := k.Revoke(m, owner); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(k.Grants()) != 1 {
		t.Fatal("a refused revoke changed state")
	}
}

func TestEncryptedGrantsNeedAnEnvelopeAndCannotBePublic(t *testing.T) {
	k, owner := grantsKeeper(t)
	enc := func(string) bool { return true }
	env := strings.Repeat("ab", 105)
	grant := func(grantee, envelope string, isEnc func(string) bool) error {
		return k.GrantChecked(MsgGrantAccess{From: "alice", CID: "c", Grantee: grantee, Capability: CapabilityRead, KeyEnvelope: envelope}, 1, owner, isEnc)
	}
	if err := grant("bob", "", enc); err == nil {
		t.Error("encrypted grant without envelope accepted")
	}
	if err := grant(Public, env, enc); err == nil {
		t.Error("public grant on an encrypted object accepted")
	}
	if err := grant("bob", "zz", enc); err == nil {
		t.Error("non-hex envelope accepted")
	}
	if err := grant("bob", strings.Repeat("ab", 300), enc); err == nil {
		t.Error("oversized envelope accepted")
	}
	if err := grant("bob", env, func(string) bool { return false }); err == nil {
		t.Error("envelope on an unencrypted object accepted")
	}
	if err := grant("bob", env, enc); err != nil {
		t.Fatal(err)
	}
	if gs := k.Grants(); len(gs) != 1 || gs[0].KeyEnvelope != env {
		t.Fatalf("grants: %+v", gs)
	}
}
