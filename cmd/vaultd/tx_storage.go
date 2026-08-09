package main

import (
	"strconv"

	"github.com/spf13/cobra"

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
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
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
				From:       from,
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
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
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
				From:     from,
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
		Use:   "open-deal [class] [cid] [size] [provider] [duration_blocks] [denom]",
		Short: "Open market or archive deal",
		Args:  cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
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
				From:        from,
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
	return &cobra.Command{
		Use:   "submit-proof [deal_id] [cid] [attestation]",
		Short: "Submit storage proof stub",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
			dealID, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, storage.MsgSubmitProof{
				Type:        storage.MsgTypeSubmitProof,
				From:        from,
				DealID:      dealID,
				CID:         args[1],
				Attestation: args[2],
			})
		},
	}
}

func cmdTxGrantAccess() *cobra.Command {
	return &cobra.Command{
		Use:   "grant [cid] [grantee] [capability] [expiry_height]",
		Short: "Grant access to a CID",
		Args:  cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := fromAddress(cmd)
			if err != nil {
				return err
			}
			exp, err := strconv.ParseInt(args[3], 10, 64)
			if err != nil {
				return err
			}
			return postTx(cmd, access.MsgGrantAccess{
				Type:       access.MsgTypeGrantAccess,
				From:       from,
				CID:        args[0],
				Grantee:    args[1],
				Capability: args[2],
				ExpiryH:    exp,
			})
		},
	}
}
