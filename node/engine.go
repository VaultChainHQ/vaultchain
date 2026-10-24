package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

type Config struct {
	Home          string
	RPCAddr       string
	RESTAddr      string
	BlockInterval time.Duration
}

type Engine struct {
	cfg     Config
	store   *Store
	mempool []json.RawMessage
	mpMu    sync.Mutex
	pending map[string]uint64 // signer -> next nonce including queued txs
	genesis *Genesis
}

func NewEngine(cfg Config) (*Engine, error) {
	if cfg.BlockInterval <= 0 {
		cfg.BlockInterval = 2 * time.Second
	}
	store, err := OpenStore(cfg.Home)
	if err != nil {
		return nil, err
	}
	g, err := LoadGenesis(cfg.Home)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load genesis: %w", err)
	}
	e := &Engine{cfg: cfg, store: store, genesis: g, pending: map[string]uint64{}}
	if store.State.Height == 0 && store.State.ChainID == "" {
		if err := store.InitFromGenesis(g); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return e, nil
}

func (e *Engine) Close() error { return e.store.Close() }

func (e *Engine) Store() *Store { return e.store }

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
	e.store.mu.RLock()
	chainID := e.store.State.ChainID
	next := e.store.State.Accounts[dt.Signer]
	bal := e.store.State.Bank.GetBalance(dt.Signer)
	e.store.mu.RUnlock()
	if dt.Tx.ChainID != chainID {
		return "", fmt.Errorf("wrong chain id: got %q want %q", dt.Tx.ChainID, chainID)
	}
	if p, ok := e.pending[dt.Signer]; ok {
		next = p
	}
	if dt.Tx.Nonce != next {
		return "", fmt.Errorf("bad nonce: got %d want %d", dt.Tx.Nonce, next)
	}
	if !bal.IsAllGTE(dt.Tx.Fee) {
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
	e.store.mu.RLock()
	nonce = e.store.State.Accounts[addr]
	e.store.mu.RUnlock()
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
		e.store.State.ChainID, e.store.State.Height, e.cfg.RPCAddr, e.cfg.RESTAddr)

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

func (e *Engine) produceBlock() error {
	e.mpMu.Lock()
	defer e.mpMu.Unlock()
	txs := e.mempool
	e.mempool = nil
	e.pending = map[string]uint64{}

	e.store.mu.Lock()
	defer e.store.mu.Unlock()

	height := e.store.State.Height + 1
	applied := 0
	for _, raw := range txs {
		res, err := ApplyTx(e.store.State, raw)
		rec := TxRecord{
			Hash:   HashTx(raw),
			Height: height,
			Raw:    raw,
			Result: res,
		}
		if err != nil {
			rec.Result = "err: " + err.Error()
		} else {
			applied++
		}
		e.store.State.Txs = append(e.store.State.Txs, rec)
	}

	appHash := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d", e.store.State.ChainID, height, applied)))
	hdr := BlockHeader{
		Height:  height,
		Time:    time.Now().UTC(),
		TxCount: len(txs),
		AppHash: hex.EncodeToString(appHash[:8]),
		ChainID: e.store.State.ChainID,
	}
	e.store.State.Height = height
	e.store.State.Blocks = append(e.store.State.Blocks, hdr)
	// Keep last 256 blocks in memory snapshot.
	if len(e.store.State.Blocks) > 256 {
		e.store.State.Blocks = e.store.State.Blocks[len(e.store.State.Blocks)-256:]
	}
	if err := e.store.PersistUnlocked(); err != nil {
		return err
	}
	if height%10 == 0 || applied > 0 {
		log.Printf("committed block height=%d txs=%d applied=%d apphash=%s", height, len(txs), applied, hdr.AppHash)
	}
	return nil
}
