# Running the vaultl1 devnet on Fly.io

This deploys **one chain node** (`vaultl1-devnet`) and **one storage provider**
(`vaultl1-provider`). Both are tiny Fly machines with their own 1 GB volume.

## What this is (and is not)

- It is a **devnet**, not a decentralized network. The node is a single validator
  that produces a block every `BLOCK_INTERVAL` (3s) on a timer. There is **no
  multi-validator consensus**, no staking, no peer networking.
- If the node's volume is lost, the chain is lost. Single machine, no HA.
- Lab mode is on; anyone can POST transactions to the public REST endpoint.
- Read tokens are bearer credentials. Grants cannot be revoked. Nothing is encrypted
  at rest on the provider.
- The provider is a separate process that talks to the node over Fly's private
  network (`vaultl1-devnet.internal`). More providers can be added by deploying
  more copies with different app names.

What real consensus would still need: a multi-validator BFT engine (for example
CometBFT) wired to the state machine, a validator set with staking and slashing for
validators, p2p networking and state sync, and a real fee market. None of that is
in this repo yet.

## 1. Install flyctl and log in

```powershell
# Windows (PowerShell)
iwr https://fly.io/install.ps1 -useb | iex
fly auth login
fly auth whoami
```

## 2. Remove old apps (only if they exist)

```powershell
fly apps list
fly apps destroy vaultl1-devnet-a
fly apps destroy vaultl1-devnet-b
fly apps destroy vaultl1-provider      # frees the name used below
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

## Size and cost

Both apps use `shared-cpu-1x` with 256 MB RAM and a 1 GB volume, one machine each.
That is the smallest sensible setup (a couple of dollars per month in total at
current Fly pricing; check https://fly.io/docs/about/pricing/). `auto_stop_machines`
is off because the chain must keep producing blocks.

## CI

`.github/workflows/ci.yml` runs gofmt, build, vet and test, and builds both images
without pushing. It does not deploy.