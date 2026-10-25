// vault-ui is a small local web UI for vaultchain storage: upload a file
// (optionally encrypted client-side), list files and deals, download and
// decrypt, and grant or revoke access.
//
// It signs transactions with a key from the local keyring, so it only ever
// listens on a loopback address and rejects requests whose Host or Origin is
// not that address. Do not expose it, and do not deploy it. See docs/UI.md.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/pkg/client"
	"github.com/vaultchain/vaultchain/pkg/readauth"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/storage"
)

type ui struct {
	c            *client.Client
	chainID      string
	provider     string // provider base URL
	providerAddr string
	rec          vcrypto.KeyRecord
	signer       client.Signer
	fee          sdk.Coins
	duration     int64
	chunkSize    int
	maxUpload    int64
	listenHost   string // host:port the browser must use

	txMu sync.Mutex // one tx at a time: nonces are sequential
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "address to listen on (loopback only)")
	node := flag.String("node", "http://127.0.0.1:1317", "node REST endpoint, e.g. https://vaultl1-devnet.fly.dev")
	provider := flag.String("provider", "http://127.0.0.1:3333", "storage provider URL, e.g. https://vaultl1-provider.fly.dev")
	chainID := flag.String("chain-id", "", "chain id to sign for (default: asked from the node)")
	home := flag.String("home", "", "keyring home (default ~/.vaultchain)")
	key := flag.String("key", "alice", "keyring key that signs and owns uploads")
	feeStr := flag.String("fee", "1000uvault", "fee per transaction")
	duration := flag.Int64("deal-duration", 10000, "storage deal duration in blocks")
	chunkSize := flag.Int("chunk-size", chunk.DefaultChunkSize, "chunk size in bytes")
	maxMB := flag.Int64("max-upload-mb", 512, "largest accepted upload")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("--listen: %v", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Fatalf("refusing to listen on %q: vault-ui signs with a local key and only runs on a loopback address", host)
	}
	if *home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		*home = filepath.Join(h, ".vaultchain")
	}
	kr, err := vcrypto.NewKeyring(*home)
	if err != nil {
		log.Fatal(err)
	}
	signer, rec, err := client.KeyringSigner(kr, *key)
	if err != nil {
		log.Fatalf("key %q in %s: %v", *key, *home, err)
	}
	fee, err := sdk.ParseCoinsNormalized(*feeStr)
	if err != nil {
		log.Fatalf("--fee: %v", err)
	}
	u := &ui{
		c: client.New(*node), chainID: *chainID, provider: strings.TrimRight(*provider, "/"),
		rec: rec, signer: signer, fee: fee.Sort(), duration: *duration, chunkSize: *chunkSize,
		maxUpload: *maxMB << 20, listenHost: *listen,
	}
	if tok := os.Getenv("VAULT_ADMIN_TOKEN"); tok != "" {
		u.c.AdminToken = tok
	}
	if u.chainID == "" {
		if u.chainID, err = u.c.ChainID(); err != nil {
			log.Fatalf("cannot ask %s for its chain id: %v", *node, err)
		}
	}
	var st struct {
		Address string `json:"address"`
	}
	if err := getJSON(u.provider+"/status", &st); err != nil || st.Address == "" {
		log.Fatalf("cannot read provider status at %s: %v", u.provider, err)
	}
	u.providerAddr = st.Address

	log.Printf("vault-ui on http://%s  account %s (%s)  chain %s  provider %s", *listen, rec.Address, rec.Name, u.chainID, u.providerAddr)
	srv := &http.Server{Addr: *listen, Handler: u.handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func getJSON(u string, out any) error {
	c := &http.Client{Timeout: 15 * time.Second}
	r, err := c.Get(u)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", r.StatusCode)
	}
	return json.NewDecoder(r.Body).Decode(out)
}

type apiError struct {
	code int
	msg  string
}

func (e apiError) Error() string { return e.msg }

func fail(code int, format string, a ...any) apiError {
	return apiError{code, fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// api wraps a JSON handler: guard + error rendering.
func (u *ui) api(h func(*http.Request) (any, error)) http.HandlerFunc {
	return u.guard(func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		if err != nil {
			var ae apiError
			if errors.As(err, &ae) {
				writeJSON(w, ae.code, map[string]string{"error": ae.msg})
			} else {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			}
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}

// guard defends the local signer: the Host must be the address we listen on
// (blocks DNS rebinding), and API calls must carry a custom header, which a
// cross-origin page cannot send without a CORS preflight that we never grant.
func (u *ui) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.hostOK(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("X-Vault-UI") != "1" {
			http.Error(w, "missing X-Vault-UI header", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h(w, r)
	}
}

func (u *ui) hostOK(host string) bool {
	_, port, _ := net.SplitHostPort(u.listenHost)
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != port {
		return false
	}
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

func (u *ui) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/info", u.api(u.handleInfo))
	mux.Handle("/api/files", u.api(u.handleFiles))
	mux.Handle("/api/upload", u.api(u.handleUpload))
	mux.Handle("/api/grant", u.api(u.handleGrant))
	mux.Handle("/api/revoke", u.api(u.handleRevoke))
	mux.Handle("/api/download", u.guard(u.handleDownload))
	mux.Handle("/", u.guard(u.handleStatic))
	return mux
}

func (u *ui) handleInfo(r *http.Request) (any, error) {
	bal, err := u.c.Balance(u.rec.Address, vtypes.NativeDenom)
	if err != nil {
		return nil, err
	}
	h, _ := u.c.Height()
	return map[string]any{
		"address": u.rec.Address, "pubkey": u.rec.PubKeyHex, "key_name": u.rec.Name,
		"chain_id": u.chainID, "node": u.c.REST, "provider": u.provider, "provider_address": u.providerAddr,
		"balance": bal, "denom": vtypes.NativeDenom, "height": h, "max_upload": u.maxUpload,
	}, nil
}

type fileView struct {
	Root      string               `json:"root"`
	Name      string               `json:"name"`
	Mime      string               `json:"mime"`
	Size      uint64               `json:"size"`
	Owner     string               `json:"owner"`
	Mine      bool                 `json:"mine"`
	Encrypted bool                 `json:"encrypted"`
	Deals     []dealView           `json:"deals"`
	Grants    []access.AccessGrant `json:"grants"`
}

type dealView struct {
	ID       uint64 `json:"id"`
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Replica  string `json:"replica_status"`
	Proven   int64  `json:"last_proven_epoch"`
	Expires  int64  `json:"expire_height"`
}

// handleFiles lists the files the account owns and the ones shared with it.
func (u *ui) handleFiles(r *http.Request) (any, error) {
	mine, err := u.c.ManifestsOf(u.rec.Address)
	if err != nil {
		return nil, err
	}
	all, err := u.c.Grants("")
	if err != nil {
		return nil, err
	}
	byCID := map[string][]access.AccessGrant{}
	shared := map[string]bool{}
	for _, g := range all {
		byCID[g.CID] = append(byCID[g.CID], g)
		if g.Grantee == u.rec.Address {
			shared[g.CID] = true
		}
	}
	recs := mine
	have := map[string]bool{}
	for _, m := range mine {
		have[m.Root] = true
	}
	for cid := range shared {
		if have[cid] {
			continue
		}
		if m, err := u.c.Manifest(cid); err == nil {
			recs = append(recs, m)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Height > recs[j].Height })
	out := make([]fileView, 0, len(recs))
	for _, m := range recs {
		fv := fileView{Root: m.Root, Name: m.Name, Mime: m.Mime, Size: m.Size, Owner: m.Owner,
			Mine: m.Owner == u.rec.Address, Encrypted: m.Encrypted(), Deals: []dealView{}, Grants: []access.AccessGrant{}}
		if fv.Mine {
			fv.Grants = append(fv.Grants, byCID[m.Root]...)
			for i := range fv.Grants {
				fv.Grants[i].KeyEnvelope = "" // not needed by the page
			}
			deals, err := u.c.Deals(m.Root)
			if err != nil {
				return nil, err
			}
			for _, d := range deals {
				dv := dealView{ID: d.ID, Status: d.Status, Expires: d.ExpireH}
				if len(d.Replicas) > 0 {
					rp := d.Replicas[0]
					dv.Provider, dv.Replica, dv.Proven = rp.Provider, rp.Status, rp.LastProvenEpoch
				}
				fv.Deals = append(fv.Deals, dv)
			}
		}
		out = append(out, fv)
	}
	return map[string]any{"files": out}, nil
}

// send signs and submits one tx and waits for its result.
func (u *ui) send(msg any) error {
	u.txMu.Lock()
	defer u.txMu.Unlock()
	h, err := u.c.Send(u.chainID, u.signer, u.fee, msg)
	if err != nil {
		return err
	}
	return u.c.WaitTx(h, 40*time.Second)
}

// handleUpload takes a multipart form whose "file" part comes last; the optional
// "encrypt" and "name" fields must precede it.
func (u *ui) handleUpload(r *http.Request) (any, error) {
	if r.Method != http.MethodPost {
		return nil, fail(http.StatusMethodNotAllowed, "POST required")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, u.maxUpload+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fail(http.StatusBadRequest, "multipart form required")
	}
	var encrypt bool
	var name string
	var part *multipart.Part
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil, fail(http.StatusBadRequest, "no file part")
		}
		if p.FormName() == "file" {
			part = p
			break
		}
		b, _ := io.ReadAll(io.LimitReader(p, 4096))
		switch p.FormName() {
		case "encrypt":
			encrypt = string(b) == "1" || string(b) == "true"
		case "name":
			name = strings.TrimSpace(string(b))
		}
	}
	if name == "" {
		name = part.FileName()
	}
	name = safeName(name)
	if name == "" {
		name = "file"
	}
	mt := part.Header.Get("Content-Type")
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	if mt == "" {
		mt = "application/octet-stream"
	}

	dir, err := os.MkdirTemp("", "vault-ui-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	plain := filepath.Join(dir, "plain")
	pf, err := os.Create(plain)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(pf, io.LimitReader(part, u.maxUpload+1))
	if err != nil {
		pf.Close()
		return nil, fail(http.StatusBadRequest, "reading upload: %v", err)
	}
	if n > u.maxUpload {
		pf.Close()
		return nil, fail(http.StatusRequestEntityTooLarge, "file exceeds %d MB", u.maxUpload>>20)
	}
	if _, err := pf.Seek(0, io.SeekStart); err != nil {
		pf.Close()
		return nil, err
	}

	var msg storage.MsgCommitManifest
	body := plain // the bytes the provider will store
	if encrypt {
		body = filepath.Join(dir, "cipher")
		msg, err = client.EncryptToFile(pf, body, u.rec, mt, name, u.chunkSize)
	} else {
		var m chunk.Manifest
		m, _, err = chunk.Build(pf, u.chunkSize, mt, name)
		msg = storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size,
			ChunkSize: m.ChunkSize, Mime: m.Mime, Name: m.Name}
	}
	pf.Close()
	if err != nil {
		return nil, err
	}

	steps := []string{}
	if m, err := u.c.Manifest(msg.Root); err == nil {
		if m.Owner != u.rec.Address {
			return nil, fail(http.StatusConflict, "this exact content was already committed by %s", m.Owner)
		}
		steps = append(steps, "manifest already on chain")
	} else if client.IsNotFound(err) {
		if err := u.send(msg); err != nil {
			return nil, fail(http.StatusBadGateway, "commit manifest: %v", err)
		}
		steps = append(steps, "manifest committed")
	} else {
		return nil, err
	}

	deals, err := u.c.Deals(msg.Root)
	if err != nil {
		return nil, err
	}
	haveDeal := false
	for _, d := range deals {
		if (d.Status == "open" || d.Status == "pending") && len(d.Replicas) > 0 && d.Replicas[0].Provider == u.providerAddr {
			haveDeal = true
		}
	}
	if !haveDeal {
		deal := storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket, CID: msg.Root,
			Provider: u.providerAddr, Duration: u.duration, Denom: vtypes.NativeDenom}
		if err := u.send(deal); err != nil {
			return nil, fail(http.StatusBadGateway, "open deal: %v", err)
		}
		steps = append(steps, "deal opened")
	} else {
		steps = append(steps, "deal already open")
	}

	f, err := os.Open(body)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	req, _ := http.NewRequest(http.MethodPost, u.provider+"/v1/objects/"+msg.Root, f)
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return nil, fail(http.StatusBadGateway, "upload to provider: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return nil, fail(http.StatusBadGateway, "provider refused the upload: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	steps = append(steps, "uploaded to provider")
	return map[string]any{"root": msg.Root, "encrypted": encrypt, "size": msg.Size, "steps": steps}, nil
}

func safeName(s string) string {
	s = filepath.Base(strings.ReplaceAll(s, "\\", "/"))
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '"' || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "." || out == ".." {
		return ""
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func decodeJSON(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return fail(http.StatusMethodNotAllowed, "POST required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fail(http.StatusBadRequest, "bad request: %v", err)
	}
	return nil
}

func (u *ui) handleGrant(r *http.Request) (any, error) {
	var req struct {
		Root       string `json:"root"`
		Grantee    string `json:"grantee"`
		Capability string `json:"capability"`
		ExpiryH    int64  `json:"expiry_height"`
		PubKey     string `json:"grantee_pubkey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return nil, err
	}
	if req.Capability == "" {
		req.Capability = access.CapabilityRead
	}
	m, err := u.c.Manifest(req.Root)
	if err != nil {
		return nil, fail(http.StatusNotFound, "unknown object: %v", err)
	}
	if m.Owner != u.rec.Address {
		return nil, fail(http.StatusForbidden, "only the owner can grant access")
	}
	msg := access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: req.Root, Grantee: strings.TrimSpace(req.Grantee),
		Capability: req.Capability, ExpiryH: req.ExpiryH}
	if m.Encrypted() {
		if msg.Grantee == access.Public {
			return nil, fail(http.StatusBadRequest, "an encrypted file cannot be made public")
		}
		if msg.KeyEnvelope, err = u.c.GrantEnvelope(req.Root, u.rec, msg.Grantee, strings.TrimSpace(req.PubKey)); err != nil {
			return nil, fail(http.StatusBadRequest, "%v", err)
		}
	}
	if err := u.send(msg); err != nil {
		return nil, fail(http.StatusBadGateway, "grant: %v", err)
	}
	return map[string]any{"ok": true, "key_shared": m.Encrypted()}, nil
}

func (u *ui) handleRevoke(r *http.Request) (any, error) {
	var req struct {
		Root    string `json:"root"`
		Grantee string `json:"grantee"`
		GrantID uint64 `json:"grant_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return nil, err
	}
	if err := u.send(access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: req.Root, Grantee: req.Grantee, GrantID: req.GrantID}); err != nil {
		return nil, fail(http.StatusBadGateway, "revoke: %v", err)
	}
	return map[string]any{"ok": true}, nil
}

// handleDownload fetches an object from the provider with a read token signed
// by the local key and, for an encrypted file with decrypt=1, decrypts it.
func (u *ui) handleDownload(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if len(root) != 64 {
		http.Error(w, "bad root", http.StatusBadRequest)
		return
	}
	if r.Header.Get("X-Vault-UI") != "1" {
		http.Error(w, "missing X-Vault-UI header", http.StatusForbidden)
		return
	}
	m, err := u.c.Manifest(root)
	if err != nil {
		http.Error(w, "unknown object: "+err.Error(), http.StatusNotFound)
		return
	}
	tok, err := readauth.Sign(u.providerAddr, root, time.Now().Add(2*time.Minute), u.rec.PubKeyHex, u.signer.Sign)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req, _ := http.NewRequest(http.MethodGet, u.provider+"/v1/objects/"+url.PathEscape(root), nil)
	readauth.Attach(req, tok)
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		http.Error(w, "provider: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		http.Error(w, fmt.Sprintf("provider: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b))), resp.StatusCode)
		return
	}
	name := safeName(m.Name)
	if name == "" {
		name = "download"
	}
	decrypt := m.Encrypted() && r.URL.Query().Get("decrypt") == "1"
	if m.Encrypted() && !decrypt {
		name += ".vle"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/octet-stream")

	if !decrypt {
		_, _ = io.Copy(w, resp.Body)
		return
	}
	// Decrypt into a temp file first so a tampered download is never half-served.
	tmp, err := os.CreateTemp("", "vault-ui-dec-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := u.c.DecryptStream(tmp, resp.Body, root, u.rec); err != nil {
		w.Header().Del("Content-Disposition")
		http.Error(w, "decrypt: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	st, _ := tmp.Stat()
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	_, _ = io.Copy(w, tmp)
}
