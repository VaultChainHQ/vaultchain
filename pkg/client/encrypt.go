package client

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	vcrypto "github.com/VaultChainHQ/vaultchain/crypto"
	"github.com/VaultChainHQ/vaultchain/pkg/chunk"
	"github.com/VaultChainHQ/vaultchain/pkg/vaultcrypt"
	"github.com/VaultChainHQ/vaultchain/x/storage"
)

// ErrPlaintext is returned when a key is requested for an unencrypted object.
var ErrPlaintext = errors.New("object is not encrypted")

// EncryptToFile encrypts plain into the file cipherPath under a fresh file
// key, hashes the ciphertext into a manifest, and returns the commit message
// whose key envelope seals the file key to owner. The signer of that message
// must be owner. The caller uploads cipherPath, not the original file.
func EncryptToFile(plain io.Reader, cipherPath string, owner vcrypto.KeyRecord, mime, name string, chunkSize int) (storage.MsgCommitManifest, error) {
	var msg storage.MsgCommitManifest
	key, err := vaultcrypt.NewKey()
	if err != nil {
		return msg, err
	}
	f, err := os.OpenFile(cipherPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return msg, err
	}
	defer f.Close()
	if _, err := vaultcrypt.Encrypt(f, plain, key, 0); err != nil {
		return msg, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return msg, err
	}
	m, _, err := chunk.Build(f, chunkSize, mime, name)
	if err != nil {
		return msg, err
	}
	pub, err := CheckPubKey(owner.Address, owner.PubKeyHex)
	if err != nil {
		return msg, err
	}
	env, err := vaultcrypt.SealKey(key, pub, m.Root, owner.Address)
	if err != nil {
		return msg, err
	}
	return storage.MsgCommitManifest{
		Type: storage.MsgTypeCommitManifest, Root: m.Root, Size: m.Size, ChunkSize: m.ChunkSize,
		Mime: m.Mime, Name: m.Name, Encryption: storage.EncryptionV1, KeyEnvelope: hex.EncodeToString(env),
	}, nil
}

func privBytes(rec vcrypto.KeyRecord) ([]byte, error) {
	b, err := hex.DecodeString(rec.PrivKeyHex)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("key %q has no usable private key", rec.Name)
	}
	return b, nil
}

// FileKey recovers the file key of the encrypted object root for the account
// rec: through the owner's envelope in the manifest, or through the newest
// grant that carries an envelope for rec. It fails if neither exists, e.g.
// after the owner revoked the grant.
func (c *Client) FileKey(root string, rec vcrypto.KeyRecord) (vaultcrypt.Key, error) {
	var zero vaultcrypt.Key
	m, err := c.Manifest(root)
	if err != nil {
		return zero, err
	}
	if !m.Encrypted() {
		return zero, ErrPlaintext
	}
	envHex := ""
	if m.Owner == rec.Address {
		envHex = m.KeyEnvelope
	} else {
		grants, err := c.Grants(root)
		if err != nil {
			return zero, err
		}
		for _, g := range grants { // ascending id: keep the newest
			if g.Grantee == rec.Address && g.KeyEnvelope != "" {
				envHex = g.KeyEnvelope
			}
		}
	}
	if envHex == "" {
		return zero, fmt.Errorf("no key envelope for %s on %s: not the owner and no grant carries one for this account", rec.Address, root)
	}
	env, err := hex.DecodeString(envHex)
	if err != nil {
		return zero, err
	}
	priv, err := privBytes(rec)
	if err != nil {
		return zero, err
	}
	return vaultcrypt.OpenKey(env, priv, root, rec.Address)
}

// GrantEnvelope re-seals the file key of root (which owner must own) to
// grantee and returns the hex envelope for MsgGrantAccess. granteePubHex may
// be empty, in which case the grantee's key is looked up in the on-chain key
// registry (which only knows accounts that have already sent a transaction).
func (c *Client) GrantEnvelope(root string, owner vcrypto.KeyRecord, grantee, granteePubHex string) (string, error) {
	key, err := c.FileKey(root, owner)
	if err != nil {
		return "", err
	}
	var pub []byte
	if granteePubHex != "" {
		pub, err = CheckPubKey(grantee, granteePubHex)
	} else {
		pub, err = c.PubKey(grantee)
		if IsNotFound(err) {
			err = fmt.Errorf("%s has not sent a transaction yet, so its public key is not on chain: pass its public key explicitly", grantee)
		}
	}
	if err != nil {
		return "", err
	}
	env, err := vaultcrypt.SealKey(key, pub, root, grantee)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(env), nil
}

// DecryptStream decrypts ciphertext from src into dst with the key rec is
// entitled to for root. dst receives verified plaintext only; on error it may
// hold a prefix, so write to a temporary file.
func (c *Client) DecryptStream(dst io.Writer, src io.Reader, root string, rec vcrypto.KeyRecord) error {
	key, err := c.FileKey(root, rec)
	if err != nil {
		return err
	}
	_, err = vaultcrypt.Decrypt(dst, src, key)
	return err
}

// DecryptBytes is DecryptStream for in-memory data.
func (c *Client) DecryptBytes(ct []byte, root string, rec vcrypto.KeyRecord) ([]byte, error) {
	var out bytes.Buffer
	if err := c.DecryptStream(&out, bytes.NewReader(ct), root, rec); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
