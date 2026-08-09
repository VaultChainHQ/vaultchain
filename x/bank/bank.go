package bank

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vtypes "github.com/vaultchain/vaultchain/types"
)

const ModuleEscrow = "vaultchain-storage-escrow"
const FeeCollector = "vaultchain-fee-collector"

// State holds account balances keyed by bech32 address.
type State struct {
	Balances map[string]sdk.Coins `json:"balances"`
}

func NewState() *State {
	return &State{Balances: make(map[string]sdk.Coins)}
}

func (s *State) GetBalance(addr string) sdk.Coins {
	if s.Balances == nil {
		s.Balances = make(map[string]sdk.Coins)
	}
	c, ok := s.Balances[addr]
	if !ok {
		return sdk.NewCoins()
	}
	return c.Sort()
}

func (s *State) SetBalance(addr string, coins sdk.Coins) {
	if s.Balances == nil {
		s.Balances = make(map[string]sdk.Coins)
	}
	cleaned := coins.Sort()
	if cleaned.IsZero() {
		delete(s.Balances, addr)
		return
	}
	s.Balances[addr] = cleaned
}

func (s *State) AddCoins(addr string, coins sdk.Coins) error {
	if !coins.IsValid() {
		return fmt.Errorf("invalid coins: %s", coins)
	}
	s.SetBalance(addr, s.GetBalance(addr).Add(coins...))
	return nil
}

func (s *State) SubCoins(addr string, coins sdk.Coins) error {
	if !coins.IsValid() {
		return fmt.Errorf("invalid coins: %s", coins)
	}
	bal := s.GetBalance(addr)
	newBal, neg := bal.SafeSub(coins...)
	if neg {
		return fmt.Errorf("insufficient funds: have %s need %s", bal, coins)
	}
	s.SetBalance(addr, newBal)
	return nil
}

func (s *State) SendCoins(from, to string, coins sdk.Coins) error {
	if err := s.SubCoins(from, coins); err != nil {
		return err
	}
	return s.AddCoins(to, coins)
}

func (s *State) PayFee(from string, fee sdk.Coins) error {
	if fee.IsZero() {
		return nil
	}
	// Burn native fee portion by sending to fee collector (tracked, not redistributed in v1).
	return s.SendCoins(from, FeeCollector, fee)
}

func EscrowAddress() string {
	return ModuleEscrow
}

func DefaultGenesisAccount(addr string, amount int64) (string, sdk.Coins) {
	return addr, sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, amount))
}
