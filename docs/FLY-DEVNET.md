# Running the vaultl1 devnet on Fly.io

This deploys **one chain node** (`vaultl1-devnet`) and **three storage providers**
(`vaultl1-provider`, `vaultl1-provider-2`, `vaultl1-provider-3`). All are tiny Fly machines
with their own 1 GB volume. A separate multi-validator CometBFT network can be deployed next to
it (`vaultl1-val-1/2/3`, see [Multi-validator devnet](#multi-validator-devnet-vaultl1-val-123)).

## What this is (and is not)

- It is a **devnet**, not a decentralized network. The `vaultl1-devnet` node is a single validator
  that produces a block every `BLOCK_INTERVAL` (3s) on a timer: no multi-validator consensus, no
  staking, no peer networking. (The separate CometBFT network `vaultl1-val-1/2/3` does run
  multi-validator consensus; see [Multi-validator devnet](#multi-validator-devnet-vaultl1-val-123).
  It has no staking either.)
- If the node's volume is lost, the chain is lost. Single machine, no HA.
- Anyone can POST *signed* transactions to the public REST endpoint (that is the
  point of a devnet), but the public REST is hardened: see
  [Public REST: what is and is not exposed](#public-rest-what-is-and-is-not-exposed).
- Read tokens are bearer credentials. Grants cannot be revoked. Nothing is encrypted
  at rest on the provider.
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

### 4b. A second (or Nth) provider

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

To reset the chain: destroy the volume and redeploy (all state and keys are lost,
and the provider must re-register on the new chain, so reset its volume too).

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

## Replication and failure test

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

## Multi-validator devnet (vaultl1-val-1/2/3)

Three more apps run the same state machine behind CometBFT (`--consensus comet`, chain id
`vault-devnet-2`, lab mode off), next to the old single-node `vaultl1-devnet`, which keeps
running unchanged. Each validator is one `shared-cpu-1x` / 256 MB machine with its own 1 GB volume
(`vaultd_data`). p2p (26656) is IPv6 6PN only (`<app>.internal`); only REST is public:
`https://vaultl1-val-1.fly.dev`, `-val-2`, `-val-3`. Config: `fly.validator.toml` (generic, the app
name comes from `-a`) and `deploy/Dockerfile.validator`.

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

**Live results (2026-10-25).** 3 validators at equal heights with equal app hashes at height 22; a tx sent to
`val-1` visible on all three; `scripts/e2e-remote.sh` 34/34 and `scripts/e2e-replication.sh` 11/11 against
`https://vaultl1-val-1.fly.dev` with two providers run locally (in a container) and registered on the new
chain; one validator stopped = chain halted, started again = chain resumed and all three
agree. Memory: about 100 MB of the 256 MB is still available per machine; `/data` was about 15 MB after the first hour.
A request to the public REST name of a stopped validator wakes its machine (`auto_start_machines`), so
poll the other validators or `fly machine status` while testing an outage.

**Providers.** The three Fly providers still point at `vaultl1-devnet` (chain `vault-devnet-1`). A provider
follows exactly one chain, so pointing it at `vault-devnet-2` means setting `DEVNET_REST` and `CHAIN_ID` for
its app, funding its account on the new chain and letting it register again. Do that deliberately: the old
devnet would then have no providers. The new chain currently has leftover test providers (from the
verification runs, registered with `127.0.0.1` endpoints) that are not reachable; reset the chain (new
volumes, or a new chain id) before using it for real.

**Cost.** Each validator is about the price of one provider (a small machine plus a 1 GB volume, roughly
USD 2 to 3 per month at current Fly pricing, check the pricing page): about USD 6 to 9 per month for the three. The
old devnet can be retired once the providers have moved (not done; deleting is a separate decision).
## Size and cost

All three apps use `shared-cpu-1x` with 256 MB RAM and a 1 GB volume, one machine
each. That is the smallest sensible setup (a few dollars per month in total at
current Fly pricing; each extra provider adds one machine and one volume; check
https://fly.io/docs/about/pricing/). `auto_stop_machines` is off because the chain
must keep producing blocks.

## CI

`.github/workflows/ci.yml` runs gofmt, build, vet and test, and builds the devnet, provider and validator images
without pushing, on `actions/checkout@v6` and `actions/setup-go@v6` (Node 24). It does
not deploy.
