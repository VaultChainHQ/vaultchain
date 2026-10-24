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

	"github.com/spf13/cobra"

	"github.com/vaultchain/vaultchain/pkg/chunk"
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
		cmdTxSubmitProof(),
	)
	return root
}

func cmdTxAccess() *cobra.Command {
	root := &cobra.Command{
		Use:   "access",
		Short: "Access module transactions",
	}
	root.AddCommand(cmdTxGrantAccess())
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
	var replication uint32
	cmd := &cobra.Command{
		Use:   "open-deal [class] [manifest_root] [size] [provider] [duration_blocks] [denom]",
		Short: "Open market or archive deal for a committed manifest (size 0 = manifest size)",
		Args:  cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := strconv.ParseUint(args[2], 10, 64)
			if err != nil {
				return err
			}
			dur, err := strconv.ParseInt(args[4], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, storage.MsgOpenDeal{
				Type:        storage.MsgTypeOpenDeal,
				Class:       args[0],
				CID:         args[1],
				Size:        size,
				Provider:    args[3],
				Duration:    dur,
				Denom:       args[5],
				Replication: replication,
			})
		},
	}
	cmd.Flags().Uint32Var(&replication, "replication", 0, "replication factor (archive default 3)")
	return cmd
}

func cmdTxSubmitProof() *cobra.Command {
	var providerURL string
	cmd := &cobra.Command{
		Use:   "submit-proof [deal_id]",
		Short: "Answer the deal's current chunk challenge (fetches the chunk and Merkle path from the provider)",
		Long: `Reads the deal's current challenge from the chain, downloads the challenged
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
			var res struct {
				Challenge storage.Challenge `json:"challenge"`
			}
			if err := httpGetJSON(fmt.Sprintf("%s/vaultchain/storage/challenge/%d", restBase(cmd), dealID), &res); err != nil {
				return err
			}
			ch := res.Challenge
			resp, err := http.Get(fmt.Sprintf("%s/v1/chunks/%s/%d", strings.TrimRight(providerURL, "/"), ch.Root, ch.Index))
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
	return &cobra.Command{
		Use:   "grant [cid] [grantee] [capability] [expiry_height]",
		Short: "Grant access to a CID",
		Args:  cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			exp, err := strconv.ParseInt(args[3], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, access.MsgGrantAccess{
				Type:       access.MsgTypeGrantAccess,
				CID:        args[0],
				Grantee:    args[1],
				Capability: args[2],
				ExpiryH:    exp,
			})
		},
	}
}

func cmdTxCommitManifest() *cobra.Command {
	var (
		mimeType  string
		name      string
		chunkSize int
		dryRun    bool
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
	return cmd
}
