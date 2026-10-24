package crypto

import (
	"encoding/hex"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"

	vtypes "github.com/vaultchain/vaultchain/types"
)

const (
	// PubKeyLen is the compressed secp256k1 public key length.
	PubKeyLen = secp256k1.PubKeySize
	// SigLen is the compact (r||s) signature length.
	SigLen = 64
)

// AddressFromPubKey derives the vault bech32 account address from a compressed
// secp256k1 public key. It does not depend on global SDK bech32 config.
func AddressFromPubKey(pub []byte) (string, error) {
	if len(pub) != PubKeyLen {
		return "", fmt.Errorf("pubkey must be %d bytes, got %d", PubKeyLen, len(pub))
	}
	pk := &secp256k1.PubKey{Key: pub}
	return bech32.ConvertAndEncode(vtypes.Bech32Prefix, pk.Address())
}

// VerifySignature verifies a compact secp256k1 signature over msg (sha256 is applied internally).
// High-S signatures are rejected, so a signature has exactly one valid encoding.
func VerifySignature(pub, msg, sig []byte) bool {
	if len(pub) != PubKeyLen || len(sig) != SigLen {
		return false
	}
	pk := &secp256k1.PubKey{Key: pub}
	return pk.VerifySignature(msg, sig)
}

// SignWithHexKey signs msg with a hex-encoded secp256k1 private key.
func SignWithHexKey(privHex string, msg []byte) ([]byte, error) {
	b, err := hex.DecodeString(privHex)
	if err != nil {
		return nil, err
	}
	priv := &secp256k1.PrivKey{Key: b}
	return priv.Sign(msg)
}
