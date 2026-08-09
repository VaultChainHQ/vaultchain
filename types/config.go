package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	Bech32Prefix = "vault"
	NativeDenom  = "uvault"
	DefaultChainID = "vault-local-1"
	AppName      = "vaultchain"
	BinaryName   = "vaultd"

	DefaultRPCAddr  = "127.0.0.1:26657"
	DefaultRESTAddr = "127.0.0.1:1317"
	DefaultHomeDir  = ".vaultchain"
)

// ConfigureBech32 sets the global SDK config to use the vault prefix.
func ConfigureBech32() {
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount(Bech32Prefix, Bech32Prefix+"pub")
	config.SetBech32PrefixForValidator(Bech32Prefix+"valoper", Bech32Prefix+"valoperpub")
	config.SetBech32PrefixForConsensusNode(Bech32Prefix+"valcons", Bech32Prefix+"valconspub")
	config.Seal()
}
