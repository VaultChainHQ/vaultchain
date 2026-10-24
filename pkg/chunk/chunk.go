// Package chunk implements content addressing for stored objects: fixed-size
// chunking, a Merkle tree over chunk hashes, the content ID ("root") derived
// from it, manifests, and Merkle inclusion proofs for single chunks.
//
// Hashing is domain separated (RFC 6962 style) so a leaf can never be passed
// off as an interior node:
//
//	leaf  = sha256(0x00 || chunk)
//	node  = sha256(0x01 || left || right)
//	root  = sha256(0x02 || treeRoot || size(8, BE) || chunkSize(8, BE))
//
// The tree splits at the largest power of two below n, so any chunk count is
// supported without padding. The content root also commits to the object size
// and chunk size, so a manifest cannot lie about either without changing the ID.
package chunk

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strings"
)

const (
	// DefaultChunkSize is 256 KiB.
	DefaultChunkSize = 256 * 1024
	// MaxChunkSize bounds the memory a reader needs per chunk.
	MaxChunkSize = 64 * 1024 * 1024

	MaxMimeLen = 127
	MaxNameLen = 255
)

// Hash is a sha256 digest.
type Hash = [sha256.Size]byte

// Manifest describes one stored object.
type Manifest struct {
	Root      string `json:"root"`       // hex content ID
	Size      uint64 `json:"size"`       // bytes
	ChunkSize uint32 `json:"chunk_size"` // bytes; every chunk but the last is exactly this long
	Mime      string `json:"mime"`
	Name      string `json:"name"`
}

// NumChunks is the number of chunks the object is split into.
func (m Manifest) NumChunks() int {
	if m.ChunkSize == 0 {
		return 0
	}
	return int((m.Size + uint64(m.ChunkSize) - 1) / uint64(m.ChunkSize))
}

// ChunkLen is the expected byte length of chunk i.
func (m Manifest) ChunkLen(i int) int {
	n := m.NumChunks()
	if i < 0 || i >= n {
		return 0
	}
	if i < n-1 {
		return int(m.ChunkSize)
	}
	return int(m.Size - uint64(n-1)*uint64(m.ChunkSize))
}

// Validate checks the shape of a manifest (not that the root matches data).
func (m Manifest) Validate() error {
	if len(m.Root) != 2*sha256.Size {
		return errors.New("root must be 64 hex chars")
	}
	if _, err := hex.DecodeString(m.Root); err != nil || m.Root != strings.ToLower(m.Root) {
		return errors.New("root must be lowercase hex")
	}
	if m.ChunkSize == 0 || m.ChunkSize > MaxChunkSize {
		return fmt.Errorf("chunk_size must be in 1..%d", MaxChunkSize)
	}
	if err := ValidateMime(m.Mime); err != nil {
		return err
	}
	return ValidateName(m.Name)
}

// ValidateMime accepts "type/subtype" with a conservative character set.
func ValidateMime(s string) error {
	if s == "" || len(s) > MaxMimeLen {
		return fmt.Errorf("mime must be 1..%d chars", MaxMimeLen)
	}
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || slash == len(s)-1 || strings.Count(s, "/") != 1 {
		return errors.New("mime must look like type/subtype")
	}
	for _, r := range s {
		ok := r == '/' || r == '.' || r == '+' || r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("mime has invalid character %q (use lowercase)", r)
		}
	}
	return nil
}

// ValidateName accepts printable text without path separators or control characters.
func ValidateName(s string) error {
	if len(s) > MaxNameLen {
		return fmt.Errorf("name longer than %d bytes", MaxNameLen)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return errors.New("name contains a control character or path separator")
		}
	}
	return nil
}

// HashLeaf hashes one chunk.
func HashLeaf(chunk []byte) Hash {
	h := sha256.New()
	h.Write([]byte{0x00})
	h.Write(chunk)
	var out Hash
	h.Sum(out[:0])
	return out
}

func hashNode(l, r Hash) Hash {
	h := sha256.New()
	h.Write([]byte{0x01})
	h.Write(l[:])
	h.Write(r[:])
	var out Hash
	h.Sum(out[:0])
	return out
}

// largest power of two strictly less than n (n >= 2).
func split(n int) int { return 1 << (bits.Len(uint(n-1)) - 1) }

// TreeRoot is the Merkle tree hash of the given leaves (sha256("") if none).
func TreeRoot(leaves []Hash) Hash {
	switch len(leaves) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return leaves[0]
	}
	k := split(len(leaves))
	return hashNode(TreeRoot(leaves[:k]), TreeRoot(leaves[k:]))
}

// ContentRoot binds the tree root to the object size and chunk size.
func ContentRoot(tree Hash, size uint64, chunkSize uint32) string {
	var buf [1 + sha256.Size + 16]byte
	buf[0] = 0x02
	copy(buf[1:], tree[:])
	binary.BigEndian.PutUint64(buf[1+sha256.Size:], size)
	binary.BigEndian.PutUint64(buf[1+sha256.Size+8:], uint64(chunkSize))
	sum := sha256.Sum256(buf[:])
	return hex.EncodeToString(sum[:])
}

// ReadChunks streams r in chunkSize pieces, calling fn for each. The slice
// passed to fn is reused between calls. It returns the total bytes read.
func ReadChunks(r io.Reader, chunkSize int, fn func(index int, chunk []byte) error) (uint64, error) {
	if chunkSize <= 0 || chunkSize > MaxChunkSize {
		return 0, fmt.Errorf("chunk size must be in 1..%d", MaxChunkSize)
	}
	buf := make([]byte, chunkSize)
	var total uint64
	for i := 0; ; i++ {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			total += uint64(n)
			if ferr := fn(i, buf[:n]); ferr != nil {
				return total, ferr
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// Build streams r and returns its manifest and the leaf hashes (32 bytes per
// chunk), which are all that is needed to generate proofs later. chunkSize <= 0
// selects DefaultChunkSize.
func Build(r io.Reader, chunkSize int, mime, name string) (Manifest, []Hash, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	var leaves []Hash
	size, err := ReadChunks(r, chunkSize, func(_ int, c []byte) error {
		leaves = append(leaves, HashLeaf(c))
		return nil
	})
	if err != nil {
		return Manifest{}, nil, err
	}
	m := Manifest{
		Root:      ContentRoot(TreeRoot(leaves), size, uint32(chunkSize)),
		Size:      size,
		ChunkSize: uint32(chunkSize),
		Mime:      mime,
		Name:      name,
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, nil, err
	}
	return m, leaves, nil
}

// BuildBytes is Build over an in-memory slice.
func BuildBytes(data []byte, chunkSize int, mime, name string) (Manifest, []Hash, error) {
	return Build(bytes.NewReader(data), chunkSize, mime, name)
}

// Proof is a Merkle audit path (RFC 6962) for one chunk, leaf-to-root order.
type Proof [][]byte

// Prove returns the audit path for chunk index given all leaf hashes.
func Prove(leaves []Hash, index int) (Proof, error) {
	if index < 0 || index >= len(leaves) {
		return nil, fmt.Errorf("chunk index %d out of range [0,%d)", index, len(leaves))
	}
	var path Proof
	var rec func(m int, d []Hash)
	rec = func(m int, d []Hash) {
		if len(d) == 1 {
			return
		}
		k := split(len(d))
		if m < k {
			rec(m, d[:k])
			s := TreeRoot(d[k:])
			path = append(path, s[:])
		} else {
			rec(m-k, d[k:])
			s := TreeRoot(d[:k])
			path = append(path, s[:])
		}
	}
	rec(index, leaves)
	return path, nil
}

func rootFromPath(m, n int, leaf Hash, path Proof) (Hash, error) {
	if n == 1 {
		if len(path) != 0 {
			return Hash{}, errors.New("proof too long")
		}
		return leaf, nil
	}
	if len(path) == 0 {
		return Hash{}, errors.New("proof too short")
	}
	var sib Hash
	last := path[len(path)-1]
	if len(last) != sha256.Size {
		return Hash{}, errors.New("proof element is not 32 bytes")
	}
	copy(sib[:], last)
	k := split(n)
	if m < k {
		l, err := rootFromPath(m, k, leaf, path[:len(path)-1])
		if err != nil {
			return Hash{}, err
		}
		return hashNode(l, sib), nil
	}
	r, err := rootFromPath(m-k, n-k, leaf, path[:len(path)-1])
	if err != nil {
		return Hash{}, err
	}
	return hashNode(sib, r), nil
}

// VerifyProof checks that chunk is chunk number index of the object described
// by m (m.Root is the trusted value), using the audit path.
func VerifyProof(m Manifest, index int, chunk []byte, path Proof) error {
	n := m.NumChunks()
	if index < 0 || index >= n {
		return fmt.Errorf("chunk index %d out of range [0,%d)", index, n)
	}
	if len(chunk) != m.ChunkLen(index) {
		return fmt.Errorf("chunk %d has length %d, want %d", index, len(chunk), m.ChunkLen(index))
	}
	tree, err := rootFromPath(index, n, HashLeaf(chunk), path)
	if err != nil {
		return err
	}
	if ContentRoot(tree, m.Size, m.ChunkSize) != m.Root {
		return errors.New("proof does not match root")
	}
	return nil
}

// VerifyReader streams r and checks it hashes to m.Root with exactly m.Size bytes.
func VerifyReader(r io.Reader, m Manifest) error {
	var leaves []Hash
	size, err := ReadChunks(r, int(m.ChunkSize), func(_ int, c []byte) error {
		leaves = append(leaves, HashLeaf(c))
		return nil
	})
	if err != nil {
		return err
	}
	if size != m.Size {
		return fmt.Errorf("size mismatch: got %d want %d", size, m.Size)
	}
	if ContentRoot(TreeRoot(leaves), size, m.ChunkSize) != m.Root {
		return errors.New("content does not match root")
	}
	return nil
}
