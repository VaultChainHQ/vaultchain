package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/ibc"
)

func (e *Engine) serveREST(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/cosmos/base/tendermint/v1beta1/node_info", e.restNodeInfo)
	mux.HandleFunc("/cosmos/bank/v1beta1/balances/", e.restBalances)
	mux.HandleFunc("/vaultchain/storage/params", e.restStorageParams)
	mux.HandleFunc("/vaultchain/storage/providers", e.restProviders)
	mux.HandleFunc("/vaultchain/storage/providers/", e.restProvider)
	mux.HandleFunc("/vaultchain/storage/deals", e.restDeals)
	mux.HandleFunc("/vaultchain/storage/deals/", e.restDeal)
	mux.HandleFunc("/vaultchain/storage/plans", e.restPlans)
	mux.HandleFunc("/vaultchain/storage/proofs", e.restProofs)
	mux.HandleFunc("/vaultchain/access/grants", e.restGrants)
	mux.HandleFunc(ibc.PolicyPath, e.restIBCPolicy)
	mux.HandleFunc("/vaultchain/auth/accounts/", e.restAccount)
	mux.HandleFunc("/vaultchain/txs", e.restSubmitTx)

	srv := &http.Server{Addr: e.cfg.RESTAddr, Handler: cors(mux), ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", e.cfg.RESTAddr)
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

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (e *Engine) restNodeInfo(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	writeJSON(w, map[string]any{
		"default_node_info": map[string]any{
			"network": e.store.State.ChainID,
			"moniker": "vaultchain-local",
			"version": "0.1.0",
		},
		"application_version": map[string]any{
			"name":     "vaultchain",
			"app_name": "vaultd",
			"version":  "0.1.0",
		},
	})
}

func (e *Engine) restBalances(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimPrefix(r.URL.Path, "/cosmos/bank/v1beta1/balances/")
	addr = strings.Trim(addr, "/")
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	writeJSON(w, map[string]any{"balances": e.store.State.Bank.GetBalance(addr)})
}

func (e *Engine) restStorageParams(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	writeJSON(w, map[string]any{"params": e.store.State.Storage.Params})
}

func (e *Engine) restProviders(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	list := make([]any, 0, len(e.store.State.Storage.Providers))
	for _, p := range e.store.State.Storage.Providers {
		list = append(list, p)
	}
	writeJSON(w, map[string]any{"providers": list})
}

func (e *Engine) restProvider(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/providers/")
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	p, ok := e.store.State.Storage.Providers[addr]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"provider": p})
}

func (e *Engine) restDeals(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	list := make([]any, 0, len(e.store.State.Storage.Deals))
	for _, d := range e.store.State.Storage.Deals {
		list = append(list, d)
	}
	writeJSON(w, map[string]any{"deals": list})
}

func (e *Engine) restDeal(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/deals/")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	d, ok := e.store.State.Storage.Deals[id]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"deal": d})
}

func (e *Engine) restPlans(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	list := make([]any, 0, len(e.store.State.Storage.Plans))
	for _, p := range e.store.State.Storage.Plans {
		list = append(list, p)
	}
	writeJSON(w, map[string]any{"plans": list})
}

func (e *Engine) restProofs(w http.ResponseWriter, r *http.Request) {
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	list := make([]any, 0, len(e.store.State.Storage.Proofs))
	for _, p := range e.store.State.Storage.Proofs {
		list = append(list, p)
	}
	writeJSON(w, map[string]any{"proofs": list})
}

func (e *Engine) restGrants(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	e.store.mu.RLock()
	defer e.store.mu.RUnlock()
	if cid != "" {
		writeJSON(w, map[string]any{"grants": e.store.State.Access.ListByCID(cid)})
		return
	}
	list := make([]any, 0, len(e.store.State.Access.Grants))
	for _, g := range e.store.State.Access.Grants {
		list = append(list, g)
	}
	writeJSON(w, map[string]any{"grants": list})
}

func (e *Engine) restIBCPolicy(w http.ResponseWriter, r *http.Request) {
	b, err := ibc.PolicyJSON()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (e *Engine) restSubmitTx(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hash, err := e.SubmitTx(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"hash": hash, "status": "queued"})
}

func (e *Engine) restAccount(w http.ResponseWriter, r *http.Request) {
	addr := strings.Trim(strings.TrimPrefix(r.URL.Path, "/vaultchain/auth/accounts/"), "/")
	if err := vtypes.ValidateAddress(addr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	nonce, pending := e.AccountNonces(addr)
	writeJSON(w, map[string]any{"address": addr, "nonce": nonce, "pending_nonce": pending})
}
