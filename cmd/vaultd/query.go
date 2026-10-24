package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	vtypes "github.com/vaultchain/vaultchain/types"
)

func cmdQuery() *cobra.Command {
	root := &cobra.Command{
		Use:   "query",
		Short: "Query node state (via REST or local DB)",
	}
	root.PersistentFlags().String("node", "http://"+vtypes.DefaultRESTAddr, "REST endpoint")
	root.AddCommand(
		cmdQueryStatus(),
		cmdQueryBalances(),
		cmdQueryStorageParams(),
		cmdQueryProviders(),
		cmdQueryDeals(),
		cmdQueryPlans(),
		cmdQueryProofs(),
		cmdQueryGrants(),
		cmdQueryIBCPolicy(),
	)
	return root
}

func restBase(cmd *cobra.Command) string {
	n, _ := cmd.Flags().GetString("node")
	return strings.TrimRight(n, "/")
}

func getJSON(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var pretty any
	if err := json.Unmarshal(b, &pretty); err == nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	}
	_, err = os.Stdout.Write(b)
	return err
}

func cmdQueryStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Node status (RPC)",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := strings.Replace(restBase(cmd), ":1317", ":26657", 1)
			base = strings.Replace(base, "1317", "26657", 1)
			return getJSON(base + "/status")
		},
	}
}

func cmdQueryBalances() *cobra.Command {
	return &cobra.Command{
		Use:   "balances [address]",
		Short: "Account balances",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/cosmos/bank/v1beta1/balances/" + args[0])
		},
	}
}

func cmdQueryStorageParams() *cobra.Command {
	return &cobra.Command{
		Use:   "storage-params",
		Short: "Storage module params",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/vaultchain/storage/params")
		},
	}
}

func cmdQueryProviders() *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List storage providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/vaultchain/storage/providers")
		},
	}
}

func cmdQueryDeals() *cobra.Command {
	return &cobra.Command{
		Use:   "deals",
		Short: "List deals",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/vaultchain/storage/deals")
		},
	}
}

func cmdQueryPlans() *cobra.Command {
	return &cobra.Command{
		Use:   "plans",
		Short: "List capacity plans",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/vaultchain/storage/plans")
		},
	}
}

func cmdQueryProofs() *cobra.Command {
	return &cobra.Command{
		Use:   "proofs",
		Short: "List proofs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/vaultchain/storage/proofs")
		},
	}
}

func cmdQueryGrants() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grants",
		Short: "List access grants",
		RunE: func(cmd *cobra.Command, args []string) error {
			url := restBase(cmd) + "/vaultchain/access/grants"
			if cid, _ := cmd.Flags().GetString("cid"); cid != "" {
				url += "?cid=" + cid
			}
			return getJSON(url)
		},
	}
	cmd.Flags().String("cid", "", "filter by CID")
	return cmd
}

func cmdQueryIBCPolicy() *cobra.Command {
	return &cobra.Command{
		Use:   "ibc-policy",
		Short: "IBC payments-only policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getJSON(restBase(cmd) + "/vaultchain/ibc/policy")
		},
	}
}
