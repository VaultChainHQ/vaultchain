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
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/ibc"
)

func cmdTx() *cobra.Command {
	root := &cobra.Command{
		Use:   "tx",
		Short: "Sign and submit transactions",
	}
	root.PersistentFlags().String("from", "", "key name in test keyring (signs the tx)")
	root.PersistentFlags().String("node", "http://"+vtypes.DefaultRESTAddr, "REST endpoint")
	root.PersistentFlags().String("fee", "1000uvault", "tx fee, e.g. 1000uvault (0uvault for none)")
	root.PersistentFlags().String("chain-id", "", "chain id to sign for (default: queried from the node)")
	root.AddCommand(
		cmdTxBankSend(),
		cmdTxStorage(),
		cmdTxAccess(),
		cmdTxIBC(),
	)
	return root
}

func httpGetJSON(url string, out any) error {
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
		return fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, string(b))
	}
	return json.Unmarshal(b, out)
}

// postTx signs msgs with the --from key (account nonce and chain id are
// fetched from the node) and submits the resulting transaction.
func postTx(cmd *cobra.Command, msgs ...any) error {
	name, _ := cmd.Flags().GetString("from")
	if name == "" {
		return fmt.Errorf("--from key name required")
	}
	kr, err := vcrypto.NewKeyring(homeFlag(cmd))
	if err != nil {
		return err
	}
	rec, err := kr.Get(name)
	if err != nil {
		return err
	}
	base := restBase(cmd)

	chainID, _ := cmd.Flags().GetString("chain-id")
	if chainID == "" {
		var info struct {
			Default struct {
				Network string `json:"network"`
			} `json:"default_node_info"`
		}
		if err := httpGetJSON(base+"/cosmos/base/tendermint/v1beta1/node_info", &info); err != nil {
			return err
		}
		chainID = info.Default.Network
	}
	var acct struct {
		Pending uint64 `json:"pending_nonce"`
	}
	if err := httpGetJSON(base+"/vaultchain/auth/accounts/"+rec.Address, &acct); err != nil {
		return err
	}
	feeStr, _ := cmd.Flags().GetString("fee")
	fee, err := parseCoins(feeStr)
	if err != nil {
		return err
	}
	fee = fee.Sort()
	if fee.IsZero() {
		fee = nil
	}
	raw, err := node.NewSignedTx(chainID, acct.Pending, msgs, fee, rec.PubKeyHex, func(b []byte) ([]byte, error) {
		return kr.Sign(name, b)
	})
	if err != nil {
		return err
	}
	resp, err := http.Post(base+"/vaultchain/txs", "application/json", bytes.NewReader(raw))
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
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return nil
}

func cmdTxBankSend() *cobra.Command {
	return &cobra.Command{
		Use:   "bank-send [to] [amount]",
		Short: "Send coins",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			coins, err := parseCoins(args[1])
			if err != nil {
				return err
			}
			return postTx(cmd, node.MsgBankSend{
				Type:   node.MsgTypeBankSend,
				To:     args[0],
				Amount: coins,
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
			amt, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, ibc.MsgSimulateIBCDeposit{
				Type:   ibc.MsgTypeSimulateDeposit,
				To:     args[0],
				Denom:  args[1],
				Amount: amt,
			})
		},
	})
	return root
}

// fromAddress returns the address of the --from key.
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
