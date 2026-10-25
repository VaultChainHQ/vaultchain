package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VaultChainHQ/vaultchain/pkg/chunk"
	"github.com/VaultChainHQ/vaultchain/pkg/readauth"
)

type Lab struct {
	Home        string
	ChainID     string
	RPC         string
	REST        string
	Provider    string
	Vaultd      string
	Key         string
	ProviderKey string
	Client      *http.Client
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (l *Lab) getJSON(url string) (json.RawMessage, error) {
	res, err := l.Client.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.RawMessage(body), nil
}

func (l *Lab) keyAddr(name string) (string, error) {
	out, err := l.execVaultd("keys", "show", name)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	addr := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(addr, "vault") {
		return "", fmt.Errorf("unexpected address output: %q", out)
	}
	return addr, nil
}

func (l *Lab) execVaultd(args ...string) (string, error) {
	base := []string{"--home", l.Home}
	cmd := exec.Command(l.Vaultd, append(base, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

func (l *Lab) runTx(from string, args ...string) (string, error) {
	txArgs := append([]string{"tx"}, args...)
	txArgs = append(txArgs, "--from", from, "--node", l.REST)
	return l.execVaultd(txArgs...)
}

func (l *Lab) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"chain_id": l.ChainID,
		"home":     l.Home,
		"provider": l.Provider,
		"key":      l.Key,
	})
}

func (l *Lab) handleStatus(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.RPC + "/status")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleBalance(w http.ResponseWriter, _ *http.Request) {
	addr, err := l.keyAddr(l.Key)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	data, err := l.getJSON(fmt.Sprintf("%s/cosmos/bank/v1beta1/balances/%s", l.REST, addr))
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "address": addr, "data": data})
}

func (l *Lab) handleStorageParams(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/params")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleProviders(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/providers")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handlePlans(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/plans")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleDeals(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/deals")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleProofs(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/proofs")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleGrants(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/access/grants")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleIBCPolicy(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/ibc/policy")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleRegisterProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		endpoint = l.Provider
	}
	cap := r.URL.Query().Get("capacity")
	if cap == "" {
		cap = strconv.FormatUint(10<<30, 10) // 10 GiB
	}
	collateral := r.URL.Query().Get("collateral")
	if collateral == "" {
		collateral = "1000000000uvault"
	}
	// Ensure provider key exists and is funded from alice if needed.
	if _, err := l.keyAddr(l.ProviderKey); err != nil {
		if _, err := l.execVaultd("keys", "add", l.ProviderKey); err != nil {
			writeErr(w, 500, err)
			return
		}
		provAddr, err := l.keyAddr(l.ProviderKey)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		if _, err := l.runTx(l.Key, "bank-send", provAddr, "5000000000000uvault"); err != nil {
			writeErr(w, 500, fmt.Errorf("fund provider: %w", err))
			return
		}
		time.Sleep(1500 * time.Millisecond)
	}
	out, err := l.runTx(l.ProviderKey, "storage", "register-provider", endpoint, cap, collateral)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out})
}

func (l *Lab) handleBuyCapacity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	bytesN := r.URL.Query().Get("bytes")
	if bytesN == "" {
		bytesN = strconv.FormatUint(1<<30, 10) // 1 GiB
	}
	dur := r.URL.Query().Get("duration")
	if dur == "" {
		dur = "10000"
	}
	denom := r.URL.Query().Get("denom")
	if denom == "" {
		denom = "uvault"
	}
	out, err := l.runTx(l.Key, "storage", "buy-capacity", bytesN, dur, denom)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out})
}

// handleOpenDeal opens a deal for an object whose manifest is already committed
// (use /api/storage/upload, or `vaultd tx storage commit-manifest`, first).
func (l *Lab) handleOpenDeal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	var body struct {
		Class    string `json:"class"`
		Root     string `json:"root"`
		Size     uint64 `json:"size"` // 0 = manifest size
		Provider string `json:"provider"`
		Duration int64  `json:"duration"`
		Denom    string `json:"denom"`
		// Replication > 1 asks the chain to assign more providers; the first is
		// the lab provider unless "provider" is "-" (chain picks all).
		Replication uint32 `json:"replication"`
		MinReplicas uint32 `json:"min_replicas"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !validRoot(body.Root) {
		writeErr(w, 400, fmt.Errorf("root (64-char hex manifest root) required: commit a manifest first"))
		return
	}
	if body.Class == "" {
		body.Class = "market"
	}
	if body.Duration == 0 {
		body.Duration = 10000
	}
	if body.Denom == "" {
		body.Denom = "uvault"
	}
	if body.Provider == "" {
		addr, err := l.keyAddr(l.ProviderKey)
		if err != nil {
			writeErr(w, 500, fmt.Errorf("provider key: %w", err))
			return
		}
		body.Provider = addr
	}
	args := []string{"storage", "open-deal",
		body.Class, body.Root, strconv.FormatUint(body.Size, 10), body.Provider,
		strconv.FormatInt(body.Duration, 10), body.Denom}
	if body.Replication > 0 {
		args = append(args, "--replication", strconv.FormatUint(uint64(body.Replication), 10))
	}
	if body.MinReplicas > 0 {
		args = append(args, "--min-replicas", strconv.FormatUint(uint64(body.MinReplicas), 10))
	}
	out, err := l.runTx(l.Key, args...)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out, "deal": body})
}

// handleSubmitProof answers the deal's current challenge by hand (the provider
// normally does this itself): the CLI fetches the challenge and the chunk.
func (l *Lab) handleSubmitProof(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	dealID := r.URL.Query().Get("deal_id")
	if dealID == "" {
		writeErr(w, 400, fmt.Errorf("deal_id required"))
		return
	}
	out, err := l.runTx(l.ProviderKey, "storage", "submit-proof", dealID, "--provider-url", l.Provider)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out})
}
func (l *Lab) handleGrantAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	cid := r.URL.Query().Get("cid")
	grantee := r.URL.Query().Get("grantee")
	cap := r.URL.Query().Get("capability")
	exp := r.URL.Query().Get("expiry")
	if cid == "" || grantee == "" {
		writeErr(w, 400, fmt.Errorf("cid and grantee required"))
		return
	}
	if cap == "" {
		cap = "read"
	}
	if exp == "" {
		exp = "999999"
	}
	out, err := l.runTx(l.Key, "access", "grant", cid, grantee, cap, exp)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out})
}

func (l *Lab) handleMintVoucher(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	addr, err := l.keyAddr(l.Key)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	denom := r.URL.Query().Get("denom")
	if denom == "" {
		denom = "ibc/ATOM"
	}
	amount := r.URL.Query().Get("amount")
	if amount == "" {
		amount = "1000000000"
	}
	out, err := l.runTx(l.Key, "ibc-lab", "mint-voucher", addr, denom, amount)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out})
}

func (l *Lab) handleProviderStatus(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.Provider + "/status")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

// handleProviderFiles lists the objects committed on chain (the provider itself
// no longer exposes a listing).
func (l *Lab) handleProviderFiles(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/manifests")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	var parsed struct {
		Manifests []struct {
			Root string `json:"root"`
			Name string `json:"name"`
			Mime string `json:"mime"`
			Size uint64 `json:"size"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "files": parsed.Manifests})
}

func validRoot(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// handleProviderDownload streams an object from the provider by manifest root,
// forwarding Range and the headers that describe the body.
func (l *Lab) handleProviderDownload(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimPrefix(r.URL.Path, "/api/provider/download/")
	if !validRoot(root) {
		writeErr(w, 400, fmt.Errorf("object id must be a 64-char hex content root"))
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, l.Provider+"/v1/objects/"+root, nil)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	for _, h := range []string{"Range", "If-Range", "If-None-Match"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// Providers only serve private objects to a signed reader: sign as the lab key.
	if tok, err := l.readToken(root); err == nil {
		readauth.Attach(req, tok)
	} else {
		log.Printf("read token: %v (requesting anonymously)", err)
	}
	res, err := l.Client.Do(req)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	defer res.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"Content-Disposition", "Content-Security-Policy", "X-Content-Type-Options", "Etag", "Cache-Control"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, res.Body)
	}
}

// sanitizeMime reduces an uploaded Content-Type to a valid bare media type.
func sanitizeMime(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	if chunk.ValidateMime(ct) != nil {
		return "application/octet-stream"
	}
	return ct
}

// txHash extracts the hash from `vaultd tx` output ({"hash":...,"status":"queued"}).
func txHash(out string) string {
	var r struct {
		Hash string `json:"hash"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &r)
	return r.Hash
}

// waitTx polls until the tx is included and returns an error if it failed.
func (l *Lab) waitTx(out string) error {
	hash := txHash(out)
	if hash == "" {
		return fmt.Errorf("no tx hash in %q", out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := l.getJSON(l.REST + "/vaultchain/txs/" + hash)
		if err == nil {
			var rec struct {
				Tx struct {
					OK     bool   `json:"ok"`
					Result string `json:"result"`
				} `json:"tx"`
			}
			if err := json.Unmarshal(raw, &rec); err != nil {
				return err
			}
			if !rec.Tx.OK {
				return fmt.Errorf("tx failed: %s", rec.Tx.Result)
			}
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("tx %s not included within 20s", hash)
}

func (l *Lab) manifestOwner(root string) (owner string, found bool, err error) {
	raw, err := l.getJSON(l.REST + "/vaultchain/storage/manifests/" + root)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", false, nil
		}
		return "", false, err
	}
	var r struct {
		Manifest struct {
			Owner string `json:"owner"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", false, err
	}
	return r.Manifest.Owner, true, nil
}

// openDealFor returns the newest pending or open deal for root between owner and provider.
func (l *Lab) openDealFor(root, owner, provider string) (uint64, bool, error) {
	q := url.Values{"cid": {root}, "owner": {owner}, "provider": {provider}}
	raw, err := l.getJSON(l.REST + "/vaultchain/storage/deals?" + q.Encode())
	if err != nil {
		return 0, false, err
	}
	var parsed struct {
		Deals []struct {
			ID     uint64 `json:"id"`
			Status string `json:"status"`
		} `json:"deals"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, false, err
	}
	var id uint64
	for _, d := range parsed.Deals {
		if (d.Status == "open" || d.Status == "pending") && d.ID > id {
			id = d.ID
		}
	}
	return id, id != 0, nil
}

// handleUpload runs the whole object flow: hash into a manifest, commit it,
// open a deal, stream the bytes to the provider, then leave proving to the provider (it answers chain challenges).
func (l *Lab) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, err)
		return
	}

	var (
		src  io.ReadSeeker
		name = "note.txt"
		mime = "text/plain"
	)
	if f, hdr, err := r.FormFile("file"); err == nil {
		defer f.Close()
		src = f
		name = filepath.Base(hdr.Filename)
		if chunk.ValidateName(name) != nil {
			name = "upload.bin"
		}
		mime = sanitizeMime(hdr.Header.Get("Content-Type"))
	} else {
		text := r.FormValue("text")
		if text == "" {
			writeErr(w, 400, fmt.Errorf("provide file or text"))
			return
		}
		// trivial "encryption" marker for personal cloud demo (client-side placeholder)
		src = bytes.NewReader([]byte("vaultenc:v1:" + text))
	}

	m, _, err := chunk.Build(src, chunk.DefaultChunkSize, mime, name)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("hash upload: %w", err))
		return
	}
	owner, err := l.keyAddr(l.Key)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	provider, err := l.keyAddr(l.ProviderKey)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("provider not registered/key missing: %w", err))
		return
	}
	class := r.FormValue("class")
	if class == "" {
		class = "market"
	}

	// 1. Commit the manifest (skipped if we already own it).
	mOwner, committed, err := l.manifestOwner(m.Root)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if committed && mOwner != owner {
		writeErr(w, 409, fmt.Errorf("root %s is already committed by %s", m.Root, mOwner))
		return
	}
	var commitOut, dealOut string
	if !committed {
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			writeErr(w, 500, err)
			return
		}
		tmp, err := os.CreateTemp("", "vault-lab-*")
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, src)
		tmp.Close()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		commitOut, err = l.runTx(l.Key, "storage", "commit-manifest", tmp.Name(),
			"--mime", m.Mime, "--name", m.Name, "--chunk-size", strconv.Itoa(int(m.ChunkSize)))
		if err != nil {
			writeErr(w, 500, fmt.Errorf("commit-manifest: %w", err))
			return
		}
	}

	// 2. Open a deal unless one is already open for this root/provider.
	dealID, haveDeal, err := l.openDealFor(m.Root, owner, provider)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if !haveDeal {
		dealOut, err = l.runTx(l.Key, "storage", "open-deal",
			class, m.Root, strconv.FormatUint(m.Size, 10), provider, "10000", "uvault")
		if err != nil {
			writeErr(w, 500, fmt.Errorf("open-deal: %w", err))
			return
		}
		if commitOut != "" {
			if err := l.waitTx(commitOut); err != nil {
				writeErr(w, 500, fmt.Errorf("commit-manifest: %w", err))
				return
			}
		}
		if err := l.waitTx(dealOut); err != nil {
			writeErr(w, 500, fmt.Errorf("open-deal: %w", err))
			return
		}
		if dealID, haveDeal, err = l.openDealFor(m.Root, owner, provider); err != nil || !haveDeal {
			writeErr(w, 500, fmt.Errorf("deal for %s not found after inclusion (%v)", m.Root, err))
			return
		}
	} else if commitOut != "" {
		if err := l.waitTx(commitOut); err != nil {
			writeErr(w, 500, fmt.Errorf("commit-manifest: %w", err))
			return
		}
	}

	// 3. Stream the bytes to the provider (never buffered by the lab).
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := l.uploadToProvider(m.Root, src); err != nil {
		writeErr(w, 502, err)
		return
	}

	writeJSON(w, 200, map[string]any{
		"ok":        true,
		"root":      m.Root,
		"cid":       m.Root,
		"name":      m.Name,
		"mime":      m.Mime,
		"deal_id":   dealID,
		"bytes":     m.Size,
		"chunks":    m.NumChunks(),
		"class":     class,
		"commit_tx": commitOut,
		"open_tx":   dealOut,
		"download":  "/api/provider/download/" + m.Root,
	})
}

func (l *Lab) uploadToProvider(root string, body io.Reader) error {
	req, err := http.NewRequest(http.MethodPost, l.Provider+"/v1/objects/"+root, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := l.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 300 {
		return fmt.Errorf("provider upload HTTP %d: %s", res.StatusCode, string(b))
	}
	return nil
}
