package ibc

import (
	"encoding/json"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/kv"
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

var keyLabMode = []byte("ibc/lab_mode")

// Keeper holds the lab-mode flag and the lab voucher minting path.
type Keeper struct {
	S       kv.Store
	Bank    bank.Keeper
	Storage storage.Keeper
}

func NewKeeper(s kv.Store) Keeper {
	return Keeper{S: s, Bank: bank.NewKeeper(s), Storage: storage.NewKeeper(s)}
}

// SetLabMode is called from genesis only.
func (k Keeper) SetLabMode(on bool) {
	if on {
		k.S.Set(keyLabMode, []byte{1})
	} else {
		k.S.Delete(keyLabMode)
	}
}

func (k Keeper) LabMode() bool { return k.S.Get(keyLabMode) != nil }

// SimulateDeposit mints a fake IBC voucher. It exists for local payment-denom
// testing and only works on chains whose genesis sets lab_mode=true.
func (k Keeper) SimulateDeposit(msg MsgSimulateIBCDeposit) error {
	if !k.LabMode() {
		return fmt.Errorf("ibc/SimulateIBCDeposit is disabled: chain is not in lab_mode")
	}
	if msg.To == "" || msg.Denom == "" || msg.Amount <= 0 {
		return fmt.Errorf("invalid simulate ibc deposit")
	}
	if len(msg.Denom) < 4 || msg.Denom[:4] != "ibc/" {
		return fmt.Errorf("lab voucher denom must start with ibc/")
	}
	coin := sdk.NewInt64Coin(msg.Denom, msg.Amount)
	if err := k.Bank.AddCoins(msg.To, sdk.NewCoins(coin)); err != nil {
		return err
	}
	// Auto-whitelist lab voucher so storage payments can use it after mint.
	params := k.Storage.Params()
	if !params.IsPaymentDenomAllowed(msg.Denom) {
		params.PaymentDenomWhitelist = append(params.PaymentDenomWhitelist, msg.Denom)
		k.Storage.SetParams(params)
	}
	return nil
}
func PolicyJSON() ([]byte, error) {
	return json.MarshalIndent(DefaultPolicy(), "", "  ")
}
