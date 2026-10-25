package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vaultchain/vaultchain/kv"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/bank"
	"github.com/vaultchain/vaultchain/x/ibc"
	"github.com/vaultchain/vaultchain/x/storage"
)

// RESTHandler is the node's REST API (also used directly by tests).
func (e *Engine) RESTHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cosmos/base/tendermint/v1beta1/node_info", e.restNodeInfo)
	mux.HandleFunc("/cosmos/bank/v1beta1/balances/", e.restBalances)
	mux.HandleFunc("/vaultchain/storage/params", e.restStorageParams)
	mux.HandleFunc("/vaultchain/storage/providers", e.restProviders)
	mux.HandleFunc("/vaultchain/storage/providers/", e.restProvider)
	mux.HandleFunc("/vaultchain/storage/deals", e.restDeals)
	mux.HandleFunc("/vaultchain/storage/deals/", e.restDeal)
	mux.HandleFunc("/vaultchain/storage/manifests", e.restManifests)
	mux.HandleFunc("/vaultchain/storage/manifests/", e.restManifest)
	mux.HandleFunc("/vaultchain/storage/challenge/", e.restChallenge)
	mux.HandleFunc("/vaultchain/storage/plans", e.restPlans)
	mux.HandleFunc("/vaultchain/storage/proofs", e.restProofs)
	mux.HandleFunc("/vaultchain/access/grants", e.restGrants)
	mux.HandleFunc(ibc.PolicyPath, e.restIBCPolicy)
	mux.HandleFunc("/vaultchain/auth/accounts/", e.restAccount)
	mux.HandleFunc("/vaultchain/txs", e.restSubmitTx)
	mux.HandleFunc("/vaultchain/txs/", e.restTx)

	return cors(mux)
}

func (e *Engine) serveREST(ctx context.Context) error {
	srv := &http.Server{Addr: e.cfg.RESTAddr, Handler: e.RESTHandler(), ReadHeaderTimeout: 5 * time.Second}
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

var errNotFound = errors.New("not found")

// httpError lets a query pick its status code.
type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

// query runs fn on a consistent read snapshot and writes its result as JSON.
func (e *Engine) query(w http.ResponseWriter, fn func(s kv.Store) (any, error)) {
	var out any
	err := e.app.View(func(s kv.Store) error {
		var err error
		out, err = fn(s)
		return err
	})
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.As(err, new(httpError)):
		var he httpError
		errors.As(err, &he)
		http.Error(w, he.msg, he.code)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		writeJSON(w, out)
	}
}

func (e *Engine) restNodeInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"default_node_info": map[string]any{
			"network": e.app.ChainID(),
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
	addr := strings.Trim(strings.TrimPrefix(r.URL.Path, "/cosmos/bank/v1beta1/balances/"), "/")
	e.query(w, func(s kv.Store) (any, error) {
		return map[string]any{"balances": bank.NewKeeper(s).GetBalance(addr)}, nil
	})
}

func (e *Engine) restStorageParams(w http.ResponseWriter, r *http.Request) {
	e.query(w, func(s kv.Store) (any, error) {
		return map[string]any{"params": storage.NewKeeper(s).Params()}, nil
	})
}

func (e *Engine) restProviders(w http.ResponseWriter, r *http.Request) {
	e.query(w, func(s kv.Store) (any, error) {
		return map[string]any{"providers": storage.NewKeeper(s).Providers()}, nil
	})
}

func (e *Engine) restProvider(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/providers/")
	e.query(w, func(s kv.Store) (any, error) {
		p, ok := storage.NewKeeper(s).Provider(addr)
		if !ok {
			return nil, errNotFound
		}
		return map[string]any{"provider": p}, nil
	})
}

// restDeals lists deals, optionally filtered by ?cid= (manifest root), ?provider= and ?owner=.
func (e *Engine) restDeals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.query(w, func(s kv.Store) (any, error) {
		out := []storage.Deal{}
		for _, d := range storage.NewKeeper(s).Deals() {
			if v := q.Get("cid"); v != "" && d.CID != v {
				continue
			}
			if v := q.Get("provider"); v != "" && !d.HasProvider(v) {
				continue
			}
			if v := q.Get("owner"); v != "" && d.Owner != v {
				continue
			}
			out = append(out, d)
		}
		return map[string]any{"deals": out}, nil
	})
}

func (e *Engine) restManifests(w http.ResponseWriter, r *http.Request) {
	e.query(w, func(s kv.Store) (any, error) {
		return map[string]any{"manifests": storage.NewKeeper(s).Manifests()}, nil
	})
}

func (e *Engine) restManifest(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/manifests/")
	e.query(w, func(s kv.Store) (any, error) {
		m, ok := storage.NewKeeper(s).Manifest(root)
		if !ok {
			return nil, errNotFound
		}
		return map[string]any{"manifest": m}, nil
	})
}

func (e *Engine) restDeal(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/deals/")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	e.query(w, func(s kv.Store) (any, error) {
		d, ok := storage.NewKeeper(s).Deal(id)
		if !ok {
			return nil, errNotFound
		}
		return map[string]any{"deal": d}, nil
	})
}

func (e *Engine) restPlans(w http.ResponseWriter, r *http.Request) {
	e.query(w, func(s kv.Store) (any, error) {
		return map[string]any{"plans": storage.NewKeeper(s).Plans()}, nil
	})
}

func (e *Engine) restProofs(w http.ResponseWriter, r *http.Request) {
	e.query(w, func(s kv.Store) (any, error) {
		return map[string]any{"proofs": storage.NewKeeper(s).Proofs()}, nil
	})
}

func (e *Engine) restGrants(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	e.query(w, func(s kv.Store) (any, error) {
		k := access.NewKeeper(s)
		if cid != "" {
			return map[string]any{"grants": k.ListByCID(cid)}, nil
		}
		return map[string]any{"grants": k.Grants()}, nil
	})
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
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

// restTx serves GET /vaultchain/txs/{hash}: the inclusion result of a tx while
// its record is within the retention window.
func (e *Engine) restTx(w http.ResponseWriter, r *http.Request) {
	hash := strings.Trim(strings.TrimPrefix(r.URL.Path, "/vaultchain/txs/"), "/")
	rec, ok := e.app.TxRecord(hash)
	if !ok {
		http.Error(w, "not found (unknown, still pending, or pruned)", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"tx": rec})
}

// restChallenge returns the current proof challenge for one provider of a deal
// (/challenge/{deal}?provider=addr): 404 if the
// deal does not exist, 409 while it is not (yet) challengeable.
func (e *Engine) restChallenge(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/challenge/"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		http.Error(w, "provider query parameter required (each replica has its own challenge)", http.StatusBadRequest)
		return
	}
	e.query(w, func(s kv.Store) (any, error) {
		ch, err := storage.NewKeeper(s).Challenge(id, provider)
		switch {
		case errors.Is(err, storage.ErrDealNotFound):
			return nil, errNotFound
		case err != nil:
			return nil, httpError{http.StatusConflict, err.Error()}
		}
		return map[string]any{"challenge": ch}, nil
	})
}
