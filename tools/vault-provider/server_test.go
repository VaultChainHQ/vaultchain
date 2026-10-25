package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vaultchain/vaultchain/pkg/chunk"
)

const provAddr = "vault1provider"

// fakeChain serves the two REST endpoints the provider uses.
type fakeChain struct {
	mans  map[string]chunk.Manifest
	deals []map[string]string

	owner     string           // owner of every committed manifest
	grants    []map[string]any // access grants (cid, grantee, capability, expiry_height)
	publicAll bool             // every object behaves as if granted to "*"
	height    int64
}

func (f *fakeChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/vaultchain/storage/manifests/"):
		m, ok := f.mans[strings.TrimPrefix(r.URL.Path, "/vaultchain/storage/manifests/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"manifest": map[string]any{
			"root": m.Root, "size": m.Size, "chunk_size": m.ChunkSize, "mime": m.Mime, "name": m.Name, "owner": f.owner}})
	case r.URL.Path == "/cosmos/base/tendermint/v1beta1/node_info":
		json.NewEncoder(w).Encode(map[string]any{"height": f.height})
	case r.URL.Path == "/vaultchain/access/grants":
		cid := r.URL.Query().Get("cid")
		out := []map[string]any{}
		for _, g := range f.grants {
			if g["cid"] == cid {
				out = append(out, g)
			}
		}
		if f.publicAll {
			out = append(out, map[string]any{"cid": cid, "grantee": "*", "capability": "read", "expiry_height": 0})
		}
		json.NewEncoder(w).Encode(map[string]any{"grants": out})
	case r.URL.Path == "/vaultchain/storage/deals":
		out := []map[string]any{}
		for _, d := range f.deals {
			if c := r.URL.Query().Get("cid"); c != "" && d["cid"] != c {
				continue
			}
			if p := r.URL.Query().Get("provider"); p != "" && d["provider"] != p {
				continue
			}
			// Real deals list replicas; the fake keeps one provider per deal.
			out = append(out, map[string]any{"cid": d["cid"], "status": d["status"],
				"replicas": []map[string]string{{"provider": d["provider"], "status": "active"}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"deals": out})
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	t     *testing.T
	chain *fakeChain
	s     *server
	ts    *httptest.Server
	dir   string
}

func newEnv(t *testing.T, origins ...string) *env {
	t.Helper()
	chain := &fakeChain{mans: map[string]chunk.Manifest{}, owner: "vault1owner", publicAll: true, height: 10}
	cs := httptest.NewServer(chain)
	t.Cleanup(cs.Close)
	dir := t.TempDir()
	s, err := newServer(provAddr, cs.URL, "test-1", dir, origins)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return &env{t: t, chain: chain, s: s, ts: ts, dir: dir}
}

func testData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// commit registers data's manifest on the fake chain (and, if deal, an open deal for this provider).
func (e *env) commit(data []byte, mime, name string, deal bool) (chunk.Manifest, []chunk.Hash) {
	e.t.Helper()
	m, leaves, err := chunk.BuildBytes(data, 4096, mime, name)
	if err != nil {
		e.t.Fatal(err)
	}
	e.chain.mans[m.Root] = m
	if deal {
		e.chain.deals = append(e.chain.deals, map[string]string{"cid": m.Root, "provider": provAddr, "status": "open"})
	}
	return m, leaves
}

func (e *env) do(method, path string, body io.Reader, hdr ...string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, r *http.Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

func (e *env) upload(m chunk.Manifest, data []byte) *http.Response {
	return e.do("POST", "/v1/objects/"+m.Root, bytes.NewReader(data))
}

func TestUploadDownloadRangeAndContentType(t *testing.T) {
	e := newEnv(t)
	data := testData(10000) // 3 chunks of 4096
	m, _ := e.commit(data, "image/png", "pic.png", true)
	if r := e.upload(m, data); r.StatusCode != 200 {
		t.Fatalf("upload: %d %s", r.StatusCode, readBody(t, r))
	}
	r := e.do("GET", "/v1/objects/"+m.Root, nil)
	got := readBody(t, r)
	if r.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download mismatch: %d len=%d", r.StatusCode, len(got))
	}
	if ct := r.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type %q", ct)
	}
	if r.Header.Get("Content-Disposition") != "" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", r.Header)
	}
	if r.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatal("no range support advertised")
	}
	// Range across a chunk boundary.
	r = e.do("GET", "/v1/objects/"+m.Root, nil, "Range", "bytes=4090-8200")
	if r.StatusCode != 206 {
		t.Fatalf("range status %d", r.StatusCode)
	}
	if b := readBody(t, r); !bytes.Equal(b, data[4090:8201]) {
		t.Fatal("range body mismatch")
	}
	if cr := r.Header.Get("Content-Range"); cr != "bytes 4090-8200/10000" {
		t.Fatalf("content-range %q", cr)
	}
	// Suffix range and HEAD.
	r = e.do("GET", "/v1/objects/"+m.Root, nil, "Range", "bytes=-100")
	if b := readBody(t, r); r.StatusCode != 206 || !bytes.Equal(b, data[9900:]) {
		t.Fatal("suffix range mismatch")
	}
	r = e.do("HEAD", "/v1/objects/"+m.Root, nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Length") != "10000" {
		t.Fatalf("head: %d %v", r.StatusCode, r.Header)
	}
	// Status endpoint.
	r = e.do("GET", "/v1/objects/"+m.Root+"/status", nil)
	var st struct {
		Have, Total int
		Complete    bool
	}
	var raw map[string]any
	json.Unmarshal(readBody(t, r), &raw)
	if raw["complete"] != true || raw["chunks_have"] != float64(3) {
		t.Fatalf("status %v", raw)
	}
	_ = st
}

func TestRiskyMimeIsForcedToDownload(t *testing.T) {
	e := newEnv(t)
	data := testData(100)
	m, _ := e.commit(data, "text/html", "page.html", true)
	if r := e.upload(m, data); r.StatusCode != 200 {
		t.Fatal(readBody(t, r))
	}
	r := e.do("GET", "/v1/objects/"+m.Root, nil)
	readBody(t, r)
	if !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(r.Header.Get("Content-Disposition"), "page.html") {
		t.Fatalf("disposition %q", r.Header.Get("Content-Disposition"))
	}
	if r.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
}

func TestWritesRequireOpenDealForThisProvider(t *testing.T) {
	e := newEnv(t)
	data := testData(5000)
	// Not committed at all.
	m0, _, _ := chunk.BuildBytes(data, 4096, "text/plain", "x")
	if r := e.upload(m0, data); r.StatusCode != 404 {
		t.Fatalf("uncommitted: %d", r.StatusCode)
	}
	// Committed but no deal.
	m, _ := e.commit(data, "text/plain", "x", false)
	if r := e.upload(m, data); r.StatusCode != 403 {
		t.Fatalf("no deal: %d", r.StatusCode)
	}
	// Deal assigned to someone else, or closed.
	e.chain.deals = append(e.chain.deals,
		map[string]string{"cid": m.Root, "provider": "someone-else", "status": "open"},
		map[string]string{"cid": m.Root, "provider": provAddr, "status": "closed"})
	if r := e.upload(m, data); r.StatusCode != 403 {
		t.Fatalf("other/closed deal: %d", r.StatusCode)
	}
	if e.s.store.countObjects() != 0 {
		t.Fatal("rejected upload left data behind")
	}
	e.chain.deals = append(e.chain.deals, map[string]string{"cid": m.Root, "provider": provAddr, "status": "open"})
	if r := e.upload(m, data); r.StatusCode != 200 {
		t.Fatalf("with deal: %d", r.StatusCode)
	}
}

func TestBadUploadsLeaveNothingBehind(t *testing.T) {
	e := newEnv(t)
	data := testData(10000)
	m, _ := e.commit(data, "text/plain", "x", true)
	bad := append([]byte(nil), data...)
	bad[9000] ^= 0xff
	cases := map[string][]byte{
		"flipped byte": bad,
		"truncated":    data[:9999],
		"too long":     append(append([]byte(nil), data...), 1),
		"empty":        {},
	}
	for name, body := range cases {
		if r := e.upload(m, body); r.StatusCode != 400 {
			t.Errorf("%s: status %d", name, r.StatusCode)
		}
	}
	if e.s.store.countObjects() != 0 {
		t.Fatal("object installed from bad upload")
	}
	ents, _ := os.ReadDir(filepath.Join(e.dir, "objects"))
	if len(ents) != 0 {
		t.Fatalf("staging leftovers: %v", ents)
	}
	if r := e.do("GET", "/v1/objects/"+m.Root, nil); r.StatusCode != 404 {
		t.Fatalf("get after bad uploads: %d", r.StatusCode)
	}
}

func proofHeader(p chunk.Proof) string {
	parts := make([]string, len(p))
	for i, h := range p {
		parts[i] = hex.EncodeToString(h)
	}
	return strings.Join(parts, ",")
}

func TestChunkPutGetWithProofs(t *testing.T) {
	e := newEnv(t)
	data := testData(10000)
	m, leaves := e.commit(data, "application/octet-stream", "blob", true)
	n := m.NumChunks()
	put := func(i int, body []byte, proofIdx int) *http.Response {
		p, err := chunk.Prove(leaves, proofIdx)
		if err != nil {
			t.Fatal(err)
		}
		return e.do("PUT", "/v1/chunks/"+m.Root+"/"+strconv.Itoa(i), bytes.NewReader(body), "X-Vault-Proof", proofHeader(p))
	}
	chunkOf := func(i int) []byte {
		lo := i * 4096
		hi := lo + m.ChunkLen(i)
		return data[lo:hi]
	}
	// Wrong proof (for another index), wrong length, tampered bytes, out of range: all rejected.
	if r := put(0, chunkOf(0), 1); r.StatusCode != 400 {
		t.Fatalf("wrong proof: %d", r.StatusCode)
	}
	if r := put(0, chunkOf(0)[:100], 0); r.StatusCode != 400 {
		t.Fatalf("short chunk: %d", r.StatusCode)
	}
	tam := append([]byte(nil), chunkOf(0)...)
	tam[0] ^= 1
	if r := put(0, tam, 0); r.StatusCode != 400 {
		t.Fatalf("tampered chunk: %d", r.StatusCode)
	}
	if r := e.do("PUT", "/v1/chunks/"+m.Root+"/9", bytes.NewReader(tam)); r.StatusCode != 400 {
		t.Fatalf("out of range: %d", r.StatusCode)
	}
	if r := e.do("PUT", "/v1/chunks/"+m.Root+"/0", bytes.NewReader(chunkOf(0)), "X-Vault-Proof", "zz"); r.StatusCode != 400 {
		t.Fatalf("garbage proof header: %d", r.StatusCode)
	}
	if e.s.store.countObjects() != 0 {
		t.Fatal("rejected chunk was stored")
	}
	// Out-of-order valid chunks; object is not served until complete.
	for _, i := range []int{2, 0} {
		if r := put(i, chunkOf(i), i); r.StatusCode != 200 {
			t.Fatalf("put %d: %d %s", i, r.StatusCode, readBody(t, r))
		}
	}
	if r := e.do("GET", "/v1/objects/"+m.Root, nil); r.StatusCode != 404 {
		t.Fatalf("partial object served: %d", r.StatusCode)
	}
	if r := put(1, chunkOf(1), 1); r.StatusCode != 200 {
		t.Fatal(readBody(t, r))
	}
	r := e.do("GET", "/v1/objects/"+m.Root, nil)
	if b := readBody(t, r); !bytes.Equal(b, data) {
		t.Fatal("assembled object mismatch")
	}
	// GET chunk returns bytes plus a proof that verifies against the root.
	for i := 0; i < n; i++ {
		r := e.do("GET", "/v1/chunks/"+m.Root+"/"+strconv.Itoa(i), nil)
		b := readBody(t, r)
		if !bytes.Equal(b, chunkOf(i)) {
			t.Fatalf("chunk %d mismatch", i)
		}
		p, err := parseProof(r.Header.Get("X-Vault-Proof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := chunk.VerifyProof(m, i, b, p); err != nil {
			t.Fatalf("served proof for chunk %d invalid: %v", i, err)
		}
	}
}

func TestSinglechunkObjectViaChunkPut(t *testing.T) {
	e := newEnv(t)
	data := testData(300)
	m, _ := e.commit(data, "text/plain", "s", true)
	if r := e.do("PUT", "/v1/chunks/"+m.Root+"/0", bytes.NewReader(data)); r.StatusCode != 200 { // empty proof
		t.Fatalf("%d %s", r.StatusCode, readBody(t, r))
	}
	if b := readBody(t, e.do("GET", "/v1/objects/"+m.Root, nil)); !bytes.Equal(b, data) {
		t.Fatal("mismatch")
	}
}

func TestReadsRehashAndRefuseCorruptedData(t *testing.T) {
	e := newEnv(t)
	data := testData(10000)
	m, _ := e.commit(data, "text/plain", "x", true)
	if r := e.upload(m, data); r.StatusCode != 200 {
		t.Fatal(readBody(t, r))
	}
	// Flip a byte inside chunk 1's data on disk.
	p := e.s.store.chunkPath(m.Root, 1)
	b, _ := os.ReadFile(p)
	b[leafLen+10] ^= 0xff
	os.WriteFile(p, b, 0o644)

	// Chunk 0 is still fine, even by Range.
	r := e.do("GET", "/v1/objects/"+m.Root, nil, "Range", "bytes=0-4095")
	if got := readBody(t, r); !bytes.Equal(got, data[:4096]) {
		t.Fatal("healthy range broken")
	}
	// Anything touching chunk 1 must not deliver the corrupted bytes.
	r = e.do("GET", "/v1/objects/"+m.Root, nil)
	got, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err == nil && len(got) == len(data) {
		t.Fatal("corrupted object served as complete")
	}
	if bytes.Contains(got, b[leafLen:leafLen+20]) && len(got) > 4096 {
		t.Fatal("corrupted bytes were served")
	}
	if len(got) > 4096 {
		t.Fatalf("served %d bytes past the corrupt chunk", len(got))
	}
	if r := e.do("GET", "/v1/chunks/"+m.Root+"/1", nil); r.StatusCode != 500 {
		t.Fatalf("chunk GET of corrupt chunk: %d", r.StatusCode)
	}
}

func TestConsistentlyForgedChunkIsCaughtByRoot(t *testing.T) {
	e := newEnv(t)
	data := testData(10000)
	m, _ := e.commit(data, "text/plain", "x", true)
	if r := e.upload(m, data); r.StatusCode != 200 {
		t.Fatal(readBody(t, r))
	}
	// An attacker rewrites chunk 2 together with its leaf, so the per-chunk hash matches.
	forged := bytes.Repeat([]byte{9}, m.ChunkLen(2))
	leaf := chunk.HashLeaf(forged)
	os.WriteFile(e.s.store.chunkPath(m.Root, 2), append(leaf[:], forged...), 0o644)
	r := e.do("GET", "/v1/objects/"+m.Root, nil)
	readBody(t, r)
	if r.StatusCode != 500 {
		t.Fatalf("forged object status %d", r.StatusCode)
	}
}

func TestCORSIsRestricted(t *testing.T) {
	e := newEnv(t, "http://localhost:8080", "*")
	data := testData(10)
	m, _ := e.commit(data, "text/plain", "x", true)
	e.upload(m, data)

	r := e.do("GET", "/v1/objects/"+m.Root, nil)
	readBody(t, r)
	if r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header without Origin")
	}
	r = e.do("GET", "/v1/objects/"+m.Root, nil, "Origin", "http://evil.example")
	readBody(t, r)
	if r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("evil origin allowed: %v", r.Header)
	}
	r = e.do("GET", "/v1/objects/"+m.Root, nil, "Origin", "http://localhost:8080")
	readBody(t, r)
	if r.Header.Get("Access-Control-Allow-Origin") != "http://localhost:8080" || r.Header.Get("Vary") == "" {
		t.Fatalf("allowed origin not echoed: %v", r.Header)
	}
	if r := e.do("OPTIONS", "/v1/objects/"+m.Root, nil, "Origin", "http://evil.example"); r.StatusCode != 403 {
		t.Fatalf("evil preflight: %d", r.StatusCode)
	}
	r = e.do("OPTIONS", "/v1/chunks/"+m.Root+"/0", nil, "Origin", "http://localhost:8080")
	if r.StatusCode != 204 || !strings.Contains(r.Header.Get("Access-Control-Allow-Headers"), "X-Vault-Proof") {
		t.Fatalf("preflight: %d %v", r.StatusCode, r.Header)
	}
}

func TestNoListingAndLegacyRoutesGone(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/list", "/v1/download/abc", "/download/abc", "/upload", "/v1/upload", "/v1/objects", "/v1/objects/"} {
		r := e.do("GET", p, nil)
		readBody(t, r)
		if r.StatusCode != 404 && r.StatusCode != 400 {
			t.Errorf("%s -> %d", p, r.StatusCode)
		}
	}
	if r := e.do("GET", "/v1/objects/..%2f..%2fetc", nil); r.StatusCode == 200 {
		t.Fatal("path traversal accepted")
	}
	r := e.do("GET", "/status", nil)
	b := readBody(t, r)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"objects"`) {
		t.Fatalf("status: %d %s", r.StatusCode, b)
	}
}

func TestLargeUploadStreams(t *testing.T) {
	e := newEnv(t)
	// 3 MiB pushed through a pipe: the handler must consume it incrementally.
	data := testData(3 << 20)
	m, _ := e.commit(data, "application/octet-stream", "big", true)
	pr, pw := io.Pipe()
	go func() {
		for off := 0; off < len(data); off += 100000 {
			end := off + 100000
			if end > len(data) {
				end = len(data)
			}
			pw.Write(data[off:end])
		}
		pw.Close()
	}()
	r := e.do("POST", "/v1/objects/"+m.Root, pr)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, readBody(t, r))
	}
	r = e.do("GET", "/v1/objects/"+m.Root, nil, "Range", "bytes=2000000-2000099")
	if b := readBody(t, r); !bytes.Equal(b, data[2000000:2000100]) {
		t.Fatal("range into big object mismatch")
	}
}
