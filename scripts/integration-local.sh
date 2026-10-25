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

./build/vaultd init local --home "${HOME_DIR}" --chain-id vault-local-1 --lab-mode
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

# A real multi-chunk file (600 KB => 3 chunks at 256 KiB).
FILE=/tmp/vault-int-file.bin
head -c 600000 /dev/urandom >"${FILE}"
ROOT_HEX="$(./build/vaultd tx storage commit-manifest "${FILE}" --mime application/octet-stream --name int.bin --dry-run | python3 -c "import sys,json; print(json.load(sys.stdin)['root'])")"
SIZE="$(stat -c %s "${FILE}")"
echo "ROOT=${ROOT_HEX} SIZE=${SIZE}"
./build/vaultd tx storage commit-manifest "${FILE}" --mime application/octet-stream --name int.bin --from alice --home "${HOME_DIR}" --node "${NODE}"
./build/vaultd tx storage open-deal market "${ROOT_HEX}" 0 "${PROV}" 10000 uvault --from alice --home "${HOME_DIR}" --node "${NODE}"
sleep 1.5

DEAL="$(curl -sf "http://${REST}/vaultchain/storage/deals?cid=${ROOT_HEX}" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['deals'][0]['id'])")"
echo "DEAL_ID=${DEAL}"
curl -sf -X POST --data-binary "@${FILE}" "http://127.0.0.1${PROV_LISTEN}/v1/objects/${ROOT_HEX}" >/dev/null
echo "uploaded"
# This provider runs without -home, so it neither accepts nor proves by itself:
# accept the assigned deal (pending -> open), then wait for the first challenge
# epoch and answer it by hand.
./build/vaultd tx storage accept-deal "${DEAL}" --from provider --home "${HOME_DIR}" --node "${NODE}"
sleep 1.5
for _ in $(seq 1 60); do
  curl -sf "http://${REST}/vaultchain/storage/challenge/${DEAL}?provider=${PROV}" >/dev/null && break
  sleep 0.5
done
curl -sf "http://${REST}/vaultchain/storage/challenge/${DEAL}?provider=${PROV}"; echo
./build/vaultd tx storage submit-proof "${DEAL}" --provider-url "http://127.0.0.1${PROV_LISTEN}" --from provider --home "${HOME_DIR}" --node "${NODE}"
sleep 1.5
curl -sf "http://${REST}/vaultchain/storage/deals?cid=${ROOT_HEX}" | python3 -c "import sys,json; d=json.load(sys.stdin)['deals'][0]; r=d['replicas'][0]; assert d['status']=='open' and r['last_proven_epoch']>0, d; print('proven in epoch', r['last_proven_epoch'])"
./build/vaultd tx ibc-lab mint-voucher "${ALICE}" ibc/ATOM 1000000 --from alice --home "${HOME_DIR}" --node "${NODE}"
sleep 1

echo "=== manifests ==="; curl -sf "http://${REST}/vaultchain/storage/manifests"; echo
echo "=== deals ==="; curl -sf "http://${REST}/vaultchain/storage/deals"; echo
echo "=== proofs ==="; curl -sf "http://${REST}/vaultchain/storage/proofs"; echo
echo "=== plans ==="; curl -sf "http://${REST}/vaultchain/storage/plans"; echo
echo "=== balances alice ==="; curl -sf "http://${REST}/cosmos/bank/v1beta1/balances/${ALICE}"; echo

echo "=== download (full) ==="
curl -sf "http://127.0.0.1${PROV_LISTEN}/v1/objects/${ROOT_HEX}" -o /tmp/vault-int-got.bin
cmp "${FILE}" /tmp/vault-int-got.bin
echo "=== download (range 262000-263000, crosses a chunk boundary) ==="
curl -sf -H "Range: bytes=262000-263000" "http://127.0.0.1${PROV_LISTEN}/v1/objects/${ROOT_HEX}" -o /tmp/vault-int-range.bin
tail -c +262001 "${FILE}" | head -c 1001 | cmp - /tmp/vault-int-range.bin
echo INT_OK