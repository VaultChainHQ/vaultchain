# Vaultchain

Clean-room decentralized storage L1 (local single-validator engine for Phase 0 lab).

| | |
|---|---|
| Binary | `vaultd` |
| Module | `github.com/vaultchain/vaultchain` |
| Bech32 | `vault` |
| Denom | `uvault` |
| Local chain-id | `vault-local-1` |
| License | MIT |

Inspired by public lessons from storage networks — **not** a fork of any existing chain.

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
pkg/chunk/             chunking, Merkle tree, content root, inclusion proofs
tools/vault-provider/  object store: verified chunked storage, Range reads, deal-gated writes
tools/vault-lab/       click-to-test lab UI (:3456)
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
# From Windows against WSL, use the printed WSL IP, e.g. http://172.x.x.x:3456/
```

Lab flows: register provider, buy personal capacity, upload a file (hash into a manifest, commit it, open a deal, stream the bytes to the provider, stub proof), IBC policy / mint voucher. The lab has no CORS and refuses cross-origin POSTs: use the UI it serves.

## Objects and the provider API

An object is a file split into chunks (256 KiB by default, `pkg/chunk`). Its **root** commits to the Merkle tree of
chunk hashes plus size and chunk size, so the root alone pins the exact bytes.

1. `commit-manifest` publishes `{root, size, chunk_size, mime, name}` on chain. The first committer owns the root
   (note: anyone holding the file can commit it first, so ownership is not proof of authorship).
2. `open-deal` takes that root (size 0 = manifest size) and only the manifest owner may open it.
3. The provider accepts bytes only for a root that has an open deal naming that provider (`-address` is required;
   the old `-skip-chain-check` is gone) and re-verifies the whole object against the root before keeping it.

| Provider route | Purpose |
|----------------|---------|
| `POST\|PUT /v1/objects/{root}` | stream a whole object (verified, then installed atomically) |
| `GET\|HEAD /v1/objects/{root}` | download, supports `Range`; each chunk is re-hashed before it is served |
| `GET /v1/objects/{root}/status` | stored / complete |
| `PUT /v1/chunks/{root}/{i}` | upload one chunk with its Merkle proof in `X-Vault-Proof` |
| `GET\|HEAD /v1/chunks/{root}/{i}` | one chunk (+ `X-Vault-Proof` once the object is complete) |
| `/status`, `/version` | node info |

Responses use the manifest's Content-Type with `nosniff` and a locked-down CSP, and are `attachment` unless the type
is a safe inline media type. CORS is off unless you pass `-allow-origin https://app.example` (repeatable; `*` is
ignored). **Removed:** `/list`, `/upload`, `/v1/upload`, `/download` (no listing; use `GET /vaultchain/storage/manifests`).
Reads are open to anyone who knows the root; access-grant enforcement on retrieval is not implemented.

Chain REST additions: `GET /vaultchain/storage/manifests[/{root}]` and `deals?cid=&provider=&owner=`.
Run `./scripts/integration-local.sh` for the full CLI flow, or `go test ./tools/vault-provider` for the same flow
against a real in-process engine.