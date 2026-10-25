package node

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/VaultChainHQ/vaultchain/kv"
	"github.com/VaultChainHQ/vaultchain/x/bank"
)

// ValidatorAddress is the CometBFT consensus address (lower-case hex) of a
// base64 ed25519 public key.
func ValidatorAddress(pubKeyB64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return "", fmt.Errorf("pub_key: %w", err)
	}
	if len(b) != ed25519.PubKeySize {
		return "", fmt.Errorf("pub_key: want %d bytes, got %d", ed25519.PubKeySize, len(b))
	}
	return hex.EncodeToString(ed25519.PubKey(b).Address()), nil
}

// ABCIApp adapts App to CometBFT's ABCI. CometBFT supplies ordering, the
// validator set, block time and proposer; the deterministic state machine in
// App does the rest, so the app hash is the real state hash.
//
//   - CheckTx: structure, signature, chain id, fee floor, nonce (against
//     committed state plus txs this node already accepted) and fee
//     affordability. The same function re-checks the mempool after each commit.
//   - FinalizeBlock: App.PrepareBlock (no writes) -> app hash.
//   - Commit: App.CommitBlock (one atomic write).
type ABCIApp struct {
	abci.BaseApplication

	app    *App
	minFee sdk.Coins

	mu      sync.Mutex
	pending map[string]uint64 // signer -> next nonce incl. txs admitted to the mempool
	staged  *PendingBlock
}

// NewABCIApp wraps app. minFee is the node-local mempool fee floor.
func NewABCIApp(app *App, minFee sdk.Coins) *ABCIApp {
	return &ABCIApp{app: app, minFee: minFee, pending: map[string]uint64{}}
}

const (
	codeOK       uint32 = 0
	codeRejected uint32 = 1
)

// Info reports the committed height and app hash so CometBFT can replay any
// blocks the app is missing after a crash.
func (a *ABCIApp) Info(_ context.Context, _ *abci.RequestInfo) (*abci.ResponseInfo, error) {
	h, _ := hex.DecodeString(a.app.AppHash())
	return &abci.ResponseInfo{
		Data:             "vaultchain",
		Version:          "0.1.0",
		AppVersion:       1,
		LastBlockHeight:  a.app.Height(),
		LastBlockAppHash: h,
	}, nil
}

// InitChain loads the genesis app_state. It is idempotent while the chain is
// still at height 0 (CometBFT may call it again after a crash before block 1).
func (a *ABCIApp) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	if !a.app.Initialized() {
		var g Genesis
		if err := json.Unmarshal(req.AppStateBytes, &g); err != nil {
			return nil, fmt.Errorf("app_state: %w", err)
		}
		if g.ChainID == "" {
			g.ChainID = req.ChainId
		}
		if g.ChainID != req.ChainId {
			return nil, fmt.Errorf("app_state chain_id %q != genesis chain_id %q", g.ChainID, req.ChainId)
		}
		g.GenesisTime = req.Time
		if err := a.app.InitChain(&g); err != nil {
			return nil, err
		}
	} else if a.app.Height() != 0 {
		return nil, fmt.Errorf("InitChain on a chain at height %d", a.app.Height())
	} else if a.app.ChainID() != req.ChainId {
		return nil, fmt.Errorf("database chain id %q != genesis chain id %q", a.app.ChainID(), req.ChainId)
	}
	h, _ := hex.DecodeString(a.app.AppHash())
	return &abci.ResponseInitChain{AppHash: h}, nil
}

// CheckTx admits a tx to this node's mempool.
func (a *ABCIApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	if err := a.checkTx(req.Tx); err != nil {
		return &abci.ResponseCheckTx{Code: codeRejected, Log: err.Error()}, nil
	}
	return &abci.ResponseCheckTx{Code: codeOK, GasWanted: 1}, nil
}

func (a *ABCIApp) checkTx(raw []byte) error {
	dt, err := DecodeTx(raw)
	if err != nil {
		return err
	}
	if !dt.Tx.Fee.IsAllGTE(a.minFee) {
		return fmt.Errorf("fee below this node's minimum (%s)", a.minFee)
	}
	var chainID string
	var next uint64
	var canPayFee bool
	_ = a.app.View(func(s kv.Store) error {
		chainID = a.app.ChainID()
		next = Nonce(s, dt.Signer)
		canPayFee = bank.NewKeeper(s).GetBalance(dt.Signer).IsAllGTE(dt.Tx.Fee)
		return nil
	})
	if dt.Tx.ChainID != chainID {
		return fmt.Errorf("wrong chain id: got %q want %q", dt.Tx.ChainID, chainID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.pending[dt.Signer]; ok && p > next {
		next = p
	}
	if dt.Tx.Nonce != next {
		return fmt.Errorf("bad nonce: got %d want %d", dt.Tx.Nonce, next)
	}
	if !canPayFee {
		return fmt.Errorf("insufficient funds for fee")
	}
	a.pending[dt.Signer] = next + 1
	return nil
}

// PendingNonce is the next nonce to use for addr once the txs in this node's
// mempool are included.
func (a *ABCIApp) PendingNonce(addr string) uint64 {
	n := a.app.Nonce(addr)
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.pending[addr]; ok && p > n {
		return p
	}
	return n
}

// FinalizeBlock executes the block against committed state and returns the
// resulting app hash. Nothing is written until Commit.
func (a *ABCIApp) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	t := normTime(req.Time)
	if last := a.app.LastBlockTime(); !t.After(last) {
		// Block 1 carries the genesis time, and ms truncation can collide:
		// clamp deterministically so every node derives the same time.
		t = last.Add(time.Millisecond)
	}
	proposer := a.app.ProposerAccount(hex.EncodeToString(req.ProposerAddress))
	in := BlockInput{Height: req.Height, Time: t, Proposer: proposer, Txs: req.Txs}
	pb, err := a.app.PrepareBlock(in)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.staged = pb
	a.mu.Unlock()

	results := make([]*abci.ExecTxResult, len(pb.Result.Results))
	for i, r := range pb.Result.Results {
		code := codeOK
		if !r.OK {
			code = codeRejected
		}
		results[i] = &abci.ExecTxResult{Code: code, Log: r.Log}
	}
	hash, _ := hex.DecodeString(pb.Result.Header.AppHash)
	return &abci.ResponseFinalizeBlock{TxResults: results, AppHash: hash}, nil
}

// Commit writes the staged block. The mempool is then re-checked by CometBFT,
// which rebuilds the pending-nonce view from committed state.
func (a *ABCIApp) Commit(_ context.Context, _ *abci.RequestCommit) (*abci.ResponseCommit, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.staged == nil {
		return nil, fmt.Errorf("commit without finalized block")
	}
	if err := a.app.CommitBlock(a.staged); err != nil {
		return nil, err
	}
	if h := a.staged.Result.Header; h.Height%10 == 0 || h.TxCount > 0 {
		log.Printf("committed block height=%d txs=%d ok=%d apphash=%s", h.Height, h.TxCount, h.OKCount, h.AppHash[:16])
	}
	a.staged = nil
	a.pending = map[string]uint64{}
	return &abci.ResponseCommit{}, nil
}
