package access

import (
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
