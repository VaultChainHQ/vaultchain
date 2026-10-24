package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/x/bank"
)

type Config struct {
	Home          string
	RPCAddr       string
	RESTAddr      string
	BlockInterval time.Duration
	// Proposer is the account that receives tx fees and is recorded in block
	// headers. Empty means fees go to the fee collector module account.
	Proposer string
	// TxRetention is how many blocks of tx records to keep (default 1000).
	TxRetention int64
}

type Engine struct {
	cfg     Config
	db      *kv.BoltDB
	app     *App
	mempool []json.RawMessage
	mpMu    sync.Mutex        // guards mempool, pending, and block production
	pending map[string]uint64 // signer -> next nonce including queued txs
	genesis *Genesis
}

// DBFile is the state database inside <home>/data.
const DBFile = "state.db"

// legacyDBFile held the single-JSON-blob state of earlier versions.
const legacyDBFile = "vaultchain.db"

func NewEngine(cfg Config) (*Engine, error) {
	if cfg.BlockInterval <= 0 {
		cfg.BlockInterval = 2 * time.Second
	}
	dataDir := filepath.Join(cfg.Home, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dataDir, legacyDBFile)); err == nil {
		if _, err := os.Stat(filepath.Join(dataDir, DBFile)); err != nil {
			return nil, fmt.Errorf("found legacy state %s: this version stores state per key and uses signed txs, "+
				"so a fresh devnet is required (stop the node, delete %s and re-run `vaultd init`)",
				filepath.Join(dataDir, legacyDBFile), dataDir)
		}
	}
	g, err := LoadGenesis(cfg.Home)
	if err != nil {
		return nil, fmt.Errorf("load genesis: %w", err)
	}
	db, err := kv.OpenBolt(filepath.Join(dataDir, DBFile))
	if err != nil {
		return nil, err
	}
	app := NewApp(db, AppConfig{TxRetention: cfg.TxRetention})
	if !app.Initialized() {
		if err := app.InitChain(g); err != nil {
			_ = db.Close()
			return nil, err
		}
	} else if app.ChainID() != g.ChainID {
		_ = db.Close()
		return nil, fmt.Errorf("genesis chain_id %q does not match database chain_id %q", g.ChainID, app.ChainID())
	}
	return &Engine{cfg: cfg, db: db, app: app, genesis: g, pending: map[string]uint64{}}, nil
}

func (e *Engine) Close() error { return e.db.Close() }

// App exposes the state machine (for tests and tooling).
func (e *Engine) App() *App { return e.app }

const maxMempool = 10000

// SubmitTx runs CheckTx-style validation (structure, signature, chain id,
// nonce against committed+pending state, fee affordability) and queues the tx.
func (e *Engine) SubmitTx(raw json.RawMessage) (string, error) {
	dt, err := DecodeTx(raw)
	if err != nil {
		return "", err
	}
	e.mpMu.Lock()
	defer e.mpMu.Unlock()
	if len(e.mempool) >= maxMempool {
		return "", fmt.Errorf("mempool full")
	}
	var chainID string
	var next uint64
	var canPayFee bool
	_ = e.app.View(func(s kv.Store) error {
		chainID = e.app.ChainID()
		next = Nonce(s, dt.Signer)
		canPayFee = bank.NewKeeper(s).GetBalance(dt.Signer).IsAllGTE(dt.Tx.Fee)
		return nil
	})
	if dt.Tx.ChainID != chainID {
		return "", fmt.Errorf("wrong chain id: got %q want %q", dt.Tx.ChainID, chainID)
	}
	if p, ok := e.pending[dt.Signer]; ok {
		next = p
	}
	if dt.Tx.Nonce != next {
		return "", fmt.Errorf("bad nonce: got %d want %d", dt.Tx.Nonce, next)
	}
	if !canPayFee {
		return "", fmt.Errorf("insufficient funds for fee")
	}
	e.mempool = append(e.mempool, raw)
	e.pending[dt.Signer] = next + 1
	return dt.Hash, nil
}

// AccountNonces returns the committed next nonce and the next nonce to use
// once all queued txs are included.
func (e *Engine) AccountNonces(addr string) (nonce, pending uint64) {
	e.mpMu.Lock()
	defer e.mpMu.Unlock()
	nonce = e.app.Nonce(addr)
	pending = nonce
	if p, ok := e.pending[addr]; ok {
		pending = p
	}
	return
}

func (e *Engine) Run(ctx context.Context) error {
	rpcErr := make(chan error, 1)
	restErr := make(chan error, 1)
	go func() { rpcErr <- e.serveRPC(ctx) }()
	go func() { restErr <- e.serveREST(ctx) }()

	ticker := time.NewTicker(e.cfg.BlockInterval)
	defer ticker.Stop()

	log.Printf("vaultchain node started chain_id=%s height=%d rpc=%s rest=%s",
		e.app.ChainID(), e.app.Height(), e.cfg.RPCAddr, e.cfg.RESTAddr)

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-rpcErr:
			if err != nil {
				return err
			}
		case err := <-restErr:
			if err != nil {
				return err
			}
		case <-ticker.C:
			if err := e.produceBlock(); err != nil {
				log.Printf("block production error: %v", err)
			}
		}
	}
}

// produceBlock drains the mempool into one block. The wall clock is read here,
// once, as block input; the state machine itself never reads it.
func (e *Engine) produceBlock() error {
	e.mpMu.Lock()
	defer e.mpMu.Unlock()
	txs := e.mempool
	e.mempool = nil
	e.pending = map[string]uint64{}

	now := normTime(time.Now())
	if last := e.app.LastBlockTime(); !now.After(last) {
		now = last.Add(time.Millisecond)
	}
	in := BlockInput{Height: e.app.Height() + 1, Time: now, Proposer: e.cfg.Proposer}
	for _, raw := range txs {
		in.Txs = append(in.Txs, raw)
	}
	res, err := e.app.ExecuteBlock(in)
	if err != nil {
		return err
	}
	if h := res.Header; h.Height%10 == 0 || h.TxCount > 0 {
		log.Printf("committed block height=%d txs=%d ok=%d apphash=%s", h.Height, h.TxCount, h.OKCount, h.AppHash[:16])
	}
	return nil
}
