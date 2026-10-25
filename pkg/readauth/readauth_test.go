package readauth

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
)

const root = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func key(t *testing.T) (*secp256k1.PrivKey, string, string) {
	t.Helper()
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().Bytes()
	addr, err := vcrypto.AddressFromPubKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv, hex.EncodeToString(pub), addr
}

func TestSignVerifyRoundTrip(t *testing.T) {
	priv, pubHex, addr := key(t)
	now := time.Now()
	tok, err := Sign("vault1prov", root, now.Add(time.Minute), pubHex, priv.Sign)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(tok, "vault1prov", root, now)
	if err != nil || got != addr {
		t.Fatalf("verify: %q %v (want %s)", got, err, addr)
	}
}

func TestRejects(t *testing.T) {
	priv, pubHex, _ := key(t)
	now := time.Now()
	tok, _ := Sign("vault1prov", root, now.Add(time.Minute), pubHex, priv.Sign)
	other := strings.Repeat("b", 64)
	cases := map[string]func() error{
		"other provider": func() error { _, e := Verify(tok, "vault1evil", root, now); return e },
		"other root":     func() error { _, e := Verify(tok, "vault1prov", other, now); return e },
		"expired":        func() error { _, e := Verify(tok, "vault1prov", root, now.Add(2*time.Minute)); return e },
		"garbage":        func() error { _, e := Verify("a.b", "vault1prov", root, now); return e },
		"too long": func() error {
			long, _ := Sign("vault1prov", root, now.Add(2*MaxTTL), pubHex, priv.Sign)
			_, e := Verify(long, "vault1prov", root, now)
			return e
		},
		"forged key": func() error {
			priv2, pub2, _ := key(t)
			_ = priv2
			parts := strings.Split(tok, ".")
			parts[0] = pub2 // someone else's key with our signature
			_, e := Verify(strings.Join(parts, "."), "vault1prov", root, now)
			return e
		},
		"tampered expiry": func() error {
			parts := strings.Split(tok, ".")
			parts[1] = "99999999999"
			_, e := Verify(strings.Join(parts, "."), "vault1prov", root, now)
			return e
		},
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: token accepted", name)
		}
	}
}

func TestFromRequest(t *testing.T) {
	r, _ := http.NewRequest("GET", "http://x/v1/objects/r?vault_token=Q", nil)
	if FromRequest(r) != "Q" {
		t.Fatal("query token")
	}
	Attach(r, "H")
	if FromRequest(r) != "H" {
		t.Fatal("header token wins")
	}
	r2, _ := http.NewRequest("GET", "http://x/", nil)
	r2.Header.Set("Authorization", "Bearer nope")
	if FromRequest(r2) != "" {
		t.Fatal("foreign scheme ignored")
	}
}
