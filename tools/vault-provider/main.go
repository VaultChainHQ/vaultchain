// vault-provider is the native Vaultchain storage provider (v2).
//
// It stores content-addressed objects as verified chunks on disk and serves them
// back with HTTP Range support. Writes are authorised by an open on-chain deal
// that names this provider; objects are identified by their manifest root.
//
//	POST /v1/objects/{root}          stream a whole object (verified against the root)
//	GET  /v1/objects/{root}          download (Range, Content-Type from the manifest)
//	GET  /v1/objects/{root}/status   chunk coverage
//	PUT  /v1/chunks/{root}/{i}       store one chunk (X-Vault-Proof = Merkle path)
//	GET  /v1/chunks/{root}/{i}       fetch one chunk (+ X-Vault-Proof when complete)
//
// With -home it also answers the chain
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
)

func main() {
	addr := flag.String("address", "", "provider bech32 address (required: deals must name it)")
	listen := flag.String("listen", ":3333", "HTTP listen address")
	dataDir := flag.String("data", "", "object store directory (default ~/.vault-provider/data)")
	rest := flag.String("rest", "http://127.0.0.1:1317", "vaultd REST API")
	chainID := flag.String("chain-id", "vault-local-1", "chain id (informational)")
	origins := flag.String("allow-origin", "", "comma-separated browser origins allowed via CORS (default: none)")
	home := flag.String("home", "", "vaultd home with the provider key; enables the automatic proof loop")
	keyName := flag.String("key", "provider", "keyring name of the provider key (with -home)")
	feeStr := flag.String("fee", "1000uvault", "fee attached to each proof tx")
	proveEvery := flag.Duration("prove-interval", 3*time.Second, "how often to poll the chain for challenges")
	flag.Parse()

	if *addr == "" {
		log.Fatal("-address is required")
	}
	if *dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		*dataDir = filepath.Join(home, ".vault-provider", "data")
	}
	s, err := newServer(*addr, *rest, *chainID, *dataDir, strings.Split(*origins, ","))
	if err != nil {
		log.Fatal(err)
	}
	if *home != "" {
		kr, err := vcrypto.NewKeyring(*home)
		if err != nil {
			log.Fatal(err)
		}
		rec, err := kr.Get(*keyName)
		if err != nil {
			log.Fatal(err)
		}
		if rec.Address != *addr {
			log.Fatalf("key %q is %s but -address is %s", *keyName, rec.Address, *addr)
		}
		fee, err := sdk.ParseCoinsNormalized(*feeStr)
		if err != nil {
			log.Fatalf("-fee: %v", err)
		}
		if fee.IsZero() {
			fee = nil
		}
		name := *keyName
		pr := newProver(s, name, rec.PubKeyHex, func(b []byte) ([]byte, error) { return kr.Sign(name, b) }, fee)
		go pr.run(context.Background(), *proveEvery)
		log.Printf("proof loop enabled (key %s, every %s)", name, *proveEvery)
	} else {
		log.Printf("WARNING: no -home: not answering storage challenges; deals will fail")
	}
	srv := &http.Server{Addr: *listen, Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("vault-provider v2 listening on %s (data=%s rest=%s chain=%s cors=%d origins)",
		*listen, *dataDir, *rest, *chainID, len(s.origins))
	log.Fatal(srv.ListenAndServe())
}
