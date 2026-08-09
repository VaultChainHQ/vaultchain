#!/usr/bin/env bash
set -euo pipefail
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

HOME_DIR=/tmp/vaultchain-int
RPC=127.0.0.1:36657
REST=127.0.0.1:31317
PROV_LISTEN=:3433

fuser -k 36657/tcp 31317/tcp 3433/tcp 2>/dev/null || true
rm -rf "${HOME_DIR}" /tmp/vault-provider-int
mkdir -p build
[[ -x build/vaultd ]] || go build -o build/vaultd ./cmd/vaultd
[[ -x build/vault-provider ]] || go build -o build/vault-provider ./tools/vault-provider

./build/vaultd init local --home "${HOME_DIR}" --chain-id vault-local-1
./build/vaultd keys add validator --home "${HOME_DIR}" >/dev/null
./build/vaultd keys add alice --home "${HOME_DIR}" >/dev/null
./build/vaultd keys add provider --home "${HOME_DIR}" >/dev/null
VAL="$(./build/vaultd keys show validator --home "${HOME_DIR}")"
ALICE="$(./build/vaultd keys show alice --home "${HOME_DIR}")"
PROV="$(./build/vaultd keys show provider --home "${HOME_DIR}")"
./build/vaultd genesis add-genesis-account "${VAL}" 100000000000000uvault --home "${HOME_DIR}"
./build/vaultd genesis add-genesis-account "${ALICE}" 50000000000000uvault --home "${HOME_DIR}"
./build/vaultd genesis add-genesis-account "${PROV}" 5000000000000uvault --home "${HOME_DIR}"

./build/vaultd start --home "${HOME_DIR}" --rpc-addr "${RPC}" --api-addr "${REST}" --block-interval 500ms >/tmp/vault-int-chain.log 2>&1 &
CPID=$!
./build/vault-provider -address "${PROV}" -listen "${PROV_LISTEN}" -data /tmp/vault-provider-int -rest "http://${REST}" -chain-id vault-local-1 >/tmp/vault-int-prov.log 2>&1 &
PRPID=$!
cleanup() { kill "${CPID}" "${PRPID}" 2>/dev/null || true; }
trap cleanup EXIT
sleep 2

NODE="http://${REST}"
./build/vaultd tx storage register-provider "http://127.0.0.1${PROV_LISTEN}" "$((10*1024*1024*1024))" 1000000000uvault --from provider --home "${HOME_DIR}" --node "${NODE}"
sleep 1
./build/vaultd tx storage buy-capacity "$((1<<30))" 10000 uvault --from alice --home "${HOME_DIR}" --node "${NODE}"
sleep 1

BLOB="vaultenc:v1:hello-vaultchain"
CID="$(printf %s "${BLOB}" | sha256sum | awk '{print $1}')"
SIZE="${#BLOB}"
./build/vaultd tx storage open-deal market "${CID}" "${SIZE}" "${PROV}" 10000 uvault --from alice --home "${HOME_DIR}" --node "${NODE}"
sleep 1

DEAL="$(curl -sf "http://${REST}/vaultchain/storage/deals" | python3 -c "import sys,json; d=json.load(sys.stdin); print([x['id'] for x in d['deals'] if x['cid']=='${CID}'][0])")"
echo "DEAL_ID=${DEAL} CID=${CID}"
printf %s "${BLOB}" >/tmp/vault-blob.bin
curl -sf -X POST "http://127.0.0.1${PROV_LISTEN}/v1/upload" \
  -F "owner=${ALICE}" -F "cid=${CID}" -F "deal_id=${DEAL}" -F "file=@/tmp/vault-blob.bin;filename=blob"
echo
./build/vaultd tx storage submit-proof "${DEAL}" "${CID}" "stub-${CID}" --from provider --home "${HOME_DIR}" --node "${NODE}"
sleep 1
./build/vaultd tx ibc-lab mint-voucher "${ALICE}" ibc/ATOM 1000000 --from alice --home "${HOME_DIR}" --node "${NODE}"
sleep 1

echo "=== deals ==="; curl -sf "http://${REST}/vaultchain/storage/deals"; echo
echo "=== proofs ==="; curl -sf "http://${REST}/vaultchain/storage/proofs"; echo
echo "=== plans ==="; curl -sf "http://${REST}/vaultchain/storage/plans"; echo
echo "=== balances alice ==="; curl -sf "http://${REST}/cosmos/bank/v1beta1/balances/${ALICE}"; echo
echo "=== download ==="; curl -sf "http://127.0.0.1${PROV_LISTEN}/v1/download/${CID}"; echo
echo INT_OK
