package storage

import (
	"encoding/hex"
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

// EncryptionV1 marks an object whose bytes are client-side ciphertext in the
// pkg/vaultcrypt v1 stream format (XChaCha20-Poly1305). The chain never sees
// keys or plaintext; it only records the marker and the owner's key envelope.
const EncryptionV1 = "vle1"

// MaxKeyEnvelopeHex bounds a hex key envelope (a sealed 32-byte key is 210 hex chars).
const MaxKeyEnvelopeHex = 512

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
	// Encryption is "" (plaintext) or EncryptionV1. The root, size and chunks
	// then describe the ciphertext.
	Encryption string `json:"encryption,omitempty"`
	// KeyEnvelope is the file key sealed to the committer (hex); required
	// exactly when Encryption is set.
	KeyEnvelope string `json:"key_envelope,omitempty"`
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

	Encryption  string `json:"encryption,omitempty"`
	KeyEnvelope string `json:"key_envelope,omitempty"`
}

// Encrypted reports whether the object is client-side encrypted.
func (r ManifestRecord) Encrypted() bool { return r.Encryption != "" }

// IsEncrypted reports whether the committed manifest root is encrypted.
func (k Keeper) IsEncrypted(root string) bool {
	r, ok := k.Manifest(root)
	return ok && r.Encrypted()
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
	switch msg.Encryption {
	case "":
		if msg.KeyEnvelope != "" {
			return fmt.Errorf("key_envelope is only valid with encryption")
		}
	case EncryptionV1:
		if msg.KeyEnvelope == "" || len(msg.KeyEnvelope) > MaxKeyEnvelopeHex || len(msg.KeyEnvelope)%2 != 0 {
			return fmt.Errorf("encrypted manifests need a hex key_envelope of at most %d chars", MaxKeyEnvelopeHex)
		}
		if _, err := hex.DecodeString(msg.KeyEnvelope); err != nil {
			return fmt.Errorf("key_envelope must be hex")
		}
	default:
		return fmt.Errorf("unknown encryption %q", msg.Encryption)
	}
	if _, exists := k.Manifest(msg.Root); exists {
		return fmt.Errorf("manifest %s already committed", msg.Root)
	}
	kv.SetJSON(k.S, manifestKey(msg.Root), ManifestRecord{
		Root: msg.Root, Size: msg.Size, ChunkSize: msg.ChunkSize, Mime: msg.Mime, Name: msg.Name,
		Owner: msg.From, Height: height,
		Encryption: msg.Encryption, KeyEnvelope: msg.KeyEnvelope,
	})
	return nil
}
