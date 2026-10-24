package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

func (e *Engine) serveRPC(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", e.handleStatus)
	mux.HandleFunc("/broadcast_tx_commit", e.handleBroadcast)
	mux.HandleFunc("/broadcast_tx_sync", e.handleBroadcast)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	srv := &http.Server{Addr: e.cfg.RPCAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", e.cfg.RPCAddr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (e *Engine) handleStatus(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	latest := BlockHeader{Height: e.store.State.Height, ChainID: e.store.State.ChainID}
	if n := len(e.store.State.Blocks); n > 0 {
		latest = e.store.State.Blocks[n-1]
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      -1,
		"result": map[string]any{
			"node_info": map[string]any{
				"network":  e.store.State.ChainID,
				"moniker":  "vaultchain-local",
				"version":  "0.1.0",
				"protocol": "vaultchain-local-engine",
			},
			"sync_info": map[string]any{
				"latest_block_height": latest.Height,
				"latest_block_time":   latest.Time,
				"latest_app_hash":     latest.AppHash,
				"catching_up":         false,
			},
			"validator_info": map[string]any{
				"address":      "local-validator",
				"voting_power": "1",
			},
		},
	}
	writeJSON(w, resp)
}

func (e *Engine) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("tx")
	if raw == "" {
		var body struct {
			Tx json.RawMessage `json:"tx"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw = string(body.Tx)
	}
	if raw == "" {
		http.Error(w, "missing tx", http.StatusBadRequest)
		return
	}
	// Accept raw JSON tx body or quoted JSON.
	var payload json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		payload = json.RawMessage(raw)
	}
	hash, err := e.SubmitTx(payload)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      -1,
		"result": map[string]any{
			"hash": hash,
			"code": 0,
			"log":  "tx queued",
		},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
