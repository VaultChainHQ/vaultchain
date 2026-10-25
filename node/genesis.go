package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/x/storage"
)

type GenesisAccount struct {
	Address string    `json:"address"`
	Coins   sdk.Coins `json:"coins"`
}

type Genesis struct {
	ChainID     string           `json:"chain_id"`
	GenesisTime time.Time        `json:"genesis_time"`
	Accounts    []GenesisAccount `json:"accounts"`
	Storage     storage.Params   `json:"storage_params"`
	// LabMode enables lab-only features such as ibc/SimulateIBCDeposit. It is
	// recorded in state at genesis and cannot be changed afterwards.
	LabMode bool `json:"lab_mode"`
	// Validators is the initial consensus validator set (CometBFT mode only;
	// the timer engine ignores it).
	Validators []GenesisValidator `json:"validators,omitempty"`
}

// GenesisValidator is one initial validator.
type GenesisValidator struct {
	Name string `json:"name"`
	// PubKey is the base64 ed25519 consensus public key.
	PubKey string `json:"pub_key"`
	Power  int64  `json:"power"`
	// Account is the vault1... address that receives the tx fees of blocks this
	// validator proposes.
	Account string `json:"account"`
}

func LoadGenesis(home string) (*Genesis, error) {
	path := filepath.Join(home, "config", "genesis.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Genesis
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("parse genesis: %w", err)
	}
	return &g, nil
}

func WriteGenesis(home string, g *Genesis) error {
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "genesis.json"), b, 0o644)
}
