package crypto

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().Bytes()
	msg := []byte("hello vault")
	sig, err := SignWithHexKey(hex.EncodeToString(priv.Bytes()), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifySignature(pub, msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature(pub, []byte("hello vault!"), sig) {
		t.Fatal("signature verified for different message")
	}
	other := secp256k1.GenPrivKey().PubKey().Bytes()
	if VerifySignature(other, msg, sig) {
		t.Fatal("signature verified under a different key")
	}
	bad := append([]byte{}, sig...)
	bad[10] ^= 0xff
	if VerifySignature(pub, msg, bad) {
		t.Fatal("tampered signature verified")
	}
	if VerifySignature(pub, msg, sig[:63]) || VerifySignature(pub[:32], msg, sig) {
		t.Fatal("malformed lengths verified")
	}
}

func TestAddressFromPubKeyMatchesKeyring(t *testing.T) {
	kr, err := NewKeyring(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := kr.Add("alice")
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := hex.DecodeString(rec.PubKeyHex)
	addr, err := AddressFromPubKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if addr != rec.Address || !strings.HasPrefix(addr, "vault1") {
		t.Fatalf("address mismatch: %s vs %s", addr, rec.Address)
	}
	sig, err := kr.Sign("alice", []byte("m"))
	if err != nil || !VerifySignature(pub, []byte("m"), sig) {
		t.Fatalf("keyring signature invalid: %v", err)
	}
	if _, err := AddressFromPubKey(pub[:10]); err == nil {
		t.Fatal("short pubkey accepted")
	}
}
