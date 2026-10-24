package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vaultchain/vaultchain/node"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/storage"
)

func cmdInit() *cobra.Command {
	var chainID string
	var labMode bool
	cmd := &cobra.Command{
		Use:   "init [moniker]",
		Short: "Initialize node home and genesis",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := homeFlag(cmd)
			moniker := "vault-local"
			if len(args) == 1 {
				moniker = args[0]
			}
			if chainID == "" {
				chainID = vtypes.DefaultChainID
			}
			cfgDir := filepath.Join(home, "config")
			dataDir := filepath.Join(home, "data")
			if err := os.MkdirAll(cfgDir, 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				return err
			}
			g := &node.Genesis{
				ChainID:     chainID,
				GenesisTime: time.Now().UTC(),
				Accounts:    nil,
				Storage:     storage.DefaultParams(),
				LabMode:     labMode,
			}
			if err := node.WriteGenesis(home, g); err != nil {
				return err
			}
			clientCfg := fmt.Sprintf("moniker=%s\nchain_id=%s\nrpc=%s\nrest=%s\n",
				moniker, chainID, vtypes.DefaultRPCAddr, vtypes.DefaultRESTAddr)
			if err := os.WriteFile(filepath.Join(cfgDir, "client.toml"), []byte(clientCfg), 0o644); err != nil {
				return err
			}
			cmd.Printf("Initialized vaultchain home at %s (chain-id %s)\n", home, chainID)
			return nil
		},
	}
	cmd.Flags().StringVar(&chainID, "chain-id", vtypes.DefaultChainID, "genesis chain id")
	cmd.Flags().BoolVar(&labMode, "lab-mode", false, "enable lab-only features (ibc mint-voucher); fixed at genesis")
	return cmd
}

func cmdGenesis() *cobra.Command {
	root := &cobra.Command{
		Use:   "genesis",
		Short: "Genesis helpers",
	}
	root.AddCommand(cmdAddGenesisAccount())
	return root
}

func cmdAddGenesisAccount() *cobra.Command {
	return &cobra.Command{
		Use:   "add-genesis-account [address] [coins]",
		Short: "Add an account to genesis.json",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := homeFlag(cmd)
			g, err := node.LoadGenesis(home)
			if err != nil {
				return err
			}
			coins, err := parseCoins(args[1])
			if err != nil {
				return err
			}
			addr := strings.TrimSpace(args[0])
			if addr == "" {
				return fmt.Errorf("address required")
			}
			g.Accounts = append(g.Accounts, node.GenesisAccount{
				Address: addr,
				Coins:   coins,
			})
			if err := node.WriteGenesis(home, g); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "added genesis account %s with %s\n", addr, coins)
			return nil
		},
	}
}
