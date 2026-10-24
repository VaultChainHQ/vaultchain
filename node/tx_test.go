package node

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeTxDerivesSigner(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	raw := alice.tx(t, "c-1", 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(5)})
	dt, err := DecodeTx(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dt.Signer != alice.addr {
		t.Fatalf("signer %s want %s", dt.Signer, alice.addr)
	}
	if m := dt.Msgs[0].(MsgBankSend); m.From != alice.addr || m.To != bob.addr {
		t.Fatalf("bad decoded msg %+v", m)
	}
}

func TestDecodeTxRejectsTampering(t *testing.T) {
	alice, bob, eve := newAcct(t), newAcct(t), newAcct(t)
	raw := alice.tx(t, "c-1", 3, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(5)})
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		return mustJSON(t, m)
	}
	cases := map[string][]byte{
		"nonce":     mutate(func(m map[string]any) { m["nonce"] = 4 }),
		"chain":     mutate(func(m map[string]any) { m["chain_id"] = "other" }),
		"fee":       mutate(func(m map[string]any) { m["fee"] = []map[string]string{{"denom": "uvault", "amount": "1"}} }),
		"pubkey":    mutate(func(m map[string]any) { m["pubkey"] = eve.pub }),
		"msg":       []byte(strings.Replace(string(raw), `"amount":"5"`, `"amount":"500"`, 1)),
		"recipient": []byte(strings.Replace(string(raw), bob.addr, eve.addr, 1)),
	}
	for name, bad := range cases {
		if _, err := DecodeTx(bad); err == nil {
			t.Errorf("%s: tampered tx accepted", name)
		}
	}
}

func TestDecodeTxRejectsClientWrittenFrom(t *testing.T) {
	alice, bob, eve := newAcct(t), newAcct(t), newAcct(t)
	raw := alice.tx(t, "c-1", 0, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(5)})
	forged := strings.Replace(string(raw), `{"type":"bank/Send"`, `{"from":"`+eve.addr+`","type":"bank/Send"`, 1)
	if forged == string(raw) {
		t.Fatal("test setup: replacement did not apply")
	}
	if _, err := DecodeTx([]byte(forged)); err == nil {
		t.Fatal("tx with client-written from accepted")
	}
}

func TestDecodeTxStructuralChecks(t *testing.T) {
	alice, bob := newAcct(t), newAcct(t)
	good := MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(1)}
	if _, err := DecodeTx(nil); err == nil {
		t.Error("empty accepted")
	}
	if _, err := DecodeTx([]byte(`{}`)); err == nil {
		t.Error("{} accepted")
	}
	// A signer cannot even build a tx with an invalid recipient address.
	if _, err := NewSignedTx("c-1", 0, []any{MsgBankSend{Type: MsgTypeBankSend, To: "notanaddress", Amount: uv(1)}}, uv(1), alice.pub,
		func(b []byte) ([]byte, error) { return alice.priv.Sign(b) }); err == nil {
		t.Error("bad address accepted")
	}
	// Non-native fee denom is rejected.
	raw, err := NewSignedTx("c-1", 0, []any{good}, mustCoins(t, "5ibc/ATOM"), alice.pub, func(b []byte) ([]byte, error) { return alice.priv.Sign(b) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTx(raw); err == nil {
		t.Error("non-native fee accepted")
	}
	// Unknown msg type.
	if _, err := DecodeMsg(json.RawMessage(`{"type":"nope/Nope"}`)); err == nil {
		t.Error("unknown msg type accepted")
	}
}
