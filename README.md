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

See [`docs/IBC-PAYMENTS-ONLY.md`](docs/IBC-PAYMENTS-ONLY.md), Phase 0 spec [`docs/PHASE0-SPEC.md`](docs/PHASE0-SPEC.md), and foundation tradeoff [`docs/FOUNDATION-SOVEREIGN-VS-COSMOS.md`](docs/FOUNDATION-SOVEREIGN-VS-COSMOS.md).

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
vaultd init local-validator --home ~/.vaultchain --chain-id vault-local-1
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
vaultd tx storage open-deal market bafyExample 1048576 <provider-addr> 5000 uvault --from alice --home ~/.vaultchain

# Lab-only fake IBC voucher for payment whitelist testing
vaultd tx ibc-lab mint-voucher $(vaultd keys show alice --home ~/.vaultchain) ibc/ATOM 1000000 --from alice --home ~/.vaultchain
```

## Layout

```
cmd/vaultd/            CLI + daemon entry
node/                  local engine (blocks, mempool, RPC, REST, bolt state)
x/storage/             providers, capacity, deals, proofs, denom whitelist
x/access/              AccessGrant store
x/bank/                balances + escrow
x/ibc/                 payments-only policy + lab voucher mint
crypto/                test keyring (secp256k1)
tools/vault-provider/  native object store (upload/download by CID)
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

Lab flows: register provider → buy personal capacity → deal+upload encrypted blob → stub proof → IBC policy / mint voucher.
