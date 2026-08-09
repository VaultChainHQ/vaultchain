package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/ibc"
	"github.com/vaultchain/vaultchain/x/storage"
)

type Envelope struct {
	Type string          `json:"type"`
	From string          `json:"from"`
	Fee  sdk.Coins       `json:"fee,omitempty"`
	Body json.RawMessage `json:"-"`
}

func ApplyTx(st *ChainState, raw json.RawMessage) (string, error) {
	var meta struct {
		Type string    `json:"type"`
		From string    `json:"from"`
		Fee  sdk.Coins `json:"fee"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", err
	}
	if meta.Type == "" {
		return "", fmt.Errorf("missing type")
	}
	if meta.From == "" {
		return "", fmt.Errorf("missing from")
	}
	if !meta.Fee.IsZero() {
		if err := st.Bank.PayFee(meta.From, meta.Fee); err != nil {
			return "", fmt.Errorf("fee: %w", err)
		}
	}

	k := storage.Keeper{Storage: st.Storage, Bank: st.Bank}
	height := st.Height + 1 // applied into next block

	switch meta.Type {
	case "bank/Send":
		var msg struct {
			To     string    `json:"to"`
			Amount sdk.Coins `json:"amount"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		if err := st.Bank.SendCoins(meta.From, msg.To, msg.Amount); err != nil {
			return "", err
		}
		return "ok", nil

	case storage.MsgTypeRegisterProvider:
		var msg storage.MsgRegisterProvider
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		msg.From = meta.From
		if err := k.RegisterProvider(msg, height); err != nil {
			return "", err
		}
		return "ok", nil

	case storage.MsgTypeBuyCapacity:
		var msg storage.MsgBuyCapacity
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		msg.From = meta.From
		if err := k.BuyCapacity(msg, height); err != nil {
			return "", err
		}
		return "ok", nil

	case storage.MsgTypeOpenDeal:
		var msg storage.MsgOpenDeal
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		msg.From = meta.From
		if err := k.OpenDeal(msg, height); err != nil {
			return "", err
		}
		return "ok", nil

	case storage.MsgTypeSubmitProof:
		var msg storage.MsgSubmitProof
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		msg.From = meta.From
		if err := k.SubmitProof(msg, height); err != nil {
			return "", err
		}
		return "ok", nil

	case access.MsgTypeGrantAccess:
		var msg access.MsgGrantAccess
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		msg.From = meta.From
		if err := st.Access.Grant(msg, height); err != nil {
			return "", err
		}
		return "ok", nil

	case ibc.MsgTypeSimulateDeposit:
		var msg ibc.MsgSimulateIBCDeposit
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		msg.From = meta.From
		if err := ibc.SimulateDeposit(st.Bank, st.Storage, msg); err != nil {
			return "", err
		}
		return "ok", nil

	default:
		return "", fmt.Errorf("unknown msg type %q", meta.Type)
	}
}

func HashTx(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Ensure bank Send type constant available for CLI.
const MsgTypeBankSend = "bank/Send"
