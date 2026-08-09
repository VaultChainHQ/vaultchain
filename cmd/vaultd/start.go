package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vaultchain/vaultchain/node"
	vtypes "github.com/vaultchain/vaultchain/types"
)

func cmdStart() *cobra.Command {
	var rpcAddr, restAddr string
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local single-validator vaultchain node",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := homeFlag(cmd)
			eng, err := node.NewEngine(node.Config{
				Home:          home,
				RPCAddr:       rpcAddr,
				RESTAddr:      restAddr,
				BlockInterval: interval,
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
	cmd.Flags().DurationVar(&interval, "block-interval", 2*time.Second, "block production interval")
	return cmd
}
