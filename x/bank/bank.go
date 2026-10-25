package bank

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/VaultChainHQ/vaultchain/kv"
	vtypes "github.com/VaultChainHQ/vaultchain/types"
)

const ModuleEscrow = "vaultchain-storage-escrow"
const FeeCollector = "vaultchain-fee-collector"

var balPrefix = []byte("bank/bal/")

func balKey(addr string) []byte { return append(append([]byte(nil), balPrefix...), addr...) }

// Keeper reads and writes balances (one kv entry per account).
type Keeper struct {
	S kv.Store
}

func NewKeeper(s kv.Store) Keeper { return Keeper{S: s} }

func (k Keeper) GetBalance(addr string) sdk.Coins {
	var c sdk.Coins
	if !kv.GetJSON(k.S, balKey(addr), &c) {
		return sdk.NewCoins()
	}
	return c.Sort()
}

func (k Keeper) SetBalance(addr string, coins sdk.Coins) {
	cleaned := coins.Sort()
	if cleaned.IsZero() {
		k.S.Delete(balKey(addr))
		return
	}
	kv.SetJSON(k.S, balKey(addr), cleaned)
}

// Balances lists every non-empty account in address order.
func (k Keeper) Balances() map[string]sdk.Coins {
	out := map[string]sdk.Coins{}
	kv.IterateJSON(k.S, balPrefix, func(key []byte, c sdk.Coins) bool {
		out[string(key[len(balPrefix):])] = c
		return true
	})
	return out
}

func (k Keeper) AddCoins(addr string, coins sdk.Coins) error {
	if !coins.IsValid() {
		return fmt.Errorf("invalid coins: %s", coins)
	}
	k.SetBalance(addr, k.GetBalance(addr).Add(coins...))
	return nil
}

func (k Keeper) SubCoins(addr string, coins sdk.Coins) error {
	if !coins.IsValid() {
		return fmt.Errorf("invalid coins: %s", coins)
	}
	bal := k.GetBalance(addr)
	newBal, neg := bal.SafeSub(coins...)
	if neg {
		return fmt.Errorf("insufficient funds: have %s need %s", bal, coins)
	}
	k.SetBalance(addr, newBal)
	return nil
}

func (k Keeper) SendCoins(from, to string, coins sdk.Coins) error {
	if err := k.SubCoins(from, coins); err != nil {
		return err
	}
	return k.AddCoins(to, coins)
}

func EscrowAddress() string {
	return ModuleEscrow
}

func DefaultGenesisAccount(addr string, amount int64) (string, sdk.Coins) {
	return addr, sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, amount))
}
