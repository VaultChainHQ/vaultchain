package storage

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/x/bank"
)

// Keeper applies storage messages against bank + storage state in a kv store.
type Keeper struct {
	S    kv.Store
	Bank bank.Keeper
}

func NewKeeper(s kv.Store) Keeper { return Keeper{S: s, Bank: bank.NewKeeper(s)} }

func (k Keeper) EnsurePaymentDenom(denom string) error {
	if !k.Params().IsPaymentDenomAllowed(denom) {
		return fmt.Errorf("denom %q not in payment_denom_whitelist", denom)
	}
	return nil
}

func (k Keeper) RegisterProvider(msg MsgRegisterProvider, height int64) error {
	_ = height
	if msg.From == "" || msg.Endpoint == "" || msg.Capacity == 0 {
		return fmt.Errorf("invalid register provider fields")
	}
	if !msg.Collateral.IsValid() || msg.Collateral.IsZero() {
		return fmt.Errorf("collateral required")
	}
	if err := k.Bank.SendCoins(msg.From, bank.EscrowAddress(), sdk.NewCoins(msg.Collateral)); err != nil {
		return err
	}
	k.SetProvider(Provider{
		Address:    msg.From,
		Endpoint:   msg.Endpoint,
		Capacity:   msg.Capacity,
		FreeBytes:  msg.Capacity,
		Collateral: msg.Collateral,
		Status:     "active",
	})
	return nil
}

func (k Keeper) priceForBytes(bytes uint64, durationBlocks int64, denom string) sdk.Coin {
	// Simple linear pricing: ceil(bytes/1GiB) * price_per_gb * max(1, duration/1000)
	const gib = 1 << 30
	units := bytes / gib
	if bytes%gib != 0 {
		units++
	}
	if units == 0 {
		units = 1
	}
	factor := durationBlocks / 1000
	if factor < 1 {
		factor = 1
	}
	amount := int64(units) * k.Params().PricePerGB * factor
	if amount < 1 {
		amount = 1
	}
	return sdk.NewInt64Coin(denom, amount)
}

func (k Keeper) BuyCapacity(msg MsgBuyCapacity, height int64) error {
	if msg.From == "" || msg.Bytes == 0 || msg.Duration <= 0 {
		return fmt.Errorf("invalid buy capacity fields")
	}
	if err := k.EnsurePaymentDenom(msg.Denom); err != nil {
		return err
	}
	payment := k.priceForBytes(msg.Bytes, msg.Duration, msg.Denom)
	if err := k.Bank.SendCoins(msg.From, bank.EscrowAddress(), sdk.NewCoins(payment)); err != nil {
		return err
	}
	id := k.nextID("plan")
	k.SetPlan(CapacityPlan{
		ID:        id,
		Owner:     msg.From,
		Bytes:     msg.Bytes,
		BytesUsed: 0,
		StartH:    height,
		EndH:      height + msg.Duration,
		Payment:   payment,
	})
	return nil
}
