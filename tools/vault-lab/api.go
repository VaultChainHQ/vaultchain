package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
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

func (l *Lab) handleOpenDeal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	var body struct {
		Class    string `json:"class"`
		CID      string `json:"cid"`
		Size     uint64 `json:"size"`
		Provider string `json:"provider"`
		Duration int64  `json:"duration"`
		Denom    string `json:"denom"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Class == "" {
		body.Class = "market"
	}
	if body.CID == "" {
		body.CID = strings.Repeat("ab", 32)[:64]
	}
	if body.Size == 0 {
		body.Size = 32
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
	out, err := l.runTx(l.Key, "storage", "open-deal",
		body.Class, body.CID, strconv.FormatUint(body.Size, 10), body.Provider,
		strconv.FormatInt(body.Duration, 10), body.Denom,
	)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out, "deal": body})
}

func (l *Lab) handleSubmitProof(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	dealID := r.URL.Query().Get("deal_id")
	cid := r.URL.Query().Get("cid")
	att := r.URL.Query().Get("attestation")
	if dealID == "" || cid == "" {
		writeErr(w, 400, fmt.Errorf("deal_id and cid required"))
		return
	}
	if att == "" {
		att = "stub-proof-" + time.Now().UTC().Format(time.RFC3339)
	}
	out, err := l.runTx(l.ProviderKey, "storage", "submit-proof", dealID, cid, att)
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

func (l *Lab) handleProviderFiles(w http.ResponseWriter, _ *http.Request) {
	data, err := l.getJSON(l.Provider + "/list")
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data})
}

func (l *Lab) handleProviderDownload(w http.ResponseWriter, r *http.Request) {
	cid := strings.TrimPrefix(r.URL.Path, "/api/provider/download/")
	res, err := l.Client.Get(l.Provider + "/v1/download/" + cid)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (l *Lab) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("POST required"))
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, err)
		return
	}

	var data []byte
	if f, _, err := r.FormFile("file"); err == nil {
		defer f.Close()
		data, err = io.ReadAll(f)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
	} else {
		text := r.FormValue("text")
		if text == "" {
			writeErr(w, 400, fmt.Errorf("provide file or text"))
			return
		}
		// trivial "encryption" marker for personal cloud demo (client-side placeholder)
		data = []byte("vaultenc:v1:" + text)
	}

	sum := sha256.Sum256(data)
	cid := hex.EncodeToString(sum[:])
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
	out, err := l.runTx(l.Key, "storage", "open-deal",
		class, cid, strconv.Itoa(len(data)), provider, "10000", "uvault",
	)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("open-deal: %w", err))
		return
	}
	time.Sleep(1500 * time.Millisecond)

	dealID, err := l.findDealID(cid)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("find deal: %w (tx=%s)", err, out))
		return
	}

	if err := l.uploadToProvider(owner, cid, dealID, data); err != nil {
		writeErr(w, 502, err)
		return
	}

	proofOut, err := l.runTx(l.ProviderKey, "storage", "submit-proof",
		strconv.FormatUint(dealID, 10), cid, "lab-stub-"+cid[:8],
	)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("proof: %w", err))
		return
	}

	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"cid":     cid,
		"deal_id": dealID,
		"bytes":   len(data),
		"class":   class,
		"open_tx": out,
		"proof":   proofOut,
	})
}

func (l *Lab) findDealID(cid string) (uint64, error) {
	data, err := l.getJSON(l.REST + "/vaultchain/storage/deals")
	if err != nil {
		return 0, err
	}
	var parsed struct {
		Deals []struct {
			ID  uint64 `json:"id"`
			CID string `json:"cid"`
		} `json:"deals"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return 0, err
	}
	for _, d := range parsed.Deals {
		if d.CID == cid {
			return d.ID, nil
		}
	}
	return 0, fmt.Errorf("deal for cid %s not found yet", cid)
}

func (l *Lab) uploadToProvider(owner, cid string, dealID uint64, data []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("owner", owner)
	_ = mw.WriteField("cid", cid)
	_ = mw.WriteField("deal_id", strconv.FormatUint(dealID, 10))
	part, err := mw.CreateFormFile("file", cid)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, l.Provider+"/v1/upload", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := l.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("provider upload HTTP %d: %s", res.StatusCode, string(body))
	}
	return nil
}
