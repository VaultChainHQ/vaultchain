package node

import (
	"strings"
	"testing"

	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/bank"
	"github.com/vaultchain/vaultchain/x/storage"
)

func newTestState(t *testing.T, funded ...testAcct) *ChainState {
	t.Helper()
	st := &ChainState{
		ChainID:  "c-1",
		Bank:     bank.NewState(),
		Storage:  storage.NewState(),
		Access:   access.NewState(),
		Accounts: map[string]uint64{},
	}
	for _, a := range funded {
		if err := st.Bank.AddCoins(a.addr, uv(1_000_000_000)); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestApplyTxReplayProtection(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	st := newTestState(t, alice)
	raw := alice.tx(t, "c-1", 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(100)})
	if _, err := ApplyTx(st, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyTx(st, raw); err == nil || !strings.Contains(err.Error(), "bad nonce") {
		t.Fatalf("replay not rejected by nonce: %v", err)
	}
	if got := st.Bank.GetBalance(bob.addr).AmountOf("uvault").Int64(); got != 100 {
		t.Fatalf("bob balance %d, want 100 (replay must not pay twice)", got)
	}
	if st.Accounts[alice.addr] != 1 {
		t.Fatalf("nonce %d want 1", st.Accounts[alice.addr])
	}
	// Skipping ahead is not allowed either.
	skip := alice.tx(t, "c-1", 5, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})
	if _, err := ApplyTx(st, skip); err == nil {
		t.Fatal("nonce gap accepted")
	}
}

func TestApplyTxWrongChainID(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	st := newTestState(t, alice)
	raw := alice.tx(t, "other-chain", 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})
	if _, err := ApplyTx(st, raw); err == nil {
		t.Fatal("tx for another chain accepted")
	}
}

func TestApplyTxCannotSpendOthersFunds(t *testing.T) {
	alice, bob, eve := newAcct(t), newAcct(t), newAcct(t)
	st := newTestState(t, alice)
	// eve signs a send; the sender is eve (unfunded), never alice.
	raw := eve.tx(t, "c-1", 0, nil, MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})
	if _, err := ApplyTx(st, raw); err == nil {
		t.Fatal("unfunded signer spent funds")
	}
	if !st.Bank.GetBalance(alice.addr).AmountOf("uvault").Equal(uv(1_000_000_000).AmountOf("uvault")) {
		t.Fatal("alice balance changed")
	}
}

func TestApplyTxFeeFailureDoesNotConsumeNonce(t *testing.T) {
	poor, bob := newAcct(t), newAcct(t)
	st := newTestState(t)
	raw := poor.tx(t, "c-1", 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)})
	if _, err := ApplyTx(st, raw); err == nil {
		t.Fatal("tx with unaffordable fee accepted")
	}
	if st.Accounts[poor.addr] != 0 {
		t.Fatal("nonce consumed on rejected tx")
	}
}

func TestGrantAccessRequiresCIDOwner(t *testing.T) {
	alice, bob, prov := newAcct(t), newAcct(t), newAcct(t)
	st := newTestState(t, alice, bob)
	st.Storage.Providers[prov.addr] = storage.Provider{Address: prov.addr, Endpoint: "http://x", Capacity: 1 << 20, FreeBytes: 1 << 20, Status: "active"}

	open := alice.tx(t, "c-1", 0, nil, storage.MsgOpenDeal{
		Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: "cid-1", Size: 10,
		Provider: prov.addr, Duration: 100, Denom: "uvault", Replication: 1,
	})
	if _, err := ApplyTx(st, open); err != nil {
		t.Fatal(err)
	}
	grant := func(a testAcct, nonce uint64) error {
		_, err := ApplyTx(st, a.tx(t, "c-1", nonce, nil, access.MsgGrantAccess{
			Type: access.MsgTypeGrantAccess, CID: "cid-1", Grantee: bob.addr, Capability: access.CapabilityRead,
		}))
		return err
	}
	if err := grant(bob, 0); err == nil {
		t.Fatal("non-owner granted access")
	}
	if len(st.Access.Grants) != 0 {
		t.Fatal("grant recorded for non-owner")
	}
	if err := grant(alice, 1); err != nil {
		t.Fatalf("owner grant failed: %v", err)
	}
	if len(st.Access.Grants) != 1 {
		t.Fatal("owner grant not recorded")
	}
	// Unknown CID has no owner.
	_, err := ApplyTx(st, alice.tx(t, "c-1", 2, nil, access.MsgGrantAccess{
		Type: access.MsgTypeGrantAccess, CID: "nope", Grantee: bob.addr, Capability: access.CapabilityRead,
	}))
	if err == nil {
		t.Fatal("grant on ownerless cid accepted")
	}
}
