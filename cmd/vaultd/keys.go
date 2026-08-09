package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
)

func cmdKeys() *cobra.Command {
	root := &cobra.Command{
		Use:   "keys",
		Short: "Manage test keyring",
	}
	root.AddCommand(cmdKeysAdd(), cmdKeysList(), cmdKeysShow())
	return root
}

func cmdKeysAdd() *cobra.Command {
	return &cobra.Command{
		Use:   "add [name]",
		Short: "Create a secp256k1 key in the test keyring",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := vcrypto.NewKeyring(homeFlag(cmd))
			if err != nil {
				return err
			}
			rec, err := kr.Add(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "name: %s\naddress: %s\npubkey: %s\nmnemonic: %s\n",
				rec.Name, rec.Address, rec.PubKeyHex, rec.Mnemonic)
			return nil
		},
	}
}

func cmdKeysList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := vcrypto.NewKeyring(homeFlag(cmd))
			if err != nil {
				return err
			}
			for _, rec := range kr.List() {
				fmt.Fprintf(os.Stdout, "%s\t%s\n", rec.Name, rec.Address)
			}
			return nil
		},
	}
}

func cmdKeysShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show key address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := vcrypto.NewKeyring(homeFlag(cmd))
			if err != nil {
				return err
			}
			rec, err := kr.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, rec.Address)
			return nil
		},
	}
}
