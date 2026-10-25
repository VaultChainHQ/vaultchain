# Vaultchain

Clean-room decentralized storage L1 (local single-validator engine for Phase 0 lab).

| | |
|---|---|
| Binary | `vaultd` |
| Module | `github.com/VaultChainHQ/vaultchain` |
| Bech32 | `vault` |
| Denom | `uvault` |
| Local chain-id | `vault-local-1` |
| License | MIT |

Inspired by public lessons from storage networks — **not** a fork of any existing chain.

## Repositories

| Repository | What it is |
| --- | --- |
| [VaultChainHQ/vaultchain](https://github.com/VaultChainHQ/vaultchain) | this repo: the chain (`vaultd`), storage provider, CLI and docs |
| [VaultChainHQ/vaultchain-explorer](https://github.com/VaultChainHQ/vaultchain-explorer) | read-only Next.js explorer for the devnet (providers, deals, files, balances) |
| [VaultChainHQ/vaultchain-upload](https://github.com/VaultChainHQ/vaultchain-upload) | Next.js site that signs, chunks, encrypts and uploads in the browser (keys never leave it) |
| [VaultChainHQ/vaultchain-web](https://github.com/VaultChainHQ/vaultchain-web) | site, docs hub and a local-first profile vault with passphrase-encrypted keys ([live](https://vaultchain-web.vercel.app)) |

`tools/tsvectors` generates the cross-language test vectors (addresses, signatures, signed txs, chunk roots, vaultcrypt ciphertexts, key envelopes, read tokens) that vaultchain-upload checks its TypeScript port against, and `go run ./tools/tsvectors -verify <file>` checks TypeScript output against the Go implementation.

## Storage classes

1. **Personal** — encrypted personal capacity plans (`buy-capacity`)
2. **Market** — per-object deals (`open-deal` class=`market`)
3. **Archive** — prepaid long-horizon deals (`open-deal` class=`archive`)

## IBC: payments only

Vaultchain accepts **ICS-20 payment denoms** only. Storage infrastructure (providers, proofs, deals, bytes) stays native.

See [`docs/IBC-PAYMENTS-ONLY.md`](docs/IBC-PAYMENTS-ONLY.md), Phase 0 spec [`docs/PHASE0-SPEC.md`](docs/PHASE0-SPEC.md), and foundation tradeoff [`docs/FOUNDATION-SOVEREIGN-VS-COSMOS.md`](docs/FOUNDATION-SOVEREIGN-VS-COSMOS.md) ([PDF](docs/FOUNDATION-SOVEREIGN-VS-COSMOS.pdf)).

Policy REST: `GET /vaultchain/ibc/policy`

## Build (WSL / Linux)

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
cd /path/to/vaultchain
go mod tidy
go build -o build/vaultd ./cmd/vaultd
```

## Init local chain

```bash
./scripts/init-vault-local.sh
# or manually:
vaultd init local-validator --home ~/.vaultchain --chain-id vault-local-1 --lab-mode
vaultd keys add validator --home ~/.vaultchain
vaultd genesis add-genesis-account $(vaultd keys show validator --home ~/.vaultchain) 100000000000000uvault --home ~/.vaultchain
```

## Start

```bash
vaultd start --home ~/.vaultchain
# optional overrides:
# vaultd start --home ~/.vaultchain --rpc-addr 127.0.0.1:26657 --api-addr 127.0.0.1:1317
```

| Service | Address |
|---------|---------|
| RPC | `http://127.0.0.1:26657` |
| REST | `http://127.0.0.1:1317` |

## Multi-validator consensus (CometBFT)

`vaultd start` defaults to the single-node timer engine (`--consensus timer`), which is fine for a local lab. With
`--consensus comet` the same state machine runs behind CometBFT (v0.39, in-process, ABCI): several validators agree on
blocks and tx order, every node serves REST from its own committed state, and the app hash is the real hash of that
state (identical on all nodes at a given height).

- `CheckTx` runs the admission checks (signature, chain id, nonce including this node's pending txs, the node-local
  `--min-fee`, fee affordability); the HTTP rate limit and admin-token gate still sit in front of REST.
- `FinalizeBlock`/`Commit` use the same deterministic apply logic as the timer engine. Block time and proposer come
  from the CometBFT header (block 1's time equals genesis time, so times are clamped to strictly increase by 1 ms).
  Tx fees go to the proposing validator's account, which genesis maps from its consensus address.
- Provider proof/challenge/repair logic is unchanged: it reads the committed height, app hash and time.
- Files per node: `config/cometgenesis.json` (CometBFT genesis; its `app_state` is the vault genesis),
  `config/priv_validator_key.json`, `config/node_key.json`, `cometdata/` (CometBFT stores) and `data/state.db` (app
  state). The p2p port is 26656; CometBFT RPC stays on loopback (`--comet-rpc-addr`). `GET /cosmos/base/tendermint/v1beta1/node_info`
  gains a `consensus` object (engine, node id, peers, validator count, catching up).

```bash
# 3 validators on this machine (no docker): REST on :1317 :1318 :1319
go build -o build/vaultd ./cmd/vaultd
scripts/comet-local.sh start      # also: status | stop [N] | start [N] | clean
# or with docker
docker compose -f deploy/docker-compose.comet.yml up --build
# or generate the files for your own hosts (contains private keys: keep it out of git)
vaultd testnet --validators 3 --chain-id vault-devnet-2 --out build/testnet --hostname-pattern 'node%d.example'
```

Validators have equal power (10) fixed at genesis: there is no staking, validator-set change or validator slashing yet.
CometBFT commits only with strictly more than 2/3 of the power, so 3 equal validators tolerate no failure (stop one and
the chain halts until it returns); 4 tolerate one. The default Fly devnet is one node plus one provider (`vaultl1-devnet`, `vaultl1-provider`). A 3-validator CometBFT
network on Fly (`fly.validator.toml`, `deploy/Dockerfile.validator`, `vaultl1-val-1/2/3`) was deployed and verified,
then retired to save cost: it is available but not currently deployed (redeploy steps in docs/FLY-DEVNET.md; or run it
locally with `deploy/docker-compose.comet.yml` / `scripts/comet-local.sh`).
The chain id is part of every signed tx; lab mode is a genesis flag and is off in `vaultd testnet` unless `--lab-mode`.

## Useful commands

```bash
# Status / params
vaultd query status
vaultd query storage-params
vaultd query ibc-policy

# Register provider + personal plan + market deal
vaultd tx storage register-provider http://127.0.0.1:8080 1099511627776 1000000uvault --from alice --home ~/.vaultchain
vaultd tx storage buy-capacity 1073741824 10000 uvault --from alice --home ~/.vaultchain
# Objects are content-addressed: commit a manifest for a file first, then open a deal on its root
vaultd tx storage commit-manifest ./photo.jpg --from alice --home ~/.vaultchain    # prints the tx; `--dry-run` shows the root
vaultd tx storage open-deal market <root> 0 <provider-addr> 5000 uvault --from alice --home ~/.vaultchain   # size 0 = manifest size

# Private files (docs/ENCRYPTION.md): encrypt client-side, share a wrapped key, revoke
vaultd tx storage commit-manifest ./report.pdf --encrypt --cipher-out ./report.pdf.vle --from alice --home ~/.vaultchain
vaultd tx access grant <root> <bob-addr> read 0 --from alice --home ~/.vaultchain     # wraps the file key for bob
vaultd decrypt <root> --from bob --node http://127.0.0.1:1317 --in got.vle --out report.pdf --home ~/.vaultchain
vaultd tx access revoke <root> <bob-addr> --from alice --home ~/.vaultchain          # provider stops serving bob within ~5 s

# Lab-only fake IBC voucher (requires the chain to be initialised with `--lab-mode`)
vaultd tx ibc-lab mint-voucher $(vaultd keys show alice --home ~/.vaultchain) ibc/ATOM 1000000 --from alice --home ~/.vaultchain
```

## Transactions, state and upgrade notes

- Every tx is signed (secp256k1, `chain_id`, per-account `nonce`, `fee`); the sender is derived from the
  signature. `vaultd tx ... --from <key> [--fee 10uvault]` signs for you.
- `vaultd init --lab-mode` enables lab-only messages (`ibc-lab mint-voucher`). It is fixed at genesis; a chain
  initialised without it rejects them.
- Block execution is deterministic: block time and proposer are inputs, not read from the clock, and each tx's
  messages run on a cached branch that is committed only if all succeed (a failed tx costs its fee and nonce only).
- State lives in `<home>/data/state.db` (bbolt), one key per account / provider / deal / ..., so a block writes only
  what it changed. Tx records are kept for the last 1000 blocks (`GET /vaultchain/txs/<hash>`); headers are kept forever.
- Details: [`docs/STATE-MACHINE.md`](docs/STATE-MACHINE.md).

**Account public keys are now recorded on chain** (`auth/pubkey/<addr>`, written when an account's first valid tx
runs; needed to wrap file keys for grantees). It changes the app hash, so a chain created before it must be reset, and
validators must all run the same version.

**Upgrading from earlier builds: a fresh devnet is required.** The old single-blob `vaultchain.db` and the old
unsigned txs are not migrated. `vaultd start` refuses to run on a legacy `data/vaultchain.db` and tells you to stop the
node, delete `<home>/data`, and re-run `vaultd init` (`scripts/init-vault-local.sh` does this for you).

## Layout

```
cmd/vaultd/            CLI + daemon entry
node/                  local engine (blocks, mempool, RPC, REST) + deterministic App state machine
kv/                    per-key state store (bbolt + in-memory), branch cache, app hash
x/storage/             providers, capacity, manifests, deals, proofs, denom whitelist
x/access/              AccessGrant store
x/bank/                balances + escrow
x/ibc/                 payments-only policy + lab voucher mint
crypto/                test keyring (secp256k1)
pkg/vaultcrypt/        client-side file encryption (XChaCha20-Poly1305 segments) + per-recipient key envelopes
pkg/client/            Go client for the REST API: sign + send txs, wait, queries, encrypt/decrypt helpers
pkg/chunk/             chunking, Merkle tree, content root, inclusion proofs
tools/vault-provider/  object store: verified chunked storage, Range reads, deal-gated writes
tools/vault-lab/       click-to-test lab UI (:3456)
tools/vault-ui/        small local upload UI: upload (optionally encrypted), download, grant, revoke (127.0.0.1:8090)
docs/                  Phase 0 + IBC policy
scripts/               init + start helpers
```

## Provider + Lab

```bash
# terminal 1 — chain
./scripts/init-vault-local.sh
./build/vaultd start --home ~/.vaultchain

# terminal 2 — provider (:3333)
chmod +x scripts/*.sh
./scripts/start-vault-provider.sh

# terminal 3 — lab UI (:3456)
./scripts/start-vault-lab.sh
# The lab binds 127.0.0.1:3456 by default because it signs with local keys. Under WSL2 localhost
# forwarding normally reaches it from Windows; only if you must, set VAULT_LAB_LISTEN=0.0.0.0:3456.
```

Lab flows: register provider, buy personal capacity, upload a file (hash into a manifest, commit it, open a deal, stream the bytes to the provider; the provider then proves it holds them), IBC policy / mint voucher. The lab has no CORS and refuses cross-origin POSTs: use the UI it serves.

## Objects and the provider API

An object is a file split into chunks (256 KiB by default, `pkg/chunk`). Its **root** commits to the Merkle tree of
chunk hashes plus size and chunk size, so the root alone pins the exact bytes.

1. `commit-manifest` publishes `{root, size, chunk_size, mime, name}` on chain. The first committer owns the root
   (note: anyone holding the file can commit it first, so ownership is not proof of authorship).
2. `open-deal` takes that root (size 0 = manifest size) and only the manifest owner may open it. The chain assigns
   `replication` providers (see "Replication"); the deal is `pending` until they accept.
3. The provider accepts bytes only for a root that has a pending or open deal in which it holds an assigned or active
   replica (`-address` is required; the old `-skip-chain-check` is gone) and re-verifies the whole object against the
   root before keeping it. Once it holds the bytes its prover sends `MsgAcceptDeal` automatically.

| Provider route | Purpose |
|----------------|---------|
| `POST\|PUT /v1/objects/{root}` | stream a whole object (verified, then installed atomically) |
| `GET\|HEAD /v1/objects/{root}` | download, supports `Range`; each chunk is re-hashed before it is served; **needs read access** (see below) |
| `GET /v1/objects/{root}/status` | stored / complete (open: reveals only chunk coverage of a root you already know) |
| `PUT /v1/chunks/{root}/{i}` | upload one chunk with its Merkle proof in `X-Vault-Proof` |
| `GET\|HEAD /v1/chunks/{root}/{i}` | one chunk (+ `X-Vault-Proof` once the object is complete); needs read access |
| `/status`, `/version` | node info |

Responses use the manifest's Content-Type with `nosniff` and a locked-down CSP, and are `attachment` unless the type
is a safe inline media type. CORS is off unless you pass `-allow-origin https://app.example` (repeatable; `*` is
ignored). **Removed:** `/list`, `/upload`, `/v1/upload`, `/download` (no listing; use `GET /vaultchain/storage/manifests`).

### Read access

A provider serves `GET/HEAD /v1/objects/{root}` and `/v1/chunks/{root}/{i}` only to a reader the chain allows, and
checks the chain (manifest owner, `access/grants`) on each request (positive answers are cached for 5 s). A reader
proves who it is with a **signed read token**, `<pubkey_hex>.<expiry_unix>.<sig_hex>`, sent as
`Authorization: Vault <token>` or `?vault_token=<token>` (for `<img>`/`<video>` URLs). It signs
`"vaultchain/read/v1\n<provider address>\n<root>\n<expiry>"`, so a token only works for one object at one provider,
and lives at most 1 h. Get one with `vaultd read-token <root> --provider <addr> --from <key> [--ttl 10m]`.

The reader is allowed if any of these hold:

1. it owns the manifest;
2. it holds a non-expired `read` or `write` grant (`vaultd tx access grant <root> <addr> read <expiry_height|0>`);
3. the root has a **public grant** (`vaultd tx access grant <root> "*" read 0`; read only): then anonymous reads work
   and responses are `Cache-Control: public, immutable`;
4. it is an assigned/active replica of a pending/open deal for the root. This is what lets a provider fetch the
   bytes from a peer when repairing; providers sign these fetches with their own key.

No token on a non-public object gives `401` (`WWW-Authenticate: Vault`), a valid token without access gives `403`, a
root with no manifest `404`. Limits: a token is a bearer credential (one in a URL ends up in logs: keep TTLs short);
there is a grant can be withdrawn with `vaultd tx access revoke` (owner-only, see below) and the provider stops serving the
revoked reader within the 5 s cache window, even with a still-valid token; and a provider that holds the bytes
can of course read them unless you encrypted the file client-side (`commit-manifest --encrypt`).

### Revocation and encryption

- `vaultd tx access revoke <root> <grantee|*> [--grant-id N]` removes grants (owner only; fails if nothing matches).
  Providers enforce it at serve time by checking chain state; only positive answers are cached (5 s). It does not
  cut a response that is already streaming and cannot recall bytes or keys already handed out.
- `commit-manifest --encrypt` encrypts the file locally (XChaCha20-Poly1305, per-file random key) and commits a
  manifest over the **ciphertext**; the provider stores and proves ciphertext only. Grants on encrypted objects carry
  the file key wrapped to the grantee's public key; `vaultd decrypt` opens it. Metadata (name, size, grants) stays public,
  losing your key loses your data, and a revoked grantee who saved the key can still read their copy. Full threat model
  and limits: [`docs/ENCRYPTION.md`](docs/ENCRYPTION.md).
- `tools/vault-ui` is a local-only web UI for all of this: [`docs/UI.md`](docs/UI.md).

Chain REST additions: `GET /vaultchain/storage/manifests[/{root}]` and `deals?cid=&provider=&owner=`.
Run `./scripts/integration-local.sh` for the full CLI flow, or `go test ./tools/vault-provider` for the same flow
against a real in-process engine.

## Replication

`vaultd tx storage open-deal <class> <root> 0 <provider|-> <blocks> uvault --replication N [--min-replicas M]` escrows
`N x per_replica_payment` (the per-replica price is the same for market and archive; archive defaults to N=3, market
to N=1) and has the chain **assign N distinct providers**: active providers with enough free bytes, ranked by
`sha256("vaultchain/assign/v1" || app hash before the block || deal id || address)`. A provider address in the
message pins the first replica; `-` lets the chain pick all of them. Capacity is reserved on each assigned provider.

- A deal is `pending` until `min_replicas` (default: all) providers send `MsgAcceptDeal` (a provider should only do
  that once it holds the bytes; `vault-provider` does it automatically). Replicas that do not accept within
  `accept_timeout_blocks` (default 20, never past the deal expiry) become `declined` and free their capacity. At the
  deadline the deal opens if quorum was reached; otherwise it is `cancelled` and the whole escrow is refunded.
- An open deal keeps one `replicas[]` entry per slot with its own status (`assigned`, `active`, `failed`,
  `declined`, `done`), accept deadline, last proven epoch and missed-proof counter.
- A replica that misses `max_missed_proofs` consecutive epochs is `failed`: 10% of *its* collateral goes to the owner
  and its capacity is freed. At the next epoch the chain tries to **repair** the slot by assigning a provider that
  never held this deal. The replacement must copy the bytes from another active replica (the provider binary does
  this over HTTP from the endpoint registered on chain, verifying them against the root) and accept. If no provider is
  eligible the slot stays empty and is retried each epoch; if every replica has failed the deal ends `failed` with a
  full refund.
- At expiry every slot's share is paid to its active replica if that replica proved at least once; an empty or
  unproven slot is refunded to the owner (an accepted-but-never-proved replica is also slashed). The deal is `closed`
  if anyone was paid, else `failed`.

Behaviour change from M6: a market deal with `replication > 1` used to be charged once; payment is now per replica
for both classes, and `open-deal` no longer requires a provider.

Live repair: `scripts/e2e-repair.sh` needs three providers (extra Fly provider apps are not deployed by default; add them as described in docs/FLY-DEVNET.md section 4b,
or point `scripts/e2e-replication.sh` at locally started providers without `FAILURE_TEST`). It opens a 2-replica deal, stops one replica's machine,
waits for the chain to fail and replace it with the spare provider (which fetches the bytes from the healthy
replica, accepts and proves), and checks both downloads. See `docs/FLY-DEVNET.md`.

## Proof of storage

Every `proof_interval_blocks` (default 10) a new *epoch* starts, seeded with the app hash before that block. For each
**active replica** of an open deal the chain derives the challenged chunk index from
`sha256("vaultchain/challenge/v2" || seed || deal_id || provider)`, so replicas get different questions and cannot
proxy each other. `GET /vaultchain/storage/challenge/{deal_id}?provider=<addr>` returns it (`400` no provider,
`404` unknown deal, `409` not challengeable: no active replica for that provider, or it accepted after the epoch began).

The provider answers with `MsgSubmitProof {deal_id, cid, epoch, chunk, proof}`: the chunk bytes plus the Merkle path,
verified on chain against the committed root (`pkg/chunk.VerifyProof`). One proof per replica per epoch; a stale epoch
or a wrong chunk is rejected. `vault-provider -home <vaultd home> -key provider` accepts and proves automatically (poll
every `-prove-interval`, default 3 s; each tx pays the fee). By hand: `vaultd tx storage accept-deal <deal_id>` and
`vaultd tx storage submit-proof <deal_id> --provider-url http://127.0.0.1:3333 --from provider`.

Limits (honest): the seed is the public app hash (a proposer can grind it and a provider learns its challenge one
block ahead), the multi-validator mode has a fixed validator set (no staking), a proof only shows the provider held that one chunk in that
epoch (assurance grows with epochs), there is no cap on repair attempts, and replica selection ignores locality,
price and reputation. This is lab-grade storage assurance, not production.
