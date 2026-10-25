package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/VaultChainHQ/vaultchain/pkg/chunk"
)

// Disk layout (everything under <data>/objects):
//
//	<root>/manifest.json   the chain manifest the object was stored under
//	<root>/chunks/<i>      32-byte leaf hash || chunk bytes
//
// Chunk files are written to a temp name and renamed, so a chunk is either
// absent or complete. Reads re-hash every chunk against its stored leaf, and
// an object is only served after its leaves have been checked against the
// content root.

const leafLen = 32

var (
	errNotFound = errors.New("object not found")
	errPartial  = errors.New("object incomplete")
	errCorrupt  = errors.New("stored data failed integrity check")
)

type objectStore struct {
	dir      string
	verified sync.Map // root -> struct{}: leaves and file sizes checked against the root
}

func newObjectStore(dataDir string) (*objectStore, error) {
	s := &objectStore{dir: filepath.Join(dataDir, "objects")}
	return s, os.MkdirAll(s.dir, 0o755)
}

func validRoot(root string) bool {
	if len(root) != 64 {
		return false
	}
	for _, c := range root {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *objectStore) objDir(root string) string { return filepath.Join(s.dir, root) }
func (s *objectStore) chunkPath(root string, i int) string {
	return filepath.Join(s.dir, root, "chunks", strconv.Itoa(i))
}

func writeAtomic(path string, write func(w io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := write(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// putChunkAt writes one verified chunk below base (an object directory).
func putChunkAt(base string, i int, leaf chunk.Hash, data []byte) error {
	return writeAtomic(filepath.Join(base, "chunks", strconv.Itoa(i)), func(w io.Writer) error {
		if _, err := w.Write(leaf[:]); err != nil {
			return err
		}
		_, err := w.Write(data)
		return err
	})
}

func (s *objectStore) writeManifest(base string, m chunk.Manifest) error {
	return writeAtomic(filepath.Join(base, "manifest.json"), func(w io.Writer) error {
		return json.NewEncoder(w).Encode(m)
	})
}

func (s *objectStore) putChunk(m chunk.Manifest, i int, leaf chunk.Hash, data []byte) error {
	base := s.objDir(m.Root)
	if _, err := os.Stat(filepath.Join(base, "manifest.json")); err != nil {
		if err := s.writeManifest(base, m); err != nil {
			return err
		}
	}
	return putChunkAt(base, i, leaf, data)
}

// manifest returns the locally stored manifest of an object.
func (s *objectStore) manifest(root string) (chunk.Manifest, error) {
	var m chunk.Manifest
	b, err := os.ReadFile(filepath.Join(s.objDir(root), "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, errNotFound
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Root != root || m.Validate() != nil {
		return m, errCorrupt
	}
	return m, nil
}

// have counts chunks that are present with the expected size.
func (s *objectStore) have(m chunk.Manifest) int {
	n := 0
	for i := 0; i < m.NumChunks(); i++ {
		if st, err := os.Stat(s.chunkPath(m.Root, i)); err == nil && st.Size() == int64(leafLen+m.ChunkLen(i)) {
			n++
		}
	}
	return n
}

func (s *objectStore) readLeaf(m chunk.Manifest, i int) (chunk.Hash, error) {
	var h chunk.Hash
	f, err := os.Open(s.chunkPath(m.Root, i))
	if err != nil {
		return h, err
	}
	defer f.Close()
	_, err = io.ReadFull(f, h[:])
	return h, err
}

// leaves loads every leaf hash. It fails with errPartial if any chunk is missing.
func (s *objectStore) leaves(m chunk.Manifest) ([]chunk.Hash, error) {
	n := m.NumChunks()
	out := make([]chunk.Hash, n)
	for i := 0; i < n; i++ {
		st, err := os.Stat(s.chunkPath(m.Root, i))
		if errors.Is(err, os.ErrNotExist) {
			return nil, errPartial
		}
		if err != nil {
			return nil, err
		}
		if st.Size() != int64(leafLen+m.ChunkLen(i)) {
			return nil, errCorrupt
		}
		if out[i], err = s.readLeaf(m, i); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// verify checks (once per process) that the stored leaves hash to the root.
func (s *objectStore) verify(m chunk.Manifest) ([]chunk.Hash, error) {
	leaves, err := s.leaves(m)
	if err != nil {
		return nil, err
	}
	if chunk.ContentRoot(chunk.TreeRoot(leaves), m.Size, m.ChunkSize) != m.Root {
		s.verified.Delete(m.Root)
		return nil, errCorrupt
	}
	s.verified.Store(m.Root, struct{}{})
	return leaves, nil
}

func (s *objectStore) ensureVerified(m chunk.Manifest) error {
	if _, ok := s.verified.Load(m.Root); ok {
		return nil
	}
	_, err := s.verify(m)
	return err
}

// readChunk returns chunk i after re-hashing it against its stored leaf.
func (s *objectStore) readChunk(m chunk.Manifest, i int) ([]byte, error) {
	if i < 0 || i >= m.NumChunks() {
		return nil, fmt.Errorf("chunk index %d out of range", i)
	}
	b, err := os.ReadFile(s.chunkPath(m.Root, i))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errPartial
	}
	if err != nil {
		return nil, err
	}
	if len(b) != leafLen+m.ChunkLen(i) {
		return nil, errCorrupt
	}
	data := b[leafLen:]
	if chunk.HashLeaf(data) != chunk.Hash(b[:leafLen]) {
		return nil, errCorrupt
	}
	return data, nil
}

// proof builds the Merkle path of chunk i (requires a complete object).
func (s *objectStore) proof(m chunk.Manifest, i int) (chunk.Proof, error) {
	leaves, err := s.leaves(m)
	if err != nil {
		return nil, err
	}
	return chunk.Prove(leaves, i)
}

func (s *objectStore) countObjects() int {
	ents, _ := os.ReadDir(s.dir)
	n := 0
	for _, e := range ents {
		if e.IsDir() && validRoot(e.Name()) {
			n++
		}
	}
	return n
}

func (s *objectStore) newStaging() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	d := filepath.Join(s.dir, ".staging-"+hex.EncodeToString(b[:]))
	return d, os.MkdirAll(d, 0o755)
}

// commitStaging moves a fully verified staging directory into place.
func (s *objectStore) commitStaging(stage string, m chunk.Manifest) error {
	dst := s.objDir(m.Root)
	if _, err := s.verify(m); err == nil { // already stored completely
		return os.RemoveAll(stage)
	}
	if err := os.RemoveAll(dst); err != nil { // drop a partial or corrupt earlier copy
		return err
	}
	s.verified.Delete(m.Root)
	return os.Rename(stage, dst)
}

// objReader streams an object with Read/Seek (so http.ServeContent can serve
// Range requests), holding one chunk in memory and re-hashing each as it loads.
type objReader struct {
	s   *objectStore
	m   chunk.Manifest
	pos int64
	cur int
	buf []byte
}

func (s *objectStore) open(m chunk.Manifest) (*objReader, error) {
	if err := s.ensureVerified(m); err != nil {
		return nil, err
	}
	return &objReader{s: s, m: m, cur: -1}, nil
}

func (r *objReader) Read(p []byte) (int, error) {
	if r.pos >= int64(r.m.Size) {
		return 0, io.EOF
	}
	idx := int(r.pos / int64(r.m.ChunkSize))
	if r.cur != idx {
		b, err := r.s.readChunk(r.m, idx)
		if err != nil {
			return 0, err
		}
		r.buf, r.cur = b, idx
	}
	off := int(r.pos - int64(idx)*int64(r.m.ChunkSize))
	n := copy(p, r.buf[off:])
	r.pos += int64(n)
	return n, nil
}

func (r *objReader) Seek(offset int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = r.pos + offset
	case io.SeekEnd:
		np = int64(r.m.Size) + offset
	default:
		return 0, errors.New("bad whence")
	}
	if np < 0 {
		return 0, errors.New("negative position")
	}
	r.pos = np
	return np, nil
}
