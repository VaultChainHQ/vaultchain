package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/VaultChainHQ/vaultchain/node"
	vtypes "github.com/VaultChainHQ/vaultchain/types"
)

func cmdStart() *cobra.Command {
	var rpcAddr, restAddr, minFeeStr, clientIPHeader string
	var consensus string
	var comet node.CometOptions
	var txRate int
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local single-validator vaultchain node",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := homeFlag(cmd)
			minFee, err := parseCoins(minFeeStr)
			if err != nil {
				return fmt.Errorf("--min-fee: %w", err)
			}
			eng, err := node.NewEngine(node.Config{
				Home:          home,
				RPCAddr:       rpcAddr,
				RESTAddr:      restAddr,
				BlockInterval: interval,
				// The admin secret comes from the environment, never a flag
				// (flags show up in process listings and logs).
				AdminToken:     os.Getenv(adminTokenEnv),
				MinFee:         minFee.Sort(),
				TxRatePerMin:   txRate,
				ClientIPHeader: clientIPHeader,
				Consensus:      consensus,
				Comet:          comet,
			})
			if err != nil {
				return err
			}
			defer eng.Close()

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return eng.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&rpcAddr, "rpc-addr", vtypes.DefaultRPCAddr, "RPC listen address")
	cmd.Flags().StringVar(&restAddr, "api-addr", vtypes.DefaultRESTAddr, "REST listen address")
	cmd.Flags().StringVar(&minFeeStr, "min-fee", "", "minimum tx fee this node admits to its mempool, e.g. 1000uvault (empty: none)")
	cmd.Flags().IntVar(&txRate, "tx-rate", 0, "max POST /vaultchain/txs per client IP per minute from public addresses (0: unlimited)")
	cmd.Flags().StringVar(&clientIPHeader, "client-ip-header", "", "trusted proxy header with the real client IP, e.g. Fly-Client-IP")
	cmd.Flags().DurationVar(&interval, "block-interval", 2*time.Second, "block production interval (timer engine) / CometBFT timeout_commit")
	cmd.Flags().StringVar(&consensus, "consensus", node.ConsensusTimer, "consensus engine: timer (single node, default) or comet (CometBFT multi-validator)")
	cmd.Flags().StringVar(&comet.Moniker, "moniker", "", "comet: node moniker")
	cmd.Flags().StringVar(&comet.P2PAddr, "p2p-addr", "tcp://0.0.0.0:26656", "comet: p2p listen address")
	cmd.Flags().StringVar(&comet.PersistentPeers, "persistent-peers", "", "comet: comma separated id@host:port peers")
	cmd.Flags().StringVar(&comet.ExternalAddr, "external-addr", "", "comet: address advertised to peers")
	cmd.Flags().StringVar(&comet.RPCAddr, "comet-rpc-addr", "tcp://127.0.0.1:26657", "comet: CometBFT RPC listen address")
	cmd.Flags().StringVar(&comet.LogLevel, "comet-log-level", "error", "comet: log level (debug, info, warn, error)")
	return cmd
}

// adminTokenEnv names the environment variable holding the node admin secret.
// On the node it enables lab-only messages over REST; on the CLI it is sent
// as the X-Admin-Token header by `vaultd tx`.
const adminTokenEnv = "VAULT_ADMIN_TOKEN"
