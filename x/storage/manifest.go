package storage

import (
	"fmt"

	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/pkg/chunk"
)

const MsgTypeCommitManifest = "storage/CommitManifest"

// MinChunkSize bounds the per-object chunk count on chain (max_object_bytes /
// MinChunkSize), which in turn bounds what a provider has to index.
const MinChunkSize = 4 * 1024

// MaxChunkSize keeps a challenge response (one chunk, base64 in a tx) under
// the 1 MiB request cap.
const MaxChunkSize = 512 * 1024

var prefixManifest = []byte("storage/manifest/")

// MsgCommitManifest registers an object's content ID and shape on chain. The
// signer becomes the owner of the root.
type MsgCommitManifest struct {
	Type      string `json:"type"`
	From      string `json:"-"`
	Root      string `json:"root"`
	Size      uint64 `json:"size"`
	ChunkSize uint32 `json:"chunk_size"`
	Mime      string `json:"mime"`
	Name      string `json:"name"`
}

// ManifestRecord is a committed manifest plus ownership.
type ManifestRecord struct {
	Root      string `json:"root"`
	Size      uint64 `json:"size"`
	ChunkSize uint32 `json:"chunk_size"`
	Mime      string `json:"mime"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Height    int64  `json:"height"`
}

// Chunk converts to the pkg/chunk manifest.
func (r ManifestRecord) Chunk() chunk.Manifest {
	return chunk.Manifest{Root: r.Root, Size: r.Size, ChunkSize: r.ChunkSize, Mime: r.Mime, Name: r.Name}
}

func manifestKey(root string) []byte { return append(append([]byte(nil), prefixManifest...), root...) }

func (k Keeper) Manifest(root string) (ManifestRecord, bool) {
	var r ManifestRecord
	ok := kv.GetJSON(k.S, manifestKey(root), &r)
	return r, ok
}

func (k Keeper) Manifests() []ManifestRecord {
	out := []ManifestRecord{}
	kv.IterateJSON(k.S, prefixManifest, func(_ []byte, r ManifestRecord) bool { out = append(out, r); return true })
	return out
}

// CommitManifest validates caps and records the manifest. The first committer
// of a root owns it. (A root can only be committed by someone who knows the
// content's hash tree, but anyone holding the content can claim it first;
// ownership is therefore "first to register", not proof of authorship.)
func (k Keeper) CommitManifest(msg MsgCommitManifest, height int64) error {
	if msg.From == "" {
		return fmt.Errorf("missing signer")
	}
	m := chunk.Manifest{Root: msg.Root, Size: msg.Size, ChunkSize: msg.ChunkSize, Mime: msg.Mime, Name: msg.Name}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	params := k.Params()
	if msg.Size == 0 {
		return fmt.Errorf("size must be > 0")
	}
	if params.MaxObjectBytes > 0 && msg.Size > params.MaxObjectBytes {
		return fmt.Errorf("size %d exceeds max_object_bytes %d", msg.Size, params.MaxObjectBytes)
	}
	if msg.ChunkSize < MinChunkSize {
		return fmt.Errorf("chunk_size must be >= %d", MinChunkSize)
	}
	if msg.ChunkSize > MaxChunkSize {
		return fmt.Errorf("chunk_size must be <= %d (a proof carries a whole chunk)", MaxChunkSize)
	}
	if _, exists := k.Manifest(msg.Root); exists {
		return fmt.Errorf("manifest %s already committed", msg.Root)
	}
	kv.SetJSON(k.S, manifestKey(msg.Root), ManifestRecord{
		Root: msg.Root, Size: msg.Size, ChunkSize: msg.ChunkSize, Mime: msg.Mime, Name: msg.Name,
		Owner: msg.From, Height: height,
	})
	return nil
}
