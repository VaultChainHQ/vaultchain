#!/usr/bin/env bash
set -euo pipefail
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
rm -f build/vaultd
go build -o build/vaultd ./cmd/vaultd
./build/vaultd start --help | head -40

HOME_DIR=/tmp/vaultchain-smoke
rm -rf "${HOME_DIR}"
./build/vaultd init local --home "${HOME_DIR}" --chain-id vault-local-1 --lab-mode
./build/vaultd keys add validator --home "${HOME_DIR}"
ADDR="$(./build/vaultd keys show validator --home "${HOME_DIR}" | tr -d '\r' | tail -n1)"
echo "CAPTURED=${ADDR}"
test -n "${ADDR}"
[[ "${ADDR}" == vault* ]]
./build/vaultd genesis add-genesis-account "${ADDR}" 100000000000000uvault --home "${HOME_DIR}"

./build/vaultd start --home "${HOME_DIR}" --rpc-addr 127.0.0.1:36657 --api-addr 127.0.0.1:31317 --block-interval 1s > /tmp/vault-start.log 2>&1 &
SPID=$!
cleanup() { kill "${SPID}" 2>/dev/null || true; }
trap cleanup EXIT
sleep 3
echo "=== log ==="
cat /tmp/vault-start.log
echo "=== status ==="
curl -sf http://127.0.0.1:36657/status
echo
echo "=== policy ==="
curl -sf http://127.0.0.1:31317/vaultchain/ibc/policy
echo
echo "=== balances ==="
curl -sf "http://127.0.0.1:31317/cosmos/bank/v1beta1/balances/${ADDR}"
echo
echo SMOKE_OK
