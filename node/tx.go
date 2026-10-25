package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/ibc"
	"github.com/vaultchain/vaultchain/x/storage"
)

const (
	// MsgTypeBankSend is the bank transfer message type.
	MsgTypeBankSend = "bank/Send"

	// SignDomain prefixes every sign document (domain separation).
	SignDomain = "vaultchain/tx/v1\n"

	MaxTxBytes   = 1 << 20 // a storage proof carries one chunk (<= storage.MaxChunkSize, base64)
	MaxMsgsPerTx = 16
)

// MsgBankSend transfers coins from the tx signer to To.
type MsgBankSend struct {
	Type   string    `json:"type"`
	From   string    `json:"-"` // always the verified signer; never read from the wire
	To     string    `json:"to"`
	Amount sdk.Coins `json:"amount"`
}

// Tx is the canonical signed transaction envelope.
//
//	{chain_id, nonce, msgs, fee, pubkey, sig}
//
// The sender is never transmitted: it is derived from pubkey. The signature
// covers SignDomain || JSON(signDoc), where signDoc is {chain_id, nonce, msgs,
// fee, pubkey} with msgs re-encoded canonically from their decoded Go types.
type Tx struct {
	ChainID string            `json:"chain_id"`
	Nonce   uint64            `json:"nonce"`
	Msgs    []json.RawMessage `json:"msgs"`
	Fee     sdk.Coins         `json:"fee"`
	PubKey  string            `json:"pubkey"` // hex, 33-byte compressed secp256k1
	Sig     string            `json:"sig"`    // hex, 64-byte compact r||s
}

type signDoc struct {
	ChainID string            `json:"chain_id"`
	Nonce   uint64            `json:"nonce"`
	Msgs    []json.RawMessage `json:"msgs"`
	Fee     sdk.Coins         `json:"fee"`
	PubKey  string            `json:"pubkey"`
}

// DecodedTx is a transaction whose structure and signature have been verified.
// Chain id, nonce and fee funds are stateful checks done by the caller.
type DecodedTx struct {
	Tx     Tx
	Msgs   []any  // typed msgs, From populated with Signer
	Signer string // bech32 address derived from Tx.PubKey
	Hash   string
}

func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

// DecodeMsg strictly decodes one message (unknown fields, including any
// client-written "from", are rejected) and validates addresses statelessly.
func DecodeMsg(raw json.RawMessage) (any, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("bad msg: %w", err)
	}
	switch head.Type {
	case MsgTypeBankSend:
		var m MsgBankSend
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		if err := vtypes.ValidateAddress(m.To); err != nil {
			return nil, err
		}
		if !m.Amount.IsValid() || m.Amount.IsZero() {
			return nil, fmt.Errorf("bank/Send: invalid amount")
		}
		return m, nil
	case storage.MsgTypeRegisterProvider:
		var m storage.MsgRegisterProvider
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		return m, nil
	case storage.MsgTypeCommitManifest:
		var m storage.MsgCommitManifest
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		return m, nil
	case storage.MsgTypeBuyCapacity:
		var m storage.MsgBuyCapacity
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		return m, nil
	case storage.MsgTypeOpenDeal:
		var m storage.MsgOpenDeal
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		if m.Provider != "" {
			if err := vtypes.ValidateAddress(m.Provider); err != nil {
				return nil, err
			}
		}
		return m, nil
	case storage.MsgTypeAcceptDeal:
		var m storage.MsgAcceptDeal
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		return m, nil
	case storage.MsgTypeSubmitProof:
		var m storage.MsgSubmitProof
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		return m, nil
	case access.MsgTypeGrantAccess:
		var m access.MsgGrantAccess
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		if m.Grantee != access.Public {
			if err := vtypes.ValidateAddress(m.Grantee); err != nil {
				return nil, err
			}
		}
		return m, nil
	case ibc.MsgTypeSimulateDeposit:
		var m ibc.MsgSimulateIBCDeposit
		if err := strictUnmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", head.Type, err)
		}
		if err := vtypes.ValidateAddress(m.To); err != nil {
			return nil, err
		}
		return m, nil
	case "":
		return nil, fmt.Errorf("missing msg type")
	default:
		return nil, fmt.Errorf("unknown msg type %q", head.Type)
	}
}

func normalizeFee(f sdk.Coins) sdk.Coins {
	if f == nil {
		return sdk.Coins{}
	}
	return f
}

// SignBytes builds the exact bytes that are signed for a transaction.
// msgs may be typed msg values or json.RawMessage; they are canonicalised via
// DecodeMsg so signer and verifier always agree.
func SignBytes(chainID string, nonce uint64, msgs []json.RawMessage, fee sdk.Coins, pubKeyHex string) ([]byte, error) {
	canon := make([]json.RawMessage, len(msgs))
	for i, raw := range msgs {
		m, err := DecodeMsg(raw)
		if err != nil {
			return nil, fmt.Errorf("msg %d: %w", i, err)
		}
		c, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		canon[i] = c
	}
	doc, err := json.Marshal(signDoc{
		ChainID: chainID,
		Nonce:   nonce,
		Msgs:    canon,
		Fee:     normalizeFee(fee),
		PubKey:  pubKeyHex,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(SignDomain), doc...), nil
}

// NewSignedTx builds, signs and encodes a transaction. sign receives the sign
// bytes and must return a 64-byte compact secp256k1 signature.
func NewSignedTx(chainID string, nonce uint64, msgs []any, fee sdk.Coins, pubKeyHex string, sign func([]byte) ([]byte, error)) ([]byte, error) {
	raws := make([]json.RawMessage, len(msgs))
	for i, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		raws[i] = b
	}
	sb, err := SignBytes(chainID, nonce, raws, fee, pubKeyHex)
	if err != nil {
		return nil, err
	}
	sig, err := sign(sb)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Tx{
		ChainID: chainID,
		Nonce:   nonce,
		Msgs:    raws,
		Fee:     normalizeFee(fee),
		PubKey:  pubKeyHex,
		Sig:     hex.EncodeToString(sig),
	})
}

// CheckFee enforces the fee policy: native denom only, valid, sorted coins.
func CheckFee(fee sdk.Coins) error {
	if !fee.IsValid() && !fee.IsZero() {
		return fmt.Errorf("invalid fee %s", fee)
	}
	for _, c := range fee {
		if c.Denom != vtypes.NativeDenom {
			return fmt.Errorf("fee must be paid in %s, got %s", vtypes.NativeDenom, c.Denom)
		}
	}
	return nil
}

// DecodeTx parses a raw tx, decodes all messages, verifies the signature and
// derives the signer. It performs no state checks.
func DecodeTx(raw []byte) (*DecodedTx, error) {
	if len(raw) == 0 || len(raw) > MaxTxBytes {
		return nil, fmt.Errorf("tx size must be 1..%d bytes", MaxTxBytes)
	}
	var tx Tx
	if err := strictUnmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("bad tx: %w", err)
	}
	if tx.ChainID == "" {
		return nil, fmt.Errorf("missing chain_id")
	}
	if n := len(tx.Msgs); n == 0 || n > MaxMsgsPerTx {
		return nil, fmt.Errorf("tx must carry 1..%d msgs", MaxMsgsPerTx)
	}
	pub, err := hex.DecodeString(tx.PubKey)
	if err != nil || len(pub) != vcrypto.PubKeyLen {
		return nil, fmt.Errorf("bad pubkey")
	}
	sig, err := hex.DecodeString(tx.Sig)
	if err != nil || len(sig) != vcrypto.SigLen {
		return nil, fmt.Errorf("bad signature encoding")
	}
	pubHex := hex.EncodeToString(pub)
	sb, err := SignBytes(tx.ChainID, tx.Nonce, tx.Msgs, tx.Fee, pubHex)
	if err != nil {
		return nil, err
	}
	if !vcrypto.VerifySignature(pub, sb, sig) {
		return nil, fmt.Errorf("signature verification failed")
	}
	signer, err := vcrypto.AddressFromPubKey(pub)
	if err != nil {
		return nil, err
	}
	if err := CheckFee(tx.Fee); err != nil {
		return nil, err
	}
	msgs := make([]any, len(tx.Msgs))
	for i, r := range tx.Msgs {
		m, err := DecodeMsg(r)
		if err != nil {
			return nil, fmt.Errorf("msg %d: %w", i, err)
		}
		msgs[i] = withSigner(m, signer)
	}
	return &DecodedTx{Tx: tx, Msgs: msgs, Signer: signer, Hash: HashTx(raw)}, nil
}

func withSigner(m any, signer string) any {
	switch v := m.(type) {
	case MsgBankSend:
		v.From = signer
		return v
	case storage.MsgRegisterProvider:
		v.From = signer
		return v
	case storage.MsgBuyCapacity:
		v.From = signer
		return v
	case storage.MsgOpenDeal:
		v.From = signer
		return v
	case storage.MsgCommitManifest:
		v.From = signer
		return v
	case storage.MsgSubmitProof:
		v.From = signer
		return v
	case storage.MsgAcceptDeal:
		v.From = signer
		return v
	case access.MsgGrantAccess:
		v.From = signer
		return v
	case ibc.MsgSimulateIBCDeposit:
		v.From = signer
		return v
	}
	return m
}

// HashTx returns the hex sha256 of the raw tx bytes.
func HashTx(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
