# vault-ui: a small local upload UI

`tools/vault-ui` is a single Go binary with an embedded page (plain HTML, CSS and JavaScript, no frameworks, no
npm, no third-party requests). It lets you:

- pick a file, optionally **encrypt** it first, and upload it (commit manifest, open a deal pinned to the
  provider, send the bytes) in one click;
- list your files and the files shared with you, their deals (status and last proven epoch) and grants;
- **download** (and decrypt) a file;
- **grant** read access to an address and **revoke** a grant.

## It signs with a local key: keep it local

The UI uses a key from the local keyring (`--home`, `--key`) to sign transactions and read tokens. The private key
never leaves the process, but anything that can talk to the UI can spend your fees and share your files. So:

- it **only listens on loopback** and refuses to start on any other address (`--listen 127.0.0.1:8090`);
- every request must have a loopback `Host` (defeats DNS rebinding) and no foreign `Origin`, and every `/api`
  call needs a custom `X-Vault-UI: 1` header (a cross-origin web page cannot send it); there is no CORS;
- the page ships with a strict CSP (`default-src 'none'; script-src 'self'; ...`) and builds the DOM with
  `textContent` only, since file names on chain are attacker-controlled;
- **do not deploy it publicly and do not tunnel or port-forward it.** There is intentionally no Fly config for it.

## Run it against the Fly devnet

```bash
go build -o build/vault-ui ./tools/vault-ui

# a key with some uvault (see docs/FLY-DEVNET.md for funding from the validator)
vaultd keys add me --home ~/.vaultl1-ui

./build/vault-ui --home ~/.vaultl1-ui --key me \
  --node https://vaultl1-devnet.fly.dev \
  --provider https://vaultl1-provider.fly.dev
# then open http://127.0.0.1:8090
```

On Windows PowerShell: `go build -o build\vault-ui.exe .\tools\vault-ui`, then run it the same way.
Against a local chain and provider use the defaults (`--node http://127.0.0.1:1317 --provider http://127.0.0.1:3333`).

| Flag | Default | |
|---|---|---|
| `--listen` | `127.0.0.1:8090` | loopback only |
| `--node` | `http://127.0.0.1:1317` | chain REST |
| `--provider` | `http://127.0.0.1:3333` | storage provider; its address is read from `/status` |
| `--chain-id` | asked from the node | |
| `--home`, `--key` | `~/.vaultchain`, `alice` | keyring and signing key |
| `--fee` | `1000uvault` | per tx (the devnet's `MIN_FEE` is 1000uvault) |
| `--deal-duration` | `10000` | blocks; the deal price is the chain's per-byte rate |
| `--max-upload-mb` | `512` | uploads are buffered on local disk first |

If `VAULT_ADMIN_TOKEN` is set in the environment it is sent as `X-Admin-Token` (only needed for lab messages, which
the UI never sends).

## Behaviour worth knowing

- Upload is idempotent: re-uploading the same bytes reuses the manifest and an existing pending or open deal.
- A downloaded encrypted file is decrypted on the local machine into a temp file first, so a tampered download
  is rejected (HTTP 422) and never half-delivered. "download ciphertext" gives the raw `.vle` bytes.
- Granting an encrypted file wraps the file key to the grantee; the grantee needs to have sent a transaction once
  (so their public key is on chain), or you paste their public key into the second box that appears for encrypted files.
- Revoke removes the grant on chain; the provider stops serving the reader within a few seconds. A grantee's saved key
  or ciphertext keeps working: see [ENCRYPTION.md](ENCRYPTION.md).
- Names, sizes and grants are public on chain whatever you do here.
- Files are read through the provider you point `--provider` at. A file stored only at another provider cannot be
  downloaded through this UI.

`go test ./tools/vault-ui` runs the handlers against a real in-process chain (upload, encrypted upload, download,
decrypt, tamper, grant, shared-with-me, revoke, and the Host/Origin/header guards).
