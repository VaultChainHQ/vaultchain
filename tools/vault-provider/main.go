// vault-provider is the native Vaultchain storage provider for local nets.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type server struct {
	addr      string
	dataDir   string
	rest      string
	chainID   string
	skipChain bool
	mu        sync.Mutex
}

func main() {
	addr := flag.String("address", "", "provider bech32 address")
	listen := flag.String("listen", ":3333", "HTTP listen address")
	dataDir := flag.String("data", "", "object store directory (default ~/.vault-provider/data)")
	rest := flag.String("rest", "http://127.0.0.1:1317", "vaultd REST API")
	chainID := flag.String("chain-id", "vault-local-1", "chain id")
	skipChain := flag.Bool("skip-chain-check", false, "accept uploads without deal verify (dev)")
	flag.Parse()

	if *dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		*dataDir = filepath.Join(home, ".vault-provider", "data")
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	s := &server{
		addr:      *addr,
		dataDir:   *dataDir,
		rest:      strings.TrimRight(*rest, "/"),
		chainID:   *chainID,
		skipChain: *skipChain,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleStatus)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/version", s.handleVersion)
	mux.HandleFunc("/v1/upload", s.handleUpload)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/v1/download/", s.handleDownload)
	mux.HandleFunc("/download/", s.handleDownload)
	mux.HandleFunc("/list", s.handleList)

	log.Printf("vault-provider listening on %s (data=%s rest=%s chain=%s)", *listen, *dataDir, *rest, *chainID)
	log.Fatal(http.ListenAndServe(*listen, withCORS(mux)))
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	entries, _ := os.ReadDir(s.dataDir)
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	writeJSON(w, 200, map[string]any{
		"status":   "online",
		"address":  s.addr,
		"chain_id": s.chainID,
		"files":    n,
	})
}

func (s *server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"version":  "vault-provider-0.1.0",
		"chain_id": s.chainID,
	})
}

func (s *server) handleList(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	cid := strings.TrimPrefix(path, "/v1/download/")
	if cid == path {
		cid = strings.TrimPrefix(path, "/download/")
	}
	cid = filepath.Base(cid)
	if cid == "" || cid == "." {
		writeErr(w, 400, fmt.Errorf("missing cid"))
		return
	}
	f, err := os.Open(filepath.Join(s.dataDir, cid))
	if err != nil {
		writeErr(w, 404, fmt.Errorf("object not found"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+cid+"\"")
	_, _ = io.Copy(w, f)
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, 400, fmt.Errorf("cannot parse form: %w", err))
		return
	}

	owner := r.FormValue("owner")
	cid := strings.ToLower(strings.TrimSpace(r.FormValue("cid")))
	dealIDStr := r.FormValue("deal_id")
	if owner == "" || cid == "" || dealIDStr == "" {
		writeErr(w, 400, fmt.Errorf("owner, cid, and deal_id are required"))
		return
	}
	dealID, err := strconv.ParseUint(dealIDStr, 10, 64)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("bad deal_id"))
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, fmt.Errorf("file field required: %w", err))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("cannot read file: %w", err))
		return
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != cid {
		writeErr(w, 400, fmt.Errorf("cid mismatch: body sha256=%s want=%s", got, cid))
		return
	}

	if !s.skipChain {
		deal, err := s.fetchDeal(dealID)
		if err != nil {
			writeErr(w, 502, fmt.Errorf("chain verify failed: %w", err))
			return
		}
		if deal.CID != cid {
			writeErr(w, 400, fmt.Errorf("deal cid mismatch"))
			return
		}
		if deal.Owner != owner {
			writeErr(w, 403, fmt.Errorf("deal owner mismatch"))
			return
		}
		if deal.Status != "open" {
			writeErr(w, 400, fmt.Errorf("deal not open"))
			return
		}
		if deal.Size > 0 && uint64(len(data)) != deal.Size {
			writeErr(w, 400, fmt.Errorf("size mismatch got=%d want=%d", len(data), deal.Size))
			return
		}
		if s.addr != "" && deal.Provider != s.addr {
			writeErr(w, 403, fmt.Errorf("deal assigned to different provider"))
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dataDir, cid)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeErr(w, 500, fmt.Errorf("write failed: %w", err))
		return
	}
	log.Printf("stored cid=%s deal=%d owner=%s bytes=%d", cid, dealID, owner, len(data))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"cid":     cid,
		"deal_id": dealID,
		"bytes":   len(data),
		"status":  "stored",
	})
}

type dealDTO struct {
	ID       uint64 `json:"id"`
	CID      string `json:"cid"`
	Owner    string `json:"owner"`
	Provider string `json:"provider"`
	Size     uint64 `json:"size"`
	Status   string `json:"status"`
}

func (s *server) fetchDeal(id uint64) (dealDTO, error) {
	cli := &http.Client{Timeout: 15 * time.Second}
	res, err := cli.Get(fmt.Sprintf("%s/vaultchain/storage/deals/%d", s.rest, id))
	if err != nil {
		return dealDTO{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return dealDTO{}, fmt.Errorf("status %d: %s", res.StatusCode, string(body))
	}
	var out struct {
		Deal dealDTO `json:"deal"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return dealDTO{}, err
	}
	return out.Deal, nil
}
