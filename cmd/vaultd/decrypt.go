package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/pkg/client"
)

// cmdDecrypt decrypts a downloaded ciphertext of an encrypted object.
func cmdDecrypt() *cobra.Command {
	c := &cobra.Command{
		Use:   "decrypt [root]",
		Short: "Decrypt a downloaded encrypted object with the --from key",
		Long: `Decrypt the ciphertext of the encrypted object with the given root (as served by
a provider) into a plaintext file. The file key comes from the owner's envelope
in the manifest, or from a grant to the --from account that carries an envelope,
both read from --node and opened with the --from key. The output is only
written when every segment authenticates; a tampered or truncated download
produces an error and no output file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, _ := cmd.Flags().GetString("in")
			out, _ := cmd.Flags().GetString("out")
			name, _ := cmd.Flags().GetString("from")
			if in == "" || out == "" || name == "" {
				return fmt.Errorf("--in, --out and --from are required")
			}
			kr, err := vcrypto.NewKeyring(homeFlag(cmd))
			if err != nil {
				return err
			}
			rec, err := kr.Get(name)
			if err != nil {
				return err
			}
			src, err := os.Open(in)
			if err != nil {
				return err
			}
			defer src.Close()
			tmp, err := os.CreateTemp(filepath.Dir(out), ".vle-decrypt-*")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			node, _ := cmd.Flags().GetString("node")
			err = client.New(node).DecryptStream(tmp, src, args[0], rec)
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if err := os.Rename(tmp.Name(), out); err != nil {
				// Windows cannot rename over an existing file.
				if _, serr := os.Stat(out); serr == nil {
					os.Remove(out)
					err = os.Rename(tmp.Name(), out)
				}
				if err != nil {
					return err
				}
			}
			st, _ := os.Stat(out)
			fmt.Fprintf(cmd.ErrOrStderr(), "decrypted %d bytes to %s\n", st.Size(), out)
			return nil
		},
	}
	c.Flags().String("from", "", "key name in test keyring (owner or grantee)")
	c.Flags().String("node", "http://127.0.0.1:1317", "REST endpoint")
	c.Flags().String("in", "", "ciphertext file")
	c.Flags().String("out", "", "plaintext output file")
	return c
}
