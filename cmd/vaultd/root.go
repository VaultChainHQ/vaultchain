package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func defaultHome() string {
	if h := os.Getenv("VAULTCHAIN_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".vaultchain"
	}
	return filepath.Join(home, ".vaultchain")
}

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "vaultd",
		Short: "Vaultchain local L1 daemon and CLI",
	}
	root.PersistentFlags().String("home", defaultHome(), "node home directory")

	root.AddCommand(
		cmdInit(),
		cmdKeys(),
		cmdStart(),
		cmdTestnet(),
		cmdGenesis(),
		cmdQuery(),
		cmdTx(),
		cmdReadToken(),
		cmdVersion(),
	)
	return root
}

func homeFlag(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup("home"); f != nil && f.Changed {
		return f.Value.String()
	}
	if f := cmd.Root().PersistentFlags().Lookup("home"); f != nil {
		return f.Value.String()
	}
	return defaultHome()
}

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("vaultd 0.1.0 (vaultchain local engine)")
		},
	}
}
