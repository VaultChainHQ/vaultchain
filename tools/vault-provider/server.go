package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/x/storage"
)

const version = "vault-provider/2"

type server struct {
	addr    string // this provider's vault address; deals must name it
	rest    string // vaultd REST base URL
	chainID string
	origins map[string]bool // allowed CORS origins (empty: CORS disabled)
	store   *objectStore
	client  *http.Client

	mu        sync.Mutex
	manifests map[string]chunk.Manifest // chain manifests are immutable once committed
}

func newServer(addr, rest, chainID, dataDir string, origins []string) (*server, error) {
	st, err := newObjectStore(dataDir)
	if err != nil {
		return nil, err
	}
	s := &server{
		addr: addr, rest: strings.TrimRight(rest, "/"), chainID: chainID, store: st,
		origins: map[string]bool{}, manifests: map[string]chunk.Manifest{},
		client: &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" && o != "*" {
			s.origins[o] = true
		}
	}
	return s, nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleStatus)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/version", s.handleVersion)
	mux.HandleFunc("/v1/objects/", s.handleObjects)
	mux.HandleFunc("/v1/chunks/", s.handleChunks)
	return s.withCORS(mux)
}

// withCORS only answers cross-origin requests from configured origins.
func (s *server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			w.Header().Add("Vary", "Origin")
			if s.origins[o] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", o)
				h.Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Range, X-Vault-Proof")
				h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, X-Vault-Proof")
				h.Set("Access-Control-Max-Age", "600")
			} else if r.Method == http.MethodOptions {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
		}
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

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/status" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": version, "address": s.addr, "chain_id": s.chainID,
		"objects": s.store.countObjects(),
	})
}

func (s *server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

// --- chain lookups -------------------------------------------------------

var errNotCommitted = errors.New("manifest not committed on chain")

func (s *server) chainGet(path string, out any) (int, error) {
	resp, err := s.client.Get(s.rest + path)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// chainManifest returns the committed manifest for root.
func (s *server) chainManifest(root string) (chunk.Manifest, error) {
	s.mu.Lock()
	m, ok := s.manifests[root]
	s.mu.Unlock()
	if ok {
		return m, nil
	}
	var res struct {
		Manifest struct {
			Root      string `json:"root"`
			Size      uint64 `json:"size"`
			ChunkSize uint32 `json:"chunk_size"`
			Mime      string `json:"mime"`
			Name      string `json:"name"`
		} `json:"manifest"`
	}
	code, err := s.chainGet("/vaultchain/storage/manifests/"+url.PathEscape(root), &res)
	if err != nil {
		return m, fmt.Errorf("chain unreachable: %w", err)
	}
	if code == http.StatusNotFound {
		return m, errNotCommitted
	}
	if code != http.StatusOK {
		return m, fmt.Errorf("chain returned HTTP %d", code)
	}
	r := res.Manifest
	m = chunk.Manifest{Root: r.Root, Size: r.Size, ChunkSize: r.ChunkSize, Mime: r.Mime, Name: r.Name}
	if m.Root != root || m.Validate() != nil {
		return m, errors.New("chain returned an invalid manifest")
	}
	s.mu.Lock()
	s.manifests[root] = m
	s.mu.Unlock()
	return m, nil
}

// authorizeWrite requires a pending or open deal for root in which this
// provider holds an assigned or active replica.
func (s *server) authorizeWrite(root string) (chunk.Manifest, int, error) {
	m, err := s.chainManifest(root)
	if errors.Is(err, errNotCommitted) {
		return m, http.StatusNotFound, err
	}
	if err != nil {
		return m, http.StatusBadGateway, err
	}
	var res struct {
		Deals []storage.Deal `json:"deals"`
	}
	q := url.Values{"cid": {root}, "provider": {s.addr}}
	code, err := s.chainGet("/vaultchain/storage/deals?"+q.Encode(), &res)
	if err != nil || code != http.StatusOK {
		return m, http.StatusBadGateway, fmt.Errorf("cannot verify deal on chain (HTTP %d, %v)", code, err)
	}
	for _, d := range res.Deals {
		if d.CID != root || (d.Status != storage.DealPending && d.Status != storage.DealOpen) {
			continue
		}
		if _, ok := d.ReplicaIndex(s.addr); ok {
			return m, 0, nil
		}
	}
	return m, http.StatusForbidden, errors.New("no pending or open deal for this object assigned to this provider")
}

// --- objects ---------------------------------------------------------------

func parseRoot(rest string) (root, tail string, ok bool) {
	root, tail, _ = strings.Cut(rest, "/")
	return root, tail, validRoot(root)
}

func (s *server) handleObjects(w http.ResponseWriter, r *http.Request) {
	root, tail, ok := parseRoot(strings.TrimPrefix(r.URL.Path, "/v1/objects/"))
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("object id must be a 64-char lowercase hex content root"))
		return
	}
	switch {
	case tail == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.serveObject(w, r, root)
	case tail == "" && (r.Method == http.MethodPost || r.Method == http.MethodPut):
		s.uploadObject(w, r, root)
	case tail == "status" && r.Method == http.MethodGet:
		s.objectStatus(w, root)
	default:
		writeErr(w, http.StatusMethodNotAllowed, errors.New("unsupported method or path"))
	}
}

func (s *server) objectStatus(w http.ResponseWriter, root string) {
	m, err := s.store.manifest(root)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	have := s.store.have(m)
	writeJSON(w, http.StatusOK, map[string]any{
		"root": root, "chunks_have": have, "chunks_total": m.NumChunks(), "complete": have == m.NumChunks(),
	})
}

func (s *server) storeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound), errors.Is(err, errPartial):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, errCorrupt):
		writeErr(w, http.StatusInternalServerError, err)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}

// uploadObject streams a whole object into the store (see ingest).
func (s *server) uploadObject(w http.ResponseWriter, r *http.Request, root string) {
	m, code, err := s.authorizeWrite(root)
	if err != nil {
		writeErr(w, code, err)
		return
	}
	n, code, err := s.ingest(http.MaxBytesReader(w, r.Body, int64(m.Size)+1), root, m)
	if err != nil {
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"root": root, "size": m.Size, "chunks": n, "complete": true})
}

// ingest streams a whole object (chunk by chunk, never buffering it) into a
// staging directory, and only installs it if it hashes to the committed root.
func (s *server) ingest(body io.Reader, root string, m chunk.Manifest) (int, int, error) {
	stage, err := s.store.newStaging()
	if err != nil {
		return 0, http.StatusInternalServerError, err
	}
	defer os.RemoveAll(stage) // no-op once renamed into place
	var leaves []chunk.Hash
	size, err := chunk.ReadChunks(body, int(m.ChunkSize), func(i int, c []byte) error {
		leaf := chunk.HashLeaf(c)
		leaves = append(leaves, leaf)
		return putChunkAt(stage, i, leaf, c)
	})
	if err != nil {
		return 0, http.StatusBadRequest, fmt.Errorf("upload failed: %w", err)
	}
	if size != m.Size {
		return 0, http.StatusBadRequest, fmt.Errorf("size %d does not match manifest size %d", size, m.Size)
	}
	if got := chunk.ContentRoot(chunk.TreeRoot(leaves), size, m.ChunkSize); got != root {
		return 0, http.StatusBadRequest, errors.New("content does not hash to the committed root")
	}
	if err := s.store.writeManifest(stage, m); err != nil {
		return 0, http.StatusInternalServerError, err
	}
	if err := s.store.commitStaging(stage, m); err != nil {
		return 0, http.StatusInternalServerError, err
	}
	return len(leaves), 0, nil
}

// inlineMIME lists types that are safe to render in a browser; everything else
// (notably text/html and image/svg+xml) is forced to download.
var inlineMIME = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"video/mp4": true, "video/webm": true, "audio/mpeg": true, "audio/ogg": true, "audio/wav": true,
	"application/pdf": true, "text/plain": true,
}

func (s *server) serveObject(w http.ResponseWriter, r *http.Request, root string) {
	m, err := s.store.manifest(root)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	rd, err := s.store.open(m)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", m.Mime)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+root+`"`)
	if !inlineMIME[m.Mime] {
		disp := "attachment"
		if m.Name != "" {
			if v := mime.FormatMediaType("attachment", map[string]string{"filename": m.Name}); v != "" {
				disp = v
			}
		}
		h.Set("Content-Disposition", disp)
	}
	// ServeContent handles Range / If-Range / HEAD; a chunk that fails its
	// re-hash mid-stream aborts the response instead of serving bad bytes.
	http.ServeContent(w, r, "", time.Time{}, rd)
}

// --- chunks ----------------------------------------------------------------

func (s *server) handleChunks(w http.ResponseWriter, r *http.Request) {
	root, tail, ok := parseRoot(strings.TrimPrefix(r.URL.Path, "/v1/chunks/"))
	idx, err := strconv.Atoi(tail)
	if !ok || err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, errors.New("path must be /v1/chunks/{root}/{index}"))
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.putChunk(w, r, root, idx)
	case http.MethodGet, http.MethodHead:
		s.getChunk(w, r, root, idx)
	default:
		writeErr(w, http.StatusMethodNotAllowed, errors.New("GET or PUT required"))
	}
}

func parseProof(h string) (chunk.Proof, error) {
	var p chunk.Proof
	if strings.TrimSpace(h) == "" {
		return p, nil
	}
	for _, part := range strings.Split(h, ",") {
		b, err := hex.DecodeString(strings.TrimSpace(part))
		if err != nil || len(b) != 32 {
			return nil, errors.New("X-Vault-Proof must be comma-separated 32-byte hex hashes")
		}
		p = append(p, b)
	}
	return p, nil
}

// putChunk stores one chunk; the X-Vault-Proof header carries its Merkle path
// (leaf-to-root sibling hashes, hex, comma-separated) so it can be checked
// against the committed root without the rest of the object.
func (s *server) putChunk(w http.ResponseWriter, r *http.Request, root string, idx int) {
	m, code, err := s.authorizeWrite(root)
	if err != nil {
		writeErr(w, code, err)
		return
	}
	if idx >= m.NumChunks() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("chunk index %d out of range (0..%d)", idx, m.NumChunks()-1))
		return
	}
	proof, err := parseProof(r.Header.Get("X-Vault-Proof"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	want := m.ChunkLen(idx)
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(want)+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(data) != want {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("chunk %d must be %d bytes, got %d", idx, want, len(data)))
		return
	}
	if err := chunk.VerifyProof(m, idx, data, proof); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("proof check failed: %w", err))
		return
	}
	if err := s.store.putChunk(m, idx, chunk.HashLeaf(data), data); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	have := s.store.have(m)
	writeJSON(w, http.StatusOK, map[string]any{
		"root": root, "index": idx, "chunks_have": have, "chunks_total": m.NumChunks(), "complete": have == m.NumChunks(),
	})
}

func (s *server) getChunk(w http.ResponseWriter, r *http.Request, root string, idx int) {
	m, err := s.store.manifest(root)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	data, err := s.store.readChunk(m, idx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	// Attach the Merkle path when the whole object is present, so a client can
	// verify the chunk against the on-chain root.
	if p, err := s.store.proof(m, idx); err == nil {
		parts := make([]string, len(p))
		for i, h := range p {
			parts[i] = hex.EncodeToString(h)
		}
		w.Header().Set("X-Vault-Proof", strings.Join(parts, ","))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
