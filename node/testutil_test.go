package node

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	vtypes "github.com/vaultchain/vaultchain/types"
)

type testAcct struct {
	priv *secp256k1.PrivKey
	pub  string
	addr string
}

func newAcct(t *testing.T) testAcct {
	t.Helper()
	priv := secp256k1.GenPrivKey()
	addr, err := vcrypto.AddressFromPubKey(priv.PubKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return testAcct{priv: priv, pub: hex.EncodeToString(priv.PubKey().Bytes()), addr: addr}
}

func uv(n int64) sdk.Coins { return sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, n)) }

func (a testAcct) tx(t *testing.T, chainID string, nonce uint64, fee sdk.Coins, msgs ...any) []byte {
	t.Helper()
	raw, err := NewSignedTx(chainID, nonce, msgs, fee, a.pub, func(b []byte) ([]byte, error) { return a.priv.Sign(b) })
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func mustCoins(t *testing.T, s string) sdk.Coins {
	t.Helper()
	c, err := sdk.ParseCoinsNormalized(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
