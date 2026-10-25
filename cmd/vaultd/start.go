package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vaultchain/vaultchain/node"
	vtypes "github.com/vaultchain/vaultchain/types"
)

func cmdStart() *cobra.Command {
	var rpcAddr, restAddr, minFeeStr, clientIPHeader string
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
	cmd.Flags().DurationVar(&interval, "block-interval", 2*time.Second, "block production interval")
	return cmd
}

// adminTokenEnv names the environment variable holding the node admin secret.
// On the node it enables lab-only messages over REST; on the CLI it is sent
// as the X-Admin-Token header by `vaultd tx`.
const adminTokenEnv = "VAULT_ADMIN_TOKEN"
