package ibc

import (
	"encoding/json"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/x/bank"
	"github.com/vaultchain/vaultchain/x/storage"
)

const (
	ModuleName             = "ibc"
	MsgTypeSimulateDeposit = "ibc/SimulateIBCDeposit"
	PolicyPath             = "/vaultchain/ibc/policy"
)

// Policy documents ICS-20 payments-only stance.
type Policy struct {
	Mode        string   `json:"mode"`
	Allowed     []string `json:"allowed"`
	Forbidden   []string `json:"forbidden"`
	Note        string   `json:"note"`
	Enforcement string   `json:"enforcement"`
}

func DefaultPolicy() Policy {
	return Policy{
		Mode: "payments-only",
		Allowed: []string{
			"ICS-20 inbound/outbound token transfers",
			"Paying for capacity plans and deals with whitelisted denoms",
		},
		Forbidden: []string{
			"Cross-chain provider discovery or registration",
			"Cross-chain proof submission or deal lifecycle",
			"Storing or routing object bytes over IBC",
			"ICS-27 ICA for storage control",
		},
		Note:        "ICS-20 tokens may fund deals; all storage infrastructure is native to Vaultchain.",
		Enforcement: "x/storage params.payment_denom_whitelist gates storage payment denoms",
	}
}

type MsgSimulateIBCDeposit struct {
	Type   string `json:"type"`
	From   string `json:"-"` // signer / lab operator
	To     string `json:"to"`
	Denom  string `json:"denom"` // e.g. ibc/ATOM
	Amount int64  `json:"amount"`
}

// SimulateDeposit mints a fake IBC voucher for local payment-denom testing only.
func SimulateDeposit(bankState *bank.State, storageState *storage.State, msg MsgSimulateIBCDeposit) error {
	if msg.To == "" || msg.Denom == "" || msg.Amount <= 0 {
		return fmt.Errorf("invalid simulate ibc deposit")
	}
	if len(msg.Denom) < 4 || msg.Denom[:4] != "ibc/" {
		return fmt.Errorf("lab voucher denom must start with ibc/")
	}
	coin := sdk.NewInt64Coin(msg.Denom, msg.Amount)
	if err := bankState.AddCoins(msg.To, sdk.NewCoins(coin)); err != nil {
		return err
	}
	// Auto-whitelist lab voucher so storage payments can use it after mint.
	if !storageState.Params.IsPaymentDenomAllowed(msg.Denom) {
		storageState.Params.PaymentDenomWhitelist = append(storageState.Params.PaymentDenomWhitelist, msg.Denom)
	}
	return nil
}

func PolicyJSON() ([]byte, error) {
	return json.MarshalIndent(DefaultPolicy(), "", "  ")
}
