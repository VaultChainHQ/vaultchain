package node

import (
	"fmt"

	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/ibc"
	"github.com/vaultchain/vaultchain/x/storage"
)

// ApplyTx verifies and applies one signed transaction to st.
//
// Order: decode+signature, chain id, nonce, fee. A tx failing any of those is
// rejected without touching state. After the fee is charged and the nonce
// consumed, messages run in order.
func ApplyTx(st *ChainState, raw []byte) (string, error) {
	dt, err := DecodeTx(raw)
	if err != nil {
		return "", err
	}
	if dt.Tx.ChainID != st.ChainID {
		return "", fmt.Errorf("wrong chain id: got %q want %q", dt.Tx.ChainID, st.ChainID)
	}
	if st.Accounts == nil {
		st.Accounts = map[string]uint64{}
	}
	if want := st.Accounts[dt.Signer]; dt.Tx.Nonce != want {
		return "", fmt.Errorf("bad nonce: got %d want %d", dt.Tx.Nonce, want)
	}
	if !dt.Tx.Fee.IsZero() {
		if err := st.Bank.PayFee(dt.Signer, dt.Tx.Fee); err != nil {
			return "", fmt.Errorf("fee: %w", err)
		}
	}
	st.Accounts[dt.Signer]++

	height := st.Height + 1 // applied into next block
	for i, m := range dt.Msgs {
		if err := execMsg(st, m, height); err != nil {
			return "", fmt.Errorf("msg %d: %w", i, err)
		}
	}
	return "ok", nil
}

func execMsg(st *ChainState, m any, height int64) error {
	k := storage.Keeper{Storage: st.Storage, Bank: st.Bank}
	switch msg := m.(type) {
	case MsgBankSend:
		return st.Bank.SendCoins(msg.From, msg.To, msg.Amount)
	case storage.MsgRegisterProvider:
		return k.RegisterProvider(msg, height)
	case storage.MsgBuyCapacity:
		return k.BuyCapacity(msg, height)
	case storage.MsgOpenDeal:
		return k.OpenDeal(msg, height)
	case storage.MsgSubmitProof:
		return k.SubmitProof(msg, height)
	case access.MsgGrantAccess:
		return st.Access.Grant(msg, height, st.Storage.OwnerOfCID)
	case ibc.MsgSimulateIBCDeposit:
		return ibc.SimulateDeposit(st.Bank, st.Storage, msg)
	default:
		return fmt.Errorf("unhandled msg %T", m)
	}
}
