package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/node"
	"github.com/vaultchain/vaultchain/x/ibc"
	vtypes "github.com/vaultchain/vaultchain/types"
)

func cmdTx() *cobra.Command {
	root := &cobra.Command{
		Use:   "tx",
		Short: "Submit transactions",
	}
	root.PersistentFlags().String("from", "", "key name in test keyring")
	root.PersistentFlags().String("node", "http://"+vtypes.DefaultRESTAddr, "REST endpoint")
	root.AddCommand(
		cmdTxBankSend(),
		cmdTxStorage(),
		cmdTxAccess(),
		cmdTxIBC(),
	)
	return root
}

func fromAddress(cmd *cobra.Command) (string, error) {
	name, _ := cmd.Flags().GetString("from")
	if name == "" {
		return "", fmt.Errorf("--from key name required")
	}
	kr, err := vcrypto.NewKeyring(homeFlag(cmd))
	if err != nil {
		return "", err
	}
	rec, err := kr.Get(name)
	if err != nil {
		return "", err
	}
	return rec.Address, nil
}

func postTx(cmd *cobra.Command, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := restBase(cmd) + "/vaultchain/txs"
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
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
	cmd.Println(string(b))
	return nil
}

func cmdTxBankSend() *cobra.Command {
	return &cobra.Command{
		Use:   "bank-send [to] [amount]",
		Short: "Send coins",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
			coins, err := parseCoins(args[1])
			if err != nil {
				return err
			}
			return postTx(cmd, map[string]any{
				"type":   node.MsgTypeBankSend,
				"from":   from,
				"to":     args[0],
				"amount": coins,
				"fee":    mustCoins("1000uvault"),
			})
		},
	}
}

func cmdTxIBC() *cobra.Command {
	root := &cobra.Command{
		Use:   "ibc-lab",
		Short: "Local IBC payment testing helpers",
	}
	root.AddCommand(&cobra.Command{
		Use:   "mint-voucher [to] [denom] [amount]",
		Short: "Mint a fake IBC voucher (lab only), e.g. ibc/ATOM",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
			amt, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, ibc.MsgSimulateIBCDeposit{
				Type:   ibc.MsgTypeSimulateDeposit,
				From:   from,
				To:     args[0],
				Denom:  args[1],
				Amount: amt,
			})
		},
	})
	return root
}
