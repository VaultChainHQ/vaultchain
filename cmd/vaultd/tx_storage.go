package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/pkg/client"
	"github.com/vaultchain/vaultchain/pkg/readauth"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/storage"
)

func cmdTxStorage() *cobra.Command {
	root := &cobra.Command{
		Use:   "storage",
		Short: "Storage module transactions",
	}
	root.AddCommand(
		cmdTxRegisterProvider(),
		cmdTxBuyCapacity(),
		cmdTxCommitManifest(),
		cmdTxOpenDeal(),
		cmdTxAcceptDeal(),
		cmdTxSubmitProof(),
	)
	return root
}

func cmdTxAccess() *cobra.Command {
	root := &cobra.Command{
		Use:   "access",
		Short: "Access module transactions",
	}
	root.AddCommand(cmdTxGrantAccess(), cmdTxRevokeAccess())
	return root
}

func cmdTxRegisterProvider() *cobra.Command {
	return &cobra.Command{
		Use:   "register-provider [endpoint] [capacity_bytes] [collateral]",
		Short: "Register as a storage provider",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cap, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return err
			}
			collateral, err := parseCoin(args[2])
			if err != nil {
				return err
			}
			return postTx(cmd, storage.MsgRegisterProvider{
				Type:       storage.MsgTypeRegisterProvider,
				Endpoint:   args[0],
				Capacity:   cap,
				Collateral: collateral,
			})
		},
	}
}

func cmdTxBuyCapacity() *cobra.Command {
	return &cobra.Command{
		Use:   "buy-capacity [bytes] [duration_blocks] [denom]",
		Short: "Buy personal capacity plan",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			bytesN, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return err
			}
			dur, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, storage.MsgBuyCapacity{
				Type:     storage.MsgTypeBuyCapacity,
				Bytes:    bytesN,
				Duration: dur,
				Denom:    args[2],
			})
		},
	}
}

func cmdTxOpenDeal() *cobra.Command {
	var replication, minReplicas uint32
	cmd := &cobra.Command{
		Use:   "open-deal [class] [manifest_root] [size] [provider|-] [duration_blocks] [denom]",
		Short: "Open market or archive deal for a committed manifest (size 0 = manifest size)",
		Long: `Escrows PerReplica x replication and has the chain assign that many active
providers, chosen deterministically from the previous block hash. Pass a provider
address to pin the first replica, or "-" to let the chain choose all of them.
The deal stays pending until --min-replicas (default: all) providers accept.`,
		Args: cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := strconv.ParseUint(args[2], 10, 64)
			if err != nil {
				return err
			}
			dur, err := strconv.ParseInt(args[4], 10, 64)
			if err != nil {
				return err
			}
			provider := args[3]
			if provider == "-" || provider == "any" {
				provider = ""
			}
			return postTx(cmd, storage.MsgOpenDeal{
				Type:        storage.MsgTypeOpenDeal,
				Class:       args[0],
				CID:         args[1],
				Size:        size,
				Provider:    provider,
				Duration:    dur,
				Denom:       args[5],
				Replication: replication,
				MinReplicas: minReplicas,
			})
		},
	}
	cmd.Flags().Uint32Var(&replication, "replication", 0, "providers assigned to the deal (default 1; archive default 3)")
	cmd.Flags().Uint32Var(&minReplicas, "min-replicas", 0, "providers that must accept for the deal to open (default: all)")
	return cmd
}

func cmdTxAcceptDeal() *cobra.Command {
	return &cobra.Command{
		Use:   "accept-deal [deal_id]",
		Short: "Accept a deal the chain assigned to --from (the provider must already hold the bytes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, storage.MsgAcceptDeal{Type: storage.MsgTypeAcceptDeal, DealID: id})
		},
	}
}

func cmdTxSubmitProof() *cobra.Command {
	var providerURL string
	cmd := &cobra.Command{
		Use:   "submit-proof [deal_id]",
		Short: "Answer the deal's current chunk challenge (fetches the chunk and Merkle path from the provider)",
		Long: `Reads the current challenge of the --from provider's replica from the chain, downloads the challenged
chunk and its Merkle path from --provider-url (GET /v1/chunks/{root}/{i}) and
submits them as a signed proof. Normally vault-provider does this itself
(-home/-key); this command is for manual use and debugging.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dealID, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return err
			}
			if providerURL == "" {
				return fmt.Errorf("--provider-url required")
			}
			self, err := fromAddress(cmd)
			if err != nil {
				return err
			}
			var res struct {
				Challenge storage.Challenge `json:"challenge"`
			}
			if err := httpGetJSON(fmt.Sprintf("%s/vaultchain/storage/challenge/%d?provider=%s", restBase(cmd), dealID, self), &res); err != nil {
				return err
			}
			ch := res.Challenge
			// The provider serves chunks to its own key as an assigned replica; sign for it.
			from, _ := cmd.Flags().GetString("from")
			tok, err := signReadToken(cmd, from, self, ch.Root, time.Minute)
			if err != nil {
				return err
			}
			creq, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/chunks/%s/%d", strings.TrimRight(providerURL, "/"), ch.Root, ch.Index), nil)
			if err != nil {
				return err
			}
			readauth.Attach(creq, tok)
			resp, err := http.DefaultClient.Do(creq)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(io.LimitReader(resp.Body, storage.MaxChunkSize+1))
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("provider returned HTTP %d for chunk %d", resp.StatusCode, ch.Index)
			}
			var path []string
			if h := resp.Header.Get("X-Vault-Proof"); h != "" {
				path = strings.Split(h, ",")
			}
			return postTx(cmd, storage.MsgSubmitProof{
				Type: storage.MsgTypeSubmitProof, DealID: dealID, CID: ch.Root,
				Epoch: ch.Epoch, Chunk: data, Proof: path,
			})
		},
	}
	cmd.Flags().StringVar(&providerURL, "provider-url", "", "provider base URL, e.g. http://127.0.0.1:3333")
	return cmd
}

func cmdTxGrantAccess() *cobra.Command {
	var granteePub string
	cmd := &cobra.Command{
		Use:   "grant [cid] [grantee] [capability] [expiry_height]",
		Short: "Grant read/write access to a CID (grantee * = anyone, read only)",
		Long: `Grant read/write access to a CID. expiry_height 0 means no expiry.

For an encrypted object the file key is also sealed to the grantee (so they can
decrypt): the signer must be the owner and hold the key. The grantee's public key
is looked up on chain (accounts that have sent a transaction); pass
--grantee-pubkey for one that has not. Encrypted objects cannot be granted to *.`,
		Args: cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			exp, err := strconv.ParseInt(args[3], 10, 64)
			if err != nil {
				return err
			}
			msg := access.MsgGrantAccess{
				Type:       access.MsgTypeGrantAccess,
				CID:        args[0],
				Grantee:    args[1],
				Capability: args[2],
				ExpiryH:    exp,
			}
			c := client.New(restBase(cmd))
			m, err := c.Manifest(msg.CID)
			if err != nil && !client.IsNotFound(err) {
				return err
			}
			if err == nil && m.Encrypted() {
				if msg.Grantee == access.Public {
					return fmt.Errorf("an encrypted object cannot be granted publicly")
				}
				rec, err := fromRecord(cmd)
				if err != nil {
					return err
				}
				if msg.KeyEnvelope, err = c.GrantEnvelope(msg.CID, rec, msg.Grantee, granteePub); err != nil {
					return err
				}
			}
			return postTx(cmd, msg)
		},
	}
	cmd.Flags().StringVar(&granteePub, "grantee-pubkey", "", "hex public key of the grantee (default: looked up on chain)")
	return cmd
}

func cmdTxRevokeAccess() *cobra.Command {
	var grantID uint64
	cmd := &cobra.Command{
		Use:   "revoke [cid] [grantee]",
		Short: "Revoke access to a CID (owner only)",
		Long: `Revoke access to a CID. Name a grantee (address, or * for the public grant) to
remove every grant of that CID to them, or use --grant-id (grantee may then be
omitted) to remove one grant. Providers stop serving the object to the grantee
within a few seconds. Revocation cannot make a reader forget a decryption key
they already received: see docs/ENCRYPTION.md.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: args[0], GrantID: grantID}
			if len(args) == 2 {
				msg.Grantee = args[1]
			}
			if msg.Grantee == "" && msg.GrantID == 0 {
				return fmt.Errorf("give a grantee or --grant-id")
			}
			return postTx(cmd, msg)
		},
	}
	cmd.Flags().Uint64Var(&grantID, "grant-id", 0, "revoke just this grant id")
	return cmd
}

// fromRecord returns the keyring record of the --from key.
func fromRecord(cmd *cobra.Command) (vcrypto.KeyRecord, error) {
	name, _ := cmd.Flags().GetString("from")
	if name == "" {
		return vcrypto.KeyRecord{}, fmt.Errorf("--from key name required")
	}
	kr, err := vcrypto.NewKeyring(homeFlag(cmd))
	if err != nil {
		return vcrypto.KeyRecord{}, err
	}
	return kr.Get(name)
}

// commitEncrypted encrypts f, writes the ciphertext to cipherOut, and commits
// (or with dryRun just prints) a manifest over the ciphertext.
func commitEncrypted(cmd *cobra.Command, f *os.File, cipherOut, mimeType, name string, chunkSize int, dryRun bool) error {
	rec, err := fromRecord(cmd)
	if err != nil {
		return fmt.Errorf("--encrypt needs --from (the owner's key): %w", err)
	}
	if cipherOut == "" {
		cipherOut = f.Name() + ".vle"
	}
	msg, err := client.EncryptToFile(f, cipherOut, rec, mimeType, name, chunkSize)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "encrypted: root=%s size=%d ciphertext=%s\n", msg.Root, msg.Size, cipherOut)
	if dryRun {
		b, _ := json.MarshalIndent(msg, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return nil
	}
	return postTx(cmd, msg)
}

func cmdTxCommitManifest() *cobra.Command {
	var (
		mimeType  string
		name      string
		chunkSize int
		dryRun    bool
		encrypt   bool
		cipherOut string
	)
	cmd := &cobra.Command{
		Use:   "commit-manifest [file]",
		Short: "Hash a local file into a chunk manifest and commit it on chain (signer becomes owner)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			if name == "" {
				name = filepath.Base(args[0])
			}
			if mimeType == "" {
				mimeType = mime.TypeByExtension(filepath.Ext(args[0]))
				if i := strings.IndexByte(mimeType, ';'); i >= 0 {
					mimeType = mimeType[:i]
				}
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
			}
			if encrypt {
				return commitEncrypted(cmd, f, cipherOut, mimeType, name, chunkSize, dryRun)
			}
			m, _, err := chunk.Build(f, chunkSize, mimeType, name)
			if err != nil {
				return err
			}
			if dryRun {
				b, _ := json.MarshalIndent(m, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}
			return postTx(cmd, storage.MsgCommitManifest{
				Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size,
				ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name,
			})
		},
	}
	cmd.Flags().StringVar(&mimeType, "mime", "", "media type (default: from file extension)")
	cmd.Flags().StringVar(&name, "name", "", "display name (default: file base name)")
	cmd.Flags().IntVar(&chunkSize, "chunk-size", chunk.DefaultChunkSize, "chunk size in bytes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the manifest instead of submitting")
	cmd.Flags().BoolVar(&encrypt, "encrypt", false, "encrypt client-side first (see docs/ENCRYPTION.md); the manifest then describes the ciphertext")
	cmd.Flags().StringVar(&cipherOut, "cipher-out", "", "with --encrypt: where to write the ciphertext to upload (default: <file>.vle)")
	return cmd
}
