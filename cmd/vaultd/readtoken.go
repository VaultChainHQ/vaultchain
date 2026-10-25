package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	vcrypto "github.com/VaultChainHQ/vaultchain/crypto"
	"github.com/VaultChainHQ/vaultchain/pkg/readauth"
)

// cmdReadToken prints a short-lived bearer token that lets the --from key read
// one object from one provider (see pkg/readauth).
func cmdReadToken() *cobra.Command {
	c := &cobra.Command{
		Use:   "read-token [root]",
		Short: "Sign a read token for one object at one provider",
		Long: `Sign a short-lived read token for the object with the given root, valid at one
provider only. Send it as "Authorization: Vault <token>" or as ?vault_token=<token>.
The provider lets the token's key read the object if it owns it or holds a read
grant for it. Tokens are bearer credentials: keep the TTL short.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("from")
			provider, _ := cmd.Flags().GetString("provider")
			ttl, _ := cmd.Flags().GetDuration("ttl")
			if name == "" || provider == "" {
				return fmt.Errorf("--from and --provider are required")
			}
			if ttl <= 0 || ttl > readauth.MaxTTL {
				return fmt.Errorf("--ttl must be in (0, %s]", readauth.MaxTTL)
			}
			tok, err := signReadToken(cmd, name, provider, args[0], ttl)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), tok)
			return nil
		},
	}
	c.Flags().String("from", "", "key name in test keyring")
	c.Flags().String("provider", "", "provider address the token is for")
	c.Flags().Duration("ttl", 10*time.Minute, "token lifetime (max 1h)")
	return c
}

// signReadToken signs a token for root at provider with the named test-keyring key.
func signReadToken(cmd *cobra.Command, name, provider, root string, ttl time.Duration) (string, error) {
	kr, err := vcrypto.NewKeyring(homeFlag(cmd))
	if err != nil {
		return "", err
	}
	rec, err := kr.Get(name)
	if err != nil {
		return "", err
	}
	return readauth.Sign(provider, root, time.Now().Add(ttl), rec.PubKeyHex, func(b []byte) ([]byte, error) {
		return kr.Sign(name, b)
	})
}
