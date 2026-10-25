// Package vaultcrypt is the client-side encryption used for private objects.
//
// A file is encrypted with a random 32-byte file key before it is chunked, so
// the content root, the manifest and everything a provider stores describe
// ciphertext. The file key is then sealed ("enveloped") to each account that
// may read the file, using that account's secp256k1 public key.
//
// Stream format (all integers big endian):
//
//	header  = "VLE1" || segmentSize(4) || noncePrefix(16)              (24 bytes)
//	segment = XChaCha20-Poly1305(key, nonce = noncePrefix || index(8),
//	          aad = header || finalFlag(1), plaintext of segmentSize bytes)
//
// Every segment but the last holds exactly segmentSize plaintext bytes; the
// last one (possibly empty, only for an empty file) is authenticated with
// finalFlag = 1. Each segment is therefore bound to the stream, its position
// and whether it ends the stream: flipped bits, reordered, duplicated,
// removed or appended segments and truncation all fail authentication.
//
// Key envelope (ECIES-style, 105 bytes):
//
//	ephemeralPub(33) || nonce(24) || XChaCha20-Poly1305(wrapKey, nonce, fileKey)
//
// wrapKey = HKDF-SHA256(ECDH(ephemeral, recipient), salt = ephemeralPub ||
// recipientPub, info = "vaultchain/key-envelope/v1|" + root + "|" + recipient
// address). Binding the envelope to the content root and the recipient stops
// it being replayed for another file or account.
//
// What this does not do: hide metadata (name, mime type, size and the
// access-control list are public on chain), provide forward secrecy, or take
// back a key that has already been delivered. See docs/ENCRYPTION.md.
package vaultcrypt

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	// KeySize is the file key length.
	KeySize = chacha20poly1305.KeySize
	// DefaultSegmentSize is the plaintext size of one segment.
	DefaultSegmentSize = 64 * 1024
	// MaxSegmentSize bounds the memory a reader needs per segment.
	MaxSegmentSize = 16 * 1024 * 1024

	// HeaderSize is the stream header length.
	HeaderSize = 4 + 4 + prefixSize
	// EnvelopeSize is the length of a sealed file key.
	EnvelopeSize = pubSize + chacha20poly1305.NonceSizeX + KeySize + chacha20poly1305.Overhead

	magic      = "VLE1"
	prefixSize = 16
	pubSize    = 33
)

// Errors callers may want to distinguish.
var (
	ErrNotEncrypted = errors.New("vaultcrypt: not an encrypted vaultchain stream")
	ErrCorrupt      = errors.New("vaultcrypt: ciphertext is corrupt, truncated or was tampered with (or the key is wrong)")
	ErrEnvelope     = errors.New("vaultcrypt: cannot open key envelope (wrong recipient, wrong object, or corrupt)")
)

// Key is a per-file symmetric key.
type Key [KeySize]byte

// NewKey returns a random file key.
func NewKey() (Key, error) {
	var k Key
	_, err := io.ReadFull(rand.Reader, k[:])
	return k, err
}

// CiphertextSize is the exact encrypted length of n plaintext bytes.
func CiphertextSize(n uint64, segmentSize int) uint64 {
	seg := uint64(segmentSize)
	segs := n/seg + 1
	if n > 0 && n%seg == 0 {
		segs = n / seg // a full final segment is never followed by an empty one
	}
	return HeaderSize + n + segs*chacha20poly1305.Overhead
}

func segNonce(prefix []byte, index uint64) []byte {
	n := make([]byte, chacha20poly1305.NonceSizeX)
	copy(n, prefix)
	binary.BigEndian.PutUint64(n[prefixSize:], index)
	return n
}

func segAAD(header []byte, final bool) []byte {
	aad := make([]byte, len(header)+1)
	copy(aad, header)
	if final {
		aad[len(header)] = 1
	}
	return aad
}

// Encrypt streams src to dst as ciphertext under key and returns the number
// of ciphertext bytes written. segmentSize <= 0 selects DefaultSegmentSize.
func Encrypt(dst io.Writer, src io.Reader, key Key, segmentSize int) (int64, error) {
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	if segmentSize > MaxSegmentSize {
		return 0, fmt.Errorf("vaultcrypt: segment size %d exceeds %d", segmentSize, MaxSegmentSize)
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return 0, err
	}
	header := make([]byte, HeaderSize)
	copy(header, magic)
	binary.BigEndian.PutUint32(header[4:], uint32(segmentSize))
	prefix := header[8:]
	if _, err := io.ReadFull(rand.Reader, prefix); err != nil {
		return 0, err
	}
	var written int64
	n, err := dst.Write(header)
	written += int64(n)
	if err != nil {
		return written, err
	}

	// Read one segment ahead so the last segment is known when it is sealed.
	cur := make([]byte, segmentSize)
	next := make([]byte, segmentSize)
	curN, err := io.ReadFull(src, cur)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return written, err
	}
	for index := uint64(0); ; index++ {
		final := curN < segmentSize
		var nextN int
		if !final {
			nextN, err = io.ReadFull(src, next)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return written, err
			}
			if nextN == 0 {
				final = true // the stream ended exactly on a segment boundary
			}
		}
		sealed := aead.Seal(nil, segNonce(prefix, index), cur[:curN], segAAD(header, final))
		n, err := dst.Write(sealed)
		written += int64(n)
		if err != nil {
			return written, err
		}
		if final {
			return written, nil
		}
		cur, next = next, cur
		curN = nextN
	}
}

// Decrypt authenticates and decrypts a stream produced by Encrypt. Plaintext
// is written to dst segment by segment as it verifies, so on error dst may
// already hold a verified prefix: callers that need all-or-nothing should
// decrypt to a temporary file and move it into place on success (Decrypt
// returns ErrCorrupt for any damage, including a missing tail).
func Decrypt(dst io.Writer, src io.Reader, key Key) (int64, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return 0, err
	}
	br := bufio.NewReaderSize(src, 1<<16)
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(br, header); err != nil || string(header[:4]) != magic {
		return 0, ErrNotEncrypted
	}
	segmentSize := int(binary.BigEndian.Uint32(header[4:]))
	if segmentSize <= 0 || segmentSize > MaxSegmentSize {
		return 0, ErrNotEncrypted
	}
	prefix := header[8:]
	sealedMax := segmentSize + chacha20poly1305.Overhead
	buf := make([]byte, sealedMax)
	var written int64
	for index := uint64(0); ; index++ {
		n, err := io.ReadFull(br, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return written, err
		}
		if n < chacha20poly1305.Overhead {
			return written, ErrCorrupt // missing final segment
		}
		// A segment is final if it is short, or if nothing follows it.
		final := n < sealedMax
		if !final {
			if _, perr := br.Peek(1); perr == io.EOF {
				final = true
			}
		}
		pt, err := aead.Open(nil, segNonce(prefix, index), buf[:n], segAAD(header, final))
		if err != nil {
			return written, ErrCorrupt
		}
		m, err := dst.Write(pt)
		written += int64(m)
		if err != nil {
			return written, err
		}
		if final {
			return written, nil
		}
	}
}

// parsePub validates a 33-byte compressed secp256k1 public key.
func parsePub(b []byte) (*secp256k1.PublicKey, error) {
	if len(b) != pubSize {
		return nil, fmt.Errorf("vaultcrypt: public key must be %d bytes", pubSize)
	}
	return secp256k1.ParsePubKey(b)
}

func wrapKey(secret, ephPub, recipientPub []byte, root, recipientAddr string) (Key, error) {
	var k Key
	salt := append(append([]byte(nil), ephPub...), recipientPub...)
	info := []byte("vaultchain/key-envelope/v1|" + root + "|" + recipientAddr)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), k[:]); err != nil {
		return k, err
	}
	return k, nil
}

// SealKey seals fileKey to the account that owns recipientPub (33-byte
// compressed secp256k1), for the object with content root root. recipientAddr
// is that account's address and only binds the envelope; it is not checked
// against the public key here.
func SealKey(fileKey Key, recipientPub []byte, root, recipientAddr string) ([]byte, error) {
	rp, err := parsePub(recipientPub)
	if err != nil {
		return nil, err
	}
	eph, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	ephPub := eph.PubKey().SerializeCompressed()
	secret := secp256k1.GenerateSharedSecret(eph, rp)
	wk, err := wrapKey(secret, ephPub, recipientPub, root, recipientAddr)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(wk[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append(append([]byte(nil), ephPub...), nonce...)
	return aead.Seal(out, nonce, fileKey[:], ephPub), nil
}

// OpenKey recovers the file key from an envelope using the recipient's
// 32-byte secp256k1 private key.
func OpenKey(envelope, recipientPriv []byte, root, recipientAddr string) (Key, error) {
	var zero Key
	if len(envelope) != EnvelopeSize || len(recipientPriv) != 32 {
		return zero, ErrEnvelope
	}
	ephPub := envelope[:pubSize]
	ep, err := parsePub(ephPub)
	if err != nil {
		return zero, ErrEnvelope
	}
	priv := secp256k1.PrivKeyFromBytes(recipientPriv)
	secret := secp256k1.GenerateSharedSecret(priv, ep)
	wk, err := wrapKey(secret, ephPub, priv.PubKey().SerializeCompressed(), root, recipientAddr)
	if err != nil {
		return zero, err
	}
	aead, err := chacha20poly1305.NewX(wk[:])
	if err != nil {
		return zero, err
	}
	nonce := envelope[pubSize : pubSize+chacha20poly1305.NonceSizeX]
	pt, err := aead.Open(nil, nonce, envelope[pubSize+chacha20poly1305.NonceSizeX:], ephPub)
	if err != nil || len(pt) != KeySize {
		return zero, ErrEnvelope
	}
	copy(zero[:], pt)
	return zero, nil
}
