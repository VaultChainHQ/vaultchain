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
	e := &Engine{cfg: cfg, store: store, genesis: g}
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

func (e *Engine) SubmitTx(raw json.RawMessage) (string, error) {
	// Validate by dry-applying against a clone would be nicer; for v1 enqueue after type check.
	var meta struct {
		Type string `json:"type"`
		From string `json:"from"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", err
	}
	if meta.Type == "" || meta.From == "" {
		return "", fmt.Errorf("tx requires type and from")
	}
	hash := HashTx(raw)
	e.mpMu.Lock()
	e.mempool = append(e.mempool, raw)
	e.mpMu.Unlock()
	return hash, nil
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
	txs := e.mempool
	e.mempool = nil
	e.mpMu.Unlock()

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
