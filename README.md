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
block ahead), there is no real multi-validator consensus, a proof only shows the provider held that one chunk in that
epoch (assurance grows with epochs), there is no cap on repair attempts, and replica selection ignores locality,
price and reputation. This is lab-grade storage assurance, not production.
