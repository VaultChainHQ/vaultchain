package storage

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/x/bank"
)

// Keeper applies storage messages against bank + storage state.
type Keeper struct {
	Storage *State
	Bank    *bank.State
}

func (k Keeper) EnsurePaymentDenom(denom string) error {
	if !k.Storage.Params.IsPaymentDenomAllowed(denom) {
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
	k.Storage.Providers[msg.From] = Provider{
		Address:    msg.From,
		Endpoint:   msg.Endpoint,
		Capacity:   msg.Capacity,
		FreeBytes:  msg.Capacity,
		Collateral: msg.Collateral,
		Status:     "active",
	}
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
	amount := int64(units) * k.Storage.Params.PricePerGB * factor
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
	id := k.Storage.NextPlanID
	k.Storage.NextPlanID++
	k.Storage.Plans[id] = CapacityPlan{
		ID:        id,
		Owner:     msg.From,
		Bytes:     msg.Bytes,
		BytesUsed: 0,
		StartH:    height,
		EndH:      height + msg.Duration,
		Payment:   payment,
	}
	return nil
}

func (k Keeper) OpenDeal(msg MsgOpenDeal, height int64) error {
	if msg.From == "" || msg.CID == "" || msg.Size == 0 || msg.Provider == "" || msg.Duration <= 0 {
		return fmt.Errorf("invalid open deal fields")
	}
	if msg.Class != ClassMarket && msg.Class != ClassArchive {
		return fmt.Errorf("class must be market or archive")
	}
	if err := k.EnsurePaymentDenom(msg.Denom); err != nil {
		return err
	}
	prov, ok := k.Storage.Providers[msg.Provider]
	if !ok || prov.Status != "active" {
		return fmt.Errorf("provider not active: %s", msg.Provider)
	}
	if prov.FreeBytes < msg.Size {
		return fmt.Errorf("provider lacks capacity")
	}
	replication := msg.Replication
	if replication == 0 {
		if msg.Class == ClassArchive {
			replication = 3
		} else {
			replication = 1
		}
	}
	payment := k.priceForBytes(msg.Size, msg.Duration, msg.Denom)
	if msg.Class == ClassArchive {
		// Archive prepaid premium.
		payment = sdk.NewCoin(payment.Denom, payment.Amount.MulRaw(int64(replication)))
	}
	if err := k.Bank.SendCoins(msg.From, bank.EscrowAddress(), sdk.NewCoins(payment)); err != nil {
		return err
	}
	prov.FreeBytes -= msg.Size
	k.Storage.Providers[msg.Provider] = prov

	id := k.Storage.NextDealID
	k.Storage.NextDealID++
	k.Storage.Deals[id] = Deal{
		ID:          id,
		Class:       msg.Class,
		Owner:       msg.From,
		Provider:    msg.Provider,
		CID:         msg.CID,
		Size:        msg.Size,
		StartH:      height,
		ExpireH:     height + msg.Duration,
		Payment:     payment,
		Replication: replication,
		LastProvenH: 0,
		Status:      "open",
	}
	return nil
}

func (k Keeper) SubmitProof(msg MsgSubmitProof, height int64) error {
	if msg.From == "" || msg.CID == "" || msg.DealID == 0 {
		return fmt.Errorf("invalid submit proof fields")
	}
	deal, ok := k.Storage.Deals[msg.DealID]
	if !ok {
		return fmt.Errorf("deal %d not found", msg.DealID)
	}
	if deal.Status != "open" {
		return fmt.Errorf("deal not open")
	}
	if deal.CID != msg.CID {
		return fmt.Errorf("cid mismatch")
	}
	if deal.Provider != msg.From {
		return fmt.Errorf("only assigned provider may submit proof")
	}
	if msg.Attestation == "" {
		return fmt.Errorf("attestation required")
	}
	deal.LastProvenH = height
	k.Storage.Deals[msg.DealID] = deal

	id := k.Storage.NextProofID
	k.Storage.NextProofID++
	k.Storage.Proofs[id] = Proof{
		ID:          id,
		DealID:      msg.DealID,
		CID:         msg.CID,
		Provider:    msg.From,
		Height:      height,
		Attestation: msg.Attestation,
	}
	return nil
}
