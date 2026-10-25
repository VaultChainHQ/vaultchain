# Running the vaultl1 devnet on Fly.io

This deploys **one chain node** (`vaultl1-devnet`, https://vaultl1-devnet.fly.dev) and **one storage
provider** (`vaultl1-provider`, https://vaultl1-provider.fly.dev). Both are tiny Fly machines with their own
1 GB volume. That is the default devnet and the only thing deployed.

Larger setups are available but **not currently deployed**: a multi-validator CometBFT network
(`vaultl1-val-1/2/3`, see [Multi-validator devnet](#multi-validator-devnet-vaultl1-val-123-not-currently-deployed))
and extra providers (section 4b, plus the replication and repair tests). They were deployed and verified
on 2026-10-25 and then retired to save cost. Everything needed to bring them back is in this file and in
the repo (`fly.validator.toml`, `deploy/docker-compose.comet.yml`, `scripts/comet-local.sh`).

## What this is (and is not)

- It is a **devnet**, not a decentralized network. The `vaultl1-devnet` node is a single validator
  that produces a block every `BLOCK_INTERVAL` (3s) on a timer: no multi-validator consensus, no
  staking, no peer networking. (The CometBFT mode, `--consensus comet`, does run multi-validator
  consensus, locally or on Fly; see
  [Multi-validator devnet](#multi-validator-devnet-vaultl1-val-123-not-currently-deployed).
  It has no staking either.)
- If the node's volume is lost, the chain is lost. Single machine, no HA.
- Anyone can POST *signed* transactions to the public REST endpoint (that is the
  point of a devnet), but the public REST is hardened: see
  [Public REST: what is and is not exposed](#public-rest-what-is-and-is-not-exposed).
- Read tokens are bearer credentials, but access is re-checked against chain state on every read, so an
  owner can revoke a grant (`vaultd tx access revoke`) and it takes effect within ~5 s. Files are plaintext at the
  provider unless the client encrypted them first (`commit-manifest --encrypt`; see
  [`ENCRYPTION.md`](ENCRYPTION.md)).
- The provider is a separate process that talks to the node over Fly's private
  network (`vaultl1-devnet.internal`). More providers are more copies of the same
  config under different app names (section 4b).

What real consensus would still need beyond the CometBFT mode: a validator set with staking and
slashing (the set and powers are fixed at genesis), state sync for new nodes, and a real fee market.

## 1. Install flyctl and log in

```powershell
# Windows (PowerShell)
iwr https://fly.io/install.ps1 -useb | iex
fly auth login
fly auth whoami
```

## 2. Check for name clashes

App names are global on Fly. Pick different names (and edit `app =` in the toml files)
if `vaultl1-devnet` or `vaultl1-provider` are already taken by someone else.

```powershell
fly apps list
```

## 3. Create the node

Run everything from the repo root (the Docker build context is the cwd).

```powershell
fly apps create vaultl1-devnet
fly volumes create vaultd_data --app vaultl1-devnet --region iad --size 1 --yes
fly deploy -c fly.devnet.toml --ha=false
```

On first boot the entrypoint runs `vaultd init --lab-mode`, creates a `validator`
key and funds it in genesis. The key lives only in `/data/vaultd/keyring-test` on
the volume. Genesis is skipped on later boots (marker file `/data/vaultd/.genesis-done`).

Check it:

```powershell
curl https://vaultl1-devnet.fly.dev/cosmos/base/tendermint/v1beta1/node_info
```

`height` should increase every ~3 seconds.

## 4. Create the provider

```powershell
fly apps create vaultl1-provider
fly volumes create provider_data --app vaultl1-provider --region iad --size 1 --yes
fly deploy -c fly.provider.toml --ha=false
fly logs -a vaultl1-provider
```

The log prints `provider address: vault1...` and then `waiting for funds`. If you
renamed the provider app, update `PUBLIC_URL` in `fly.provider.toml` first.

### 4b. A second (or Nth) provider (optional, not deployed)

Only `vaultl1-provider` is deployed and registered. `vaultl1-provider-2` and `-3` were used for the replication
and repair tests on the previous chain and then destroyed. The chain was reset on 2026-10-25 (new genesis, same
chain id `vault-devnet-1`), so their registrations are gone: `GET /vaultchain/storage/providers` lists exactly one
provider. To run the replication or repair tests again, deploy fresh providers under new names.
`fly.provider.toml` carries no app-specific values: the advertised `PUBLIC_URL`
defaults to `https://$FLY_APP_NAME.fly.dev`, and the volume name is per app. So
another provider is the same file with `-a`:

```powershell
fly apps create vaultl1-provider-2
fly volumes create provider_data --app vaultl1-provider-2 --region iad --size 1 --yes
fly deploy -c fly.provider.toml -a vaultl1-provider-2 --ha=false
fly logs -a vaultl1-provider-2          # prints its own address, then waits for funds
```

Each provider generates its own key on its own volume. Fund it as in section 5 (use
its address) and it registers itself. Replication 2 needs two active providers with
enough free bytes; the chain assigns providers, you only pin the first one.

## 5. Fund the provider (one time)

There is no key import, so the provider generates its own key on its volume. Send it
funds from the validator account:

```powershell
fly ssh console -a vaultl1-devnet -C "su-exec vault:vault vaultd tx bank-send <PROVIDER_ADDR> 5000000000uvault --from validator --home /data/vaultd --node http://127.0.0.1:1317 --fee 1000uvault"
```

Within ~10-20 seconds the provider registers itself (collateral 1000000000uvault,
capacity 700000000 bytes) and logs `is registered on chain`. Verify:

```powershell
curl https://vaultl1-devnet.fly.dev/vaultchain/storage/providers/<PROVIDER_ADDR>
curl https://vaultl1-provider.fly.dev/status
```

## Secrets

Keys are on volumes, never in git. Optional tuning goes through `fly secrets set` or
`[env]`: `ALLOW_ORIGIN` (comma-separated CORS origins for browser clients),
`CAPACITY_BYTES`, `COLLATERAL`, `FEE`, `VALIDATOR_FUNDS`.

The node has one real secret, `VAULT_ADMIN_TOKEN` (see the next section). Generate
a random value and set it as a Fly secret; never commit or paste it anywhere else:

```powershell
fly secrets set VAULT_ADMIN_TOKEN=<64 random hex chars> -a vaultl1-devnet
```

`vaultd tx ...` sends it as `X-Admin-Token` when `VAULT_ADMIN_TOKEN` is set in your
environment. Without a token configured on the node, nobody is admin.

## Public REST: what is and is not exposed

Lab mode is a genesis flag (`vaultd init --lab-mode`, fixed for the life of a chain).
It enables exactly one thing: the `ibc/SimulateIBCDeposit` message
(`vaultd tx ibc-lab mint-voucher`), which lets the *sender mint any amount of any
`ibc/*` voucher* and auto-whitelists that denom for storage payments. Nothing else
differs.

What the node now does for the internet (the three are node-local admission policy,
not consensus; they cost nothing for normal signed transactions):

| Control | Setting | Effect |
|---|---|---|
| Admin gate | `VAULT_ADMIN_TOKEN` secret, `X-Admin-Token` header | Lab messages are refused over public REST (403) unless the header matches (constant-time compare). With no token set, always refused. |
| Minimum fee | `MIN_FEE` (default `1000uvault`) | Zero- or low-fee transactions are refused at submit (400), so a fresh keypair cannot fill the mempool for free. |
| Rate limit | `TX_RATE_PER_MIN` (default `60`, burst 30) | Per client IP on `POST /vaultchain/txs`; excess gets 429 with `Retry-After`. Client IP comes from Fly's `Fly-Client-IP` header; loopback and Fly private (6PN) callers, such as providers and `fly ssh`, are exempt. |
| Lab mode default | `LAB_MODE` (default off) | New chains are created without lab mode unless `LAB_MODE=1` at first boot. |

`vaultl1-devnet-1` was created with lab mode on, so it still has the capability;
the REST gate is what protects it. Only someone with the token (or a shell on the
machine, which owns the RPC on loopback) can mint. To get rid of the capability
entirely, reset the chain with `LAB_MODE` unset (destroys all chain state).

Still true, by design of a devnet: signed transactions are accepted from anyone with
funds; a distributed attacker (many IPs) can still burn through the limits; CORS is
`*` on the node REST; the provider upload endpoint accepts bytes for any root that
has an on-chain manifest (bytes are verified against the root, so they cannot corrupt
an object, but they use disk and bandwidth); read tokens are bearer credentials.

```powershell
fly secrets set ALLOW_ORIGIN=https://your-app.example -a vaultl1-provider
```

## Operations

```powershell
fly status -a vaultl1-devnet
fly logs -a vaultl1-devnet
fly ssh console -a vaultl1-devnet
fly volumes snapshots list <volume-id> -a vaultl1-devnet   # Fly takes daily snapshots
```

To reset the chain: stop the machine, destroy the volume, create a new one and redeploy (all state and keys are lost,
and the validator account is new, so fund every other account again),
and the provider must re-register on the new chain, so reset its volume too; it then gets a new address and needs
funding again, section 5). App secrets (`VAULT_ADMIN_TOKEN`) are kept across a volume reset:

```powershell
fly machine list -a vaultl1-devnet            # note the machine id
fly machine stop <id> -a vaultl1-devnet
fly volumes list -a vaultl1-devnet            # note the volume id
fly volumes destroy <volume-id> -a vaultl1-devnet --yes
fly volumes create vaultd_data --app vaultl1-devnet --region iad --size 1 --yes
fly deploy -c fly.devnet.toml --ha=false      # same for vaultl1-provider with fly.provider.toml / provider_data
```

## End-to-end test against the live devnet

`scripts/e2e-remote.sh` drives the whole flow against the deployed node and provider:
commit-manifest, open-deal, provider auto-accept, upload, automatic Merkle proof,
full and Range downloads with hash checks, and access enforcement (anonymous 401,
no-grant 403, granted reader 200, public `*` grant). It creates `alice` and `bob`
keys in `~/.vaultl1-e2e`, prints their addresses, and asks you to fund them (same
`bank-send` command as above) before it runs.

```bash
go build -o build/vaultd ./cmd/vaultd
NODE=https://vaultl1-devnet.fly.dev PROVIDER_URL=https://vaultl1-provider.fly.dev scripts/e2e-remote.sh
```

## Revocation and encrypted-file test

`scripts/e2e-private.sh` runs against the same node and provider: it encrypts a file client-side, checks the provider
stores only ciphertext, decrypts as the owner, rejects tampered and truncated ciphertext, grants bob (wrapping the key
to his public key), has bob decrypt, then revokes him and checks that the provider refuses his still-valid read token
and that his key envelope is gone from chain state. `scripts/e2e-remote.sh` also revokes bob's grant and the public
grant and expects 403 / 401 within seconds.

```bash
NODE=https://vaultl1-devnet.fly.dev PROVIDER_URL=https://vaultl1-provider.fly.dev scripts/e2e-private.sh
```

## Local upload UI

[`docs/UI.md`](UI.md): `./build/vault-ui --node https://vaultl1-devnet.fly.dev --provider https://vaultl1-provider.fly.dev
--home <keyring> --key <name>`, then open http://127.0.0.1:8090. It signs with a local key, binds loopback only and is
**not** deployed on Fly (and should not be).

## Replication and failure test

> Needs extra provider apps, which are not deployed now (see section 4b). Deploy two or three providers first,
> or run `scripts/e2e-replication.sh` against locally started providers (leave `FAILURE_TEST` unset; use a
> fresh chain, since stale local test providers stay registered on the chain under their old endpoints).
> Last live results (2026-10-25, before the extra providers were retired): replication 11/11, repair 15/15.

`scripts/e2e-replication.sh` opens one deal with `--replication 2` across both
providers: both are assigned, the deal stays pending until the owner uploads to
each, both accept and prove, and both serve a hash-identical download.

With `FAILURE_TEST=1` (needs a logged-in `fly`) it then stops provider 2's machine:
first briefly (the missed-epoch counter rises, the replica stays active, and the
counter resets once provider 2 proves again), then long enough that the replica
fails (3 consecutive missed epochs, about 2 minutes) and is slashed 1/10 of its
collateral to the owner. The deal stays open on the surviving replica. The script
always starts the machine again on exit.

```bash
FLY=fly FAILURE_TEST=1 scripts/e2e-replication.sh
```

Repair needs a *third* eligible provider: a failed provider is excluded from its own
deal, so with only two providers the empty slot stays empty and the chain retries
every epoch. With `vaultl1-provider-3` deployed, `scripts/e2e-repair.sh` shows reassignment live:

```bash
FLY=fly PROVIDER_URLS="https://vaultl1-provider.fly.dev https://vaultl1-provider-2.fly.dev https://vaultl1-provider-3.fly.dev" \
  scripts/e2e-repair.sh
```

It opens a deal with 2 replicas, stops one replica's machine (whichever the chain picked), waits for
that slot to fail (3 missed epochs, about 2 minutes) and be reassigned to the spare, which fetches the
bytes from the healthy replica, accepts and proves; then both downloads must match the original hash.
The victim is always started again on exit. (Last live run: 15/15 checks passed.)

## Multi-validator devnet (vaultl1-val-1/2/3, not currently deployed)

**Status: retired.** Three apps ran the same state machine behind CometBFT (`--consensus comet`, chain id
`vault-devnet-2`, lab mode off), next to `vaultl1-devnet`. They were verified on 2026-10-25 and destroyed
(apps and volumes) to save cost. Nothing below is live; the commands are the exact steps to deploy it again.
Each validator was one `shared-cpu-1x` / 256 MB machine with its own 1 GB volume (`vaultd_data`). p2p
(26656) is IPv6 6PN only (`<app>.internal`); only REST is public (`https://vaultl1-val-N.fly.dev` while
deployed). Config: `fly.validator.toml` (generic, the app name comes from `-a`) and `deploy/Dockerfile.validator`.
To run the same network without Fly: `docker compose -f deploy/docker-compose.comet.yml up --build` or
`scripts/comet-local.sh`.

**Fault tolerance: 3 equal validators tolerate zero failures.** CometBFT needs strictly more than 2/3 of
the voting power to commit. With 3 x 10 power, two validators hold exactly 2/3, so stopping one
validator halts the chain until it returns (verified live: the chain stalled at the same height on the two
survivors and resumed within seconds after the machine started). Tolerating one failed validator needs at
least 4 equal validators (verified locally with 4: one stopped, the other three kept producing blocks, the
restarted one caught up). The validator set is fixed at genesis, so going from 3 to 4 means generating a
new genesis and a new chain.

**Keys and files.** The network is generated once, offline, on your machine. The output contains private
keys, so keep it out of git:

```bash
vaultd testnet --validators 3 --chain-id vault-devnet-2 --out ~/.vaultl1-val-net \
  --hostname-pattern 'vaultl1-val-%d.internal'
```

For each `nodeN` there are four secrets, set per app as base64 (names only; never print the values):
`COMET_GENESIS_B64` (`config/cometgenesis.json`, not secret), `COMET_PRIV_VALIDATOR_KEY_B64`
(`config/priv_validator_key.json`), `COMET_NODE_KEY_B64` (`config/node_key.json`), `VAULT_KEYRING_B64`
(`keyring-test/keys.json`, the validator's fee account), plus `PERSISTENT_PEERS` (the other two
`id@vaultl1-val-M.internal:26656`, from `nodeN/persistent_peers.txt`). `deploy/validator-entrypoint.sh`
writes these files on every boot, never touches `priv_validator_state.json` (double-sign protection),
and fails if a file is missing. A peer whose `.internal` name does not resolve yet is skipped for that boot;
it dials in instead.

```bash
for N in 1 2 3; do
  fly apps create vaultl1-val-$N
  fly volumes create vaultd_data -a vaultl1-val-$N -r iad -n 1 -y
  # build an .env file (NAME=value lines, no BOM) with the five secrets, then:
  fly secrets import -a vaultl1-val-$N --stage < val-$N.env
  fly deploy -c fly.validator.toml -a vaultl1-val-$N --ha=false
done
```

**Checks.** `GET /cosmos/base/tendermint/v1beta1/node_info` has a `consensus` object (height, peers,
validators, catching_up). All three validators must report the same height (+-1), 2 peers and 3
validators. To compare app hashes at one height:
`fly ssh console -a vaultl1-val-1 -C "curl -s 'http://127.0.0.1:26657/block?height=22'"` (the `app_hash` in the
header must match on every node). A signed `bank-send` submitted to one node must show up in the balances
of all three.

**Live results (2026-10-25, historical).** 3 validators at equal heights with equal app hashes at height 22; a tx
sent to `val-1` visible on all three; `scripts/e2e-remote.sh` 34/34 and `scripts/e2e-replication.sh` 11/11
against `https://vaultl1-val-1.fly.dev` with two providers run locally (in a container) and registered on the new
chain; one validator stopped = chain halted, started again = chain resumed and all three agree. Memory: about
100 MB of the 256 MB was still available per machine; `/data` was about 15 MB after the first hour. A request to
the public REST name of a stopped validator wakes its machine (`auto_start_machines`), so poll the other
validators or `fly machine status` while testing an outage.

**Redeploying.** The keys in `~/.vaultl1-val-net` (kept locally, never in git) still match the genesis, so the
same loop above recreates the same network from height 0 (the old volumes and their
`priv_validator_state.json` are gone, which is fine for a fresh chain). Use new keys (re-run `vaultd testnet`)
if the old ones may have been exposed. To run 4 validators (tolerates one failure), generate with
`--validators 4`.

**Providers.** A provider follows exactly one chain, so a provider for `vault-devnet-2` is its own app with
`DEVNET_REST=http://vaultl1-val-1.internal:1317` and `CHAIN_ID=vault-devnet-2`, funded on that chain. The
`vault-devnet-1` providers never moved.

**Cost.** Each validator is about the price of one provider (a small machine plus a 1 GB volume, roughly
USD 2 to 3 per month at current Fly pricing, check the pricing page): about USD 6 to 9 per month for the three.

## Size and cost

The two apps (`vaultl1-devnet`, `vaultl1-provider`) use `shared-cpu-1x` with 256 MB RAM and a 1 GB volume, one
machine each. That is the smallest sensible setup: roughly USD 4 to 6 per month in total at current Fly
pricing (an estimate, not a bill; check https://fly.io/docs/about/pricing/). Each extra provider or validator
adds one machine and one volume. `auto_stop_machines` is off because the chain must keep producing blocks.

## CI

`.github/workflows/ci.yml` runs gofmt, build, vet and test, and builds the devnet, provider and validator images
without pushing, on `actions/checkout@v6` and `actions/setup-go@v6` (Node 24). It does
not deploy.
