package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/VaultChainHQ/vaultchain/crypto"
	"github.com/VaultChainHQ/vaultchain/node"
	"github.com/VaultChainHQ/vaultchain/pkg/client"
	"github.com/VaultChainHQ/vaultchain/pkg/readauth"
	vtypes "github.com/VaultChainHQ/vaultchain/types"
	"github.com/VaultChainHQ/vaultchain/x/access"
	"github.com/VaultChainHQ/vaultchain/x/storage"
)

// fakeProvider stores uploads and serves them to anyone holding a valid read
// token for it. Access control itself is covered by tools/vault-provider tests;
// here it only gives the UI something to talk to.
type fakeProvider struct {
	addr string
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/status":
		_ = json.NewEncoder(w).Encode(map[string]string{"address": f.addr})
	case strings.HasPrefix(r.URL.Path, "/v1/objects/"):
		root := strings.TrimPrefix(r.URL.Path, "/v1/objects/")
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			f.objs[root] = b
			return
		}
		if _, err := readauth.Verify(readauth.FromRequest(r), f.addr, root, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		b, ok := f.objs[root]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	t     *testing.T
	ui    *ui
	srv   *httptest.Server
	host  string
	bobUI *ui
	fp    *fakeProvider
	node  *client.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	mk := func(name string) (*ui, vcrypto.KeyRecord) {
		kr, err := vcrypto.NewKeyring(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kr.Add(name); err != nil {
			t.Fatal(err)
		}
		s, rec, err := client.KeyringSigner(kr, name)
		if err != nil {
			t.Fatal(err)
		}
		return &ui{rec: rec, signer: s, fee: sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 10)),
			duration: 1000, chunkSize: 4096, maxUpload: 1 << 20}, rec
	}
	alice, ar := mk("alice")
	bob, br := mk("bob")
	prov, pr := mk("prov")

	coins := sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, 1_000_000_000))
	g := &node.Genesis{ChainID: "ui-test-1", GenesisTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Storage: storage.DefaultParams()}
	for _, a := range []string{ar.Address, br.Address, pr.Address} {
		g.Accounts = append(g.Accounts, node.GenesisAccount{Address: a, Coins: coins})
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := node.WriteGenesis(home, g); err != nil {
		t.Fatal(err)
	}
	eng, err := node.NewEngine(node.Config{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(30 * time.Millisecond):
				_ = eng.ProduceBlock()
			}
		}
	}()
	t.Cleanup(func() { close(done); wg.Wait(); _ = eng.Close() })
	rest := httptest.NewServer(eng.RESTHandler())
	t.Cleanup(rest.Close)

	// register the provider
	cl := client.New(rest.URL)
	if _, err := cl.SendWait(g.ChainID, prov.signer, prov.fee, storage.MsgRegisterProvider{Type: storage.MsgTypeRegisterProvider,
		Endpoint: "http://p", Capacity: 1 << 30, Collateral: sdk.NewInt64Coin(vtypes.NativeDenom, 1000)}); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{addr: pr.Address, objs: map[string][]byte{}}
	ps := httptest.NewServer(fp)
	t.Cleanup(ps.Close)

	e := &env{t: t, ui: alice, bobUI: bob, fp: fp, node: cl}
	for _, x := range []*ui{alice, bob} {
		x.c, x.chainID, x.provider, x.providerAddr = client.New(rest.URL), g.ChainID, ps.URL, pr.Address
	}
	e.srv = httptest.NewServer(nil)
	e.srv.Config.Handler = alice.handler()
	t.Cleanup(e.srv.Close)
	alice.listenHost = strings.TrimPrefix(e.srv.URL, "http://")
	e.host = alice.listenHost
	return e
}

func (e *env) do(method, path string, hdr map[string]string, body io.Reader) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *env) apiJSON(path string, in, out any) int {
	e.t.Helper()
	var rd io.Reader
	method := "GET"
	if in != nil {
		b, _ := json.Marshal(in)
		rd, method = bytes.NewReader(b), "POST"
	}
	resp := e.do(method, path, map[string]string{"X-Vault-UI": "1"}, rd)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == 200 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s: %v: %s", path, err, b)
		}
	}
	if resp.StatusCode != 200 {
		e.t.Logf("%s -> %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.StatusCode
}

func (e *env) upload(name string, data []byte, encrypt bool) (int, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("encrypt", map[bool]string{true: "1", false: "0"}[encrypt])
	_ = mw.WriteField("name", name)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(data)
	_ = mw.Close()
	resp := e.do("POST", "/api/upload", map[string]string{"X-Vault-UI": "1", "Content-Type": mw.FormDataContentType()}, &buf)
	defer resp.Body.Close()
	var out struct {
		Root string `json:"root"`
	}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != 200 {
		e.t.Logf("upload -> %d %s", resp.StatusCode, b)
	}
	return resp.StatusCode, out.Root
}

func (e *env) download(root string, decrypt bool) (int, []byte, string) {
	e.t.Helper()
	p := "/api/download?root=" + root
	if decrypt {
		p += "&decrypt=1"
	}
	resp := e.do("GET", p, map[string]string{"X-Vault-UI": "1"}, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header.Get("Content-Disposition")
}

func TestUIGuards(t *testing.T) {
	e := newEnv(t)
	hdr := map[string]string{"X-Vault-UI": "1"}
	for _, c := range []struct {
		name string
		path string
		hdr  map[string]string
		host string
		want int
	}{
		{"index", "/", nil, "", 200},
		{"app.js", "/app.js", nil, "", 200},
		{"no such file", "/main.go", nil, "", 404},
		{"api without header", "/api/info", nil, "", 403},
		{"api with header", "/api/info", hdr, "", 200},
		{"bad origin", "/api/info", map[string]string{"X-Vault-UI": "1", "Origin": "http://evil.example"}, "", 403},
		{"download without header", "/api/download?root=" + strings.Repeat("a", 64), nil, "", 403},
		{"rebinding host", "/api/info", hdr, "evil.example", 403},
	} {
		req, _ := http.NewRequest("GET", e.srv.URL+c.path, nil)
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		if c.host != "" {
			req.Host = c.host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: got %d want %d", c.name, resp.StatusCode, c.want)
		}
	}
	resp := e.do("GET", "/", nil, nil)
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("missing CSP")
	}
}

func TestUIPlainAndEncryptedFlow(t *testing.T) {
	e := newEnv(t)
	var info struct {
		Address         string `json:"address"`
		ProviderAddress string `json:"provider_address"`
		Balance         int64  `json:"balance"`
	}
	if e.apiJSON("/api/info", nil, &info) != 200 || info.Address != e.ui.rec.Address || info.ProviderAddress != e.fp.addr || info.Balance == 0 {
		t.Fatalf("info: %+v", info)
	}

	// --- plaintext file: stored as-is, downloadable, listed with its deal ---
	plain := []byte(strings.Repeat("hello vaultchain ", 1000))
	code, root := e.upload("../../notes\"x.txt", plain, false)
	if code != 200 || len(root) != 64 {
		t.Fatalf("upload plain: %d", code)
	}
	if !bytes.Equal(e.fp.objs[root], plain) {
		t.Fatal("provider did not receive the plaintext bytes")
	}
	code, got, disp := e.download(root, false)
	if code != 200 || !bytes.Equal(got, plain) || !strings.Contains(disp, "notes") || strings.Contains(disp, "..") || strings.Contains(disp, "/") {
		t.Fatalf("download plain: %d %q", code, disp)
	}
	// uploading the same file again must not fail or double-open the deal
	if code, root2 := e.upload("notes.txt", plain, false); code != 200 || root2 != root {
		t.Fatalf("re-upload: %d", code)
	}

	// --- encrypted file ---
	secret := []byte(strings.Repeat("launch code 0000. ", 3000))
	code, eroot := e.upload("secret.txt", secret, true)
	if code != 200 {
		t.Fatalf("upload encrypted: %d", code)
	}
	if bytes.Contains(e.fp.objs[eroot], []byte("launch code")) {
		t.Fatal("provider received plaintext of an encrypted upload")
	}
	code, got, disp = e.download(eroot, true)
	if code != 200 || !bytes.Equal(got, secret) || !strings.Contains(disp, "secret.txt") || strings.Contains(disp, ".vle") {
		t.Fatalf("decrypting download: %d %q", code, disp)
	}
	code, got, disp = e.download(eroot, false)
	if code != 200 || !bytes.Equal(got, e.fp.objs[eroot]) || !strings.Contains(disp, ".vle") {
		t.Fatalf("raw download: %d %q", code, disp)
	}
	// a corrupted object is refused, not half-served
	e.fp.mu.Lock()
	ct := append([]byte(nil), e.fp.objs[eroot]...)
	ct[len(ct)/2] ^= 1
	good := e.fp.objs[eroot]
	e.fp.objs[eroot] = ct
	e.fp.mu.Unlock()
	if code, got, _ := e.download(eroot, true); code != 422 || bytes.Contains(got, []byte("launch")) {
		t.Fatalf("tampered download: %d", code)
	}
	e.fp.mu.Lock()
	e.fp.objs[eroot] = good
	e.fp.mu.Unlock()

	// --- listing ---
	var files struct {
		Files []fileView `json:"files"`
	}
	if e.apiJSON("/api/files", nil, &files) != 200 || len(files.Files) != 2 {
		t.Fatalf("files: %+v", files)
	}
	for _, f := range files.Files {
		if !f.Mine || len(f.Deals) != 1 {
			t.Fatalf("file view: %+v", f)
		}
		if f.Root == eroot && (!f.Encrypted || f.Name != "secret.txt") {
			t.Fatalf("encrypted view: %+v", f)
		}
	}

	// --- grant: refused for a grantee with no pubkey on chain, ok after bob signs a tx ---
	br := e.bobUI.rec
	if c := e.apiJSON("/api/grant", map[string]any{"root": eroot, "grantee": br.Address}, nil); c != 400 {
		t.Fatalf("grant before bob has a pubkey: %d", c)
	}
	if c := e.apiJSON("/api/grant", map[string]any{"root": eroot, "grantee": access.Public}, nil); c != 400 {
		t.Fatalf("public grant of encrypted file: %d", c)
	}
	if err := e.bobUI.send(storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest, Root: strings.Repeat("b", 64), Size: 1, ChunkSize: 4096, Mime: "text/plain"}); err != nil {
		t.Fatalf("bob tx: %v", err)
	}
	if c := e.apiJSON("/api/grant", map[string]any{"root": eroot, "grantee": br.Address}, nil); c != 200 {
		t.Fatalf("grant: %d", c)
	}
	var grants struct {
		Files []fileView `json:"files"`
	}
	e.apiJSON("/api/files", nil, &grants)
	var gid uint64
	for _, f := range grants.Files {
		if f.Root == eroot && len(f.Grants) == 1 && f.Grants[0].Grantee == br.Address && f.Grants[0].KeyEnvelope == "" {
			gid = f.Grants[0].ID
		}
	}
	if gid == 0 {
		t.Fatalf("grant not listed: %+v", grants)
	}
	// bob's own UI can now decrypt it
	bobSrv := httptest.NewServer(e.bobUI.handler())
	t.Cleanup(bobSrv.Close)
	e.bobUI.listenHost = strings.TrimPrefix(bobSrv.URL, "http://")
	breq, _ := http.NewRequest("GET", bobSrv.URL+"/api/download?root="+eroot+"&decrypt=1", nil)
	breq.Header.Set("X-Vault-UI", "1")
	bresp, err := http.DefaultClient.Do(breq)
	if err != nil {
		t.Fatal(err)
	}
	bb, _ := io.ReadAll(bresp.Body)
	bresp.Body.Close()
	if bresp.StatusCode != 200 || !bytes.Equal(bb, secret) {
		t.Fatalf("grantee decrypt: %d", bresp.StatusCode)
	}
	// bob sees it under "shared with me"
	breq, _ = http.NewRequest("GET", bobSrv.URL+"/api/files", nil)
	breq.Header.Set("X-Vault-UI", "1")
	bresp, _ = http.DefaultClient.Do(breq)
	var bf struct {
		Files []fileView `json:"files"`
	}
	_ = json.NewDecoder(bresp.Body).Decode(&bf)
	bresp.Body.Close()
	shared := false
	for _, f := range bf.Files {
		if f.Root == eroot && !f.Mine && f.Encrypted {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("bob does not see the shared file: %+v", bf)
	}

	// --- revoke: the key envelope is gone, so bob's UI can no longer decrypt ---
	if c := e.apiJSON("/api/revoke", map[string]any{"root": eroot, "grant_id": gid}, nil); c != 200 {
		t.Fatalf("revoke: %d", c)
	}
	breq, _ = http.NewRequest("GET", bobSrv.URL+"/api/download?root="+eroot+"&decrypt=1", nil)
	breq.Header.Set("X-Vault-UI", "1")
	bresp, _ = http.DefaultClient.Do(breq)
	bresp.Body.Close()
	if bresp.StatusCode == 200 {
		t.Fatal("revoked grantee could still decrypt through the UI")
	}
	if c := e.apiJSON("/api/revoke", map[string]any{"root": eroot, "grant_id": gid}, nil); c == 200 {
		t.Fatal("revoking a revoked grant should fail")
	}
}
