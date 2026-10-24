package types

import (
	"fmt"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

const (
	Bech32Prefix   = "vault"
	NativeDenom    = "uvault"
	DefaultChainID = "vault-local-1"
	AppName        = "vaultchain"
	BinaryName     = "vaultd"

	DefaultRPCAddr  = "127.0.0.1:26657"
	DefaultRESTAddr = "127.0.0.1:1317"
	DefaultHomeDir  = ".vaultchain"
)

var bech32Once sync.Once

// ConfigureBech32 sets the global SDK config to use the vault prefix.
// It is safe to call more than once.
func ConfigureBech32() {
	bech32Once.Do(func() {
		config := sdk.GetConfig()
		config.SetBech32PrefixForAccount(Bech32Prefix, Bech32Prefix+"pub")
		config.SetBech32PrefixForValidator(Bech32Prefix+"valoper", Bech32Prefix+"valoperpub")
		config.SetBech32PrefixForConsensusNode(Bech32Prefix+"valcons", Bech32Prefix+"valconspub")
		config.Seal()
	})
}

// ValidateAddress checks that addr is a well-formed vault bech32 account address.
func ValidateAddress(addr string) error {
	hrp, bz, err := bech32.DecodeAndConvert(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if hrp != Bech32Prefix {
		return fmt.Errorf("invalid address %q: want prefix %q", addr, Bech32Prefix)
	}
	if len(bz) != 20 {
		return fmt.Errorf("invalid address %q: want 20 bytes, got %d", addr, len(bz))
	}
	return nil
}
