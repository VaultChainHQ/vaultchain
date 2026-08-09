package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed web/*
var webFS embed.FS

func main() {
	listen := flag.String("listen", ":3456", "HTTP listen address")
	home := flag.String("home", "", "vaultd home (default ~/.vaultchain)")
	chainID := flag.String("chain-id", "vault-local-1", "chain id")
	rpc := flag.String("rpc", "http://127.0.0.1:26657", "RPC")
	rest := flag.String("rest", "http://127.0.0.1:1317", "REST API")
	provider := flag.String("provider", "http://127.0.0.1:3333", "vault-provider URL")
	vaultd := flag.String("vaultd", "vaultd", "vaultd binary")
	key := flag.String("key", "alice", "keyring key for txs")
	providerKey := flag.String("provider-key", "provider", "provider key name")
	flag.Parse()

	if *home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		*home = filepath.Join(h, ".vaultchain")
	}
	if path, err := exec.LookPath(*vaultd); err == nil {
		*vaultd = path
	}

	lab := &Lab{
		Home:        *home,
		ChainID:     *chainID,
		RPC:         *rpc,
		REST:        strings.TrimRight(*rest, "/"),
		Provider:    strings.TrimRight(*provider, "/"),
		Vaultd:      *vaultd,
		Key:         *key,
		ProviderKey: *providerKey,
		Client:      &http.Client{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", lab.handleHealth)
	mux.HandleFunc("/api/status", lab.handleStatus)
	mux.HandleFunc("/api/balance", lab.handleBalance)
	mux.HandleFunc("/api/storage/params", lab.handleStorageParams)
	mux.HandleFunc("/api/providers", lab.handleProviders)
	mux.HandleFunc("/api/storage/plans", lab.handlePlans)
	mux.HandleFunc("/api/storage/deals", lab.handleDeals)
	mux.HandleFunc("/api/storage/proofs", lab.handleProofs)
	mux.HandleFunc("/api/storage/register-provider", lab.handleRegisterProvider)
	mux.HandleFunc("/api/storage/buy-capacity", lab.handleBuyCapacity)
	mux.HandleFunc("/api/storage/open-deal", lab.handleOpenDeal)
	mux.HandleFunc("/api/storage/submit-proof", lab.handleSubmitProof)
	mux.HandleFunc("/api/storage/upload", lab.handleUpload)
	mux.HandleFunc("/api/access/grants", lab.handleGrants)
	mux.HandleFunc("/api/access/grant", lab.handleGrantAccess)
	mux.HandleFunc("/api/ibc/policy", lab.handleIBCPolicy)
	mux.HandleFunc("/api/ibc/mint-voucher", lab.handleMintVoucher)
	mux.HandleFunc("/api/provider/status", lab.handleProviderStatus)
	mux.HandleFunc("/api/provider/files", lab.handleProviderFiles)
	mux.HandleFunc("/api/provider/download/", lab.handleProviderDownload)

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	ip := wslHint()
	log.Printf("vault-lab listening on http://127.0.0.1%s", *listen)
	if ip != "" {
		log.Printf("WSL tip: open http://%s%s from Windows", ip, *listen)
	}
	log.Printf("  home=%s chain=%s key=%s provider=%s", *home, *chainID, *key, *provider)
	log.Fatal(http.Serve(ln, withCORS(mux)))
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

func wslHint() string {
	out, err := exec.Command("hostname", "-I").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
