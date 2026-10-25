package main

import (
	"os"

	vtypes "github.com/VaultChainHQ/vaultchain/types"
)

func main() {
	vtypes.ConfigureBech32()
	if err := NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
