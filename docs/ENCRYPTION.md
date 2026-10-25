# Private files: revocation and client-side encryption

Two separate mechanisms. Use the first for "who may fetch this from a provider" and add the second when the
provider (and anyone who can read its disk or network traffic) must not be able to read the contents.

## 1. Revoking access

```bash
# revoke everything a grantee holds on an object (or "*" for the public grant)
vaultd tx access revoke <root> <grantee|*> --from alice
# revoke exactly one grant (ids are shown by GET /vaultchain/access/grants?cid=<root>)
vaultd tx access revoke <root> --grant-id 7 --from alice
```

- `MsgRevokeAccess` (`access/RevokeAccess`) is signed by the **object owner** only. It names a `cid` and a
  `grantee` and/or a `grant_id` (a `grant_id` belongs to that cid; if both are given they must both match). A
  revoke that matches nothing fails, so a typo cannot look like success. Grant ids are never reused.
- **Enforcement is at the provider, at serve time.** A read token only proves *who the reader is*. On every read
  the provider asks the chain, live, whether that reader is the owner, holds a non-expired grant (or the object has
  a public grant), or is a replica provider. Only *positive* answers are cached, for at most `accessCacheTTL`
  (5 s). So after a revoke is included in a block, a still-valid read token stops working within about 5 s, and
  the old grant's expiry no longer matters. Denials are never cached, so a new grant works immediately.
- Limits: a stream that already passed the check is not cut off mid-response (it ends when the body ends);
  anything the reader already downloaded stays downloaded; a revoked public grant does not recall copies
  made while it was public. For encrypted objects see below: revoking does not take back a key that was handed out.

## 2. Client-side encryption

```bash
# encrypt, commit the manifest over the ciphertext, then upload the ciphertext as usual
vaultd tx storage commit-manifest ./report.pdf --encrypt --cipher-out ./report.pdf.vle --from alice
#   stderr: encrypted: root=<root> size=<n> ciphertext=./report.pdf.vle
vaultd tx storage open-deal market <root> 0 <provider-addr> 10000 uvault --from alice
curl -X POST --data-binary @report.pdf.vle https://<provider>/v1/objects/<root>

# share with bob: the file key is wrapped to bob's public key and stored in the grant
vaultd tx access grant <root> <bob-addr> read 0 --from alice      # bob must have sent at least one tx
vaultd tx access grant <root> <bob-addr> read 0 --grantee-pubkey <hex> --from alice   # or pass his key

# download the ciphertext with a read token, then decrypt with your own key
vaultd read-token <root> --provider <provider-addr> --from bob > tok
curl -H "Authorization: Vault $(cat tok)" https://<provider>/v1/objects/<root> -o got.vle
vaultd decrypt <root> --from bob --node https://<node> --in got.vle --out report.pdf

vaultd keys show bob --pubkey      # prints the hex public key
```

### How it works

1. A random 256-bit **file key** is generated per file.
2. The file is encrypted with **XChaCha20-Poly1305** in 64 KiB segments (`pkg/vaultcrypt`). The stream is a 24-byte
   header (`"VLE1"`, segment size, a random 16-byte nonce prefix) followed by sealed segments, each with a 16-byte tag.
   Segment nonce = prefix || segment index; the associated data is the header plus a "final segment" flag. So a
   modified, reordered, duplicated, dropped, appended or truncated segment fails authentication, and the decryptor
   writes nothing unless every segment verifies (`vaultd decrypt` writes to a temp file and renames).
3. Chunking, Merkle tree and the content **root are computed over the ciphertext**; the manifest `size` is the
   ciphertext size. Providers, proofs of storage and repair work unchanged and never see plaintext.
4. The manifest carries `encryption: "vle1"` and the owner's **key envelope**. Each grant on an encrypted object
   carries an envelope for that grantee.
5. A **key envelope** (105 bytes) is `ephemeralPub(33) || nonce(24) || AEAD(file key)`. The wrap key is
   HKDF-SHA256 over the ECDH secret between a fresh ephemeral secp256k1 key and the recipient's account key, with
   the object root and the recipient's address in the derivation, so an envelope cannot be replayed for another
   object or person. Only the holder of the recipient's private key can open it.
6. The chain checks shape only: an encrypted manifest must carry an envelope, an envelope on an unencrypted
   object is rejected, grants on an encrypted object must carry an envelope and cannot be to `*` (public).
7. Recipients' public keys come from an **on-chain pubkey registry**: the first time an account sends a valid
   transaction its public key is recorded (`GET /vaultchain/auth/pubkeys/<addr>`, checked against the address).
   An account that has never transacted can still be granted if you pass `--grantee-pubkey`.

### Threat model

Protects against: the provider operator, other providers (repair copies), disk theft, network observers, and anyone
who fetches the ciphertext, learning the contents. Tampering with stored or served bytes is detected on decrypt.

Does **not** hide, and you should assume is public:
- **Metadata**: name, MIME type, size (to within 16 bytes per 64 KiB), the root, the owner, deal and provider
  details, when you stored it, and **who has been granted access** (addresses and expiries).
- Whether two people stored the same plaintext is not revealed (random per-file key), but nothing hides that a file exists.

Honest limits:
- **Key loss is data loss.** Your file key exists only as an envelope sealed to your account key. Lose the
  private key (keyring file, mnemonic) and nobody, including the chain and providers, can decrypt your files.
  Back up the mnemonic. There is no recovery.
- **Revocation cannot un-share a key.** `revoke` removes the grant and its envelope from chain state and the
  provider stops serving the revoked reader. But a grantee who already opened the envelope holds the file key, and
  if they also kept (or can still obtain) the ciphertext, they can still decrypt it. Chain history keeps old blocks, and
  anyone who copied the grant envelope keeps what they copied. Real revocation of an already shared secret means
  **re-encrypting under a new key**: encrypt again (new root), upload, and grant only to the people who should still
  have access. The old root should then be revoked and its deals left to expire.
- **No forward secrecy** against a compromised account key: whoever later gets your private key can open every
  envelope sealed to it that is still readable on chain.
- **Whole-file decrypt only.** The provider supports `Range` on ciphertext, but this version decrypts whole files; no
  seekable or streaming-playback decrypt.
- **Authenticity of the owner is not proven**: anyone can commit the same ciphertext first and own the root; only
  the committer's envelope is on the manifest. Grantees should only trust roots they were told about.
- A malicious *owner* can share garbage; a grantee finds out when decryption fails. A malicious *grantee* can leak the
  plaintext. The provider can still **delete** or withhold ciphertext (the proof-of-storage deals and slashing remain
  the defence).
- It is new code, not audited. The primitives are standard (`golang.org/x/crypto` XChaCha20-Poly1305 and HKDF, secp256k1
  ECDH); the construction around them is ours.

### Message formats and compatibility

All additions are optional fields (`omitempty`) or new message types, so transactions that do not use them are
byte-identical to before:

| Change | Where |
|---|---|
| `key_envelope` (hex) on grants | `access/GrantAccess`, `GET /vaultchain/access/grants` |
| `encryption`, `key_envelope` on manifests | `storage/CommitManifest`, manifests REST |
| new message `access/RevokeAccess` | `x/access` |
| new consensus state `auth/pubkey/<addr>` | written when an account's first valid tx executes |

The pubkey registry changes the app hash, so **a chain created before this change must be reset** (new genesis), and
validators on mixed versions would diverge on a rolling upgrade: upgrade all of them together on a fresh chain.
