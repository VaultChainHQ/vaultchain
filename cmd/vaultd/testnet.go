package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/vaultchain/vaultchain/node"
	vtypes "github.com/vaultchain/vaultchain/types"
)

// cmdTestnet generates the files for an N-validator CometBFT network: one home
// per node with the shared genesis, that node's consensus and p2p keys and a
// funded "validator" account, plus persistent_peers.txt. The output contains
// private keys: keep it out of git (build/ is ignored).
func cmdTestnet() *cobra.Command {
	var o node.NetworkOptions
	var funds string
	cmd := &cobra.Command{
		Use:   "testnet",
		Short: "Generate the files for an N-validator CometBFT network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			coins, err := parseCoins(funds)
			if err != nil {
				return fmt.Errorf("--validator-funds: %w", err)
			}
			o.Funds = coins
			vtypes.ConfigureBech32()
			if _, err := node.GenerateNetwork(o); err != nil {
				return err
			}
			cmd.Printf("Generated %d-validator network %q in %s (contains private keys: do not commit)\n",
				o.Validators, o.ChainID, o.Out)
			return nil
		},
	}
	cmd.Flags().IntVar(&o.Validators, "validators", 3, "number of validators")
	cmd.Flags().StringVar(&o.ChainID, "chain-id", "vault-devnet-2", "chain id")
	cmd.Flags().StringVar(&o.Out, "out", "build/testnet", "output directory")
	cmd.Flags().StringVar(&o.HostPattern, "hostname-pattern", "node%d",
		"peer hostname; %d is replaced by the validator number (e.g. node%d, vaultl1-val-%d.internal, or a fixed 127.0.0.1)")
	cmd.Flags().IntVar(&o.P2PPort, "p2p-port", 26656, "p2p port used in persistent_peers.txt")
	cmd.Flags().IntVar(&o.P2PPortStep, "p2p-port-step", 0, "add this to the p2p port for each further validator (nodes sharing one host)")
	cmd.Flags().StringVar(&funds, "validator-funds", "100000000000000uvault", "genesis balance of each validator account")
	cmd.Flags().BoolVar(&o.LabMode, "lab-mode", false, "enable lab-only features (ibc mint-voucher); fixed at genesis")
	return cmd
}
