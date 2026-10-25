#!/usr/bin/env bash
# End-to-end test against a live devnet + provider (e.g. the Fly.io deployment).
#
#   NODE=https://vaultl1-devnet.fly.dev PROVIDER_URL=https://vaultl1-provider.fly.dev \
#     scripts/e2e-remote.sh
#
# Needs build/vaultd (go build -o build/vaultd ./cmd/vaultd), curl, python3, sha256sum.
# Creates keys alice and bob in $HOME_DIR on first run and exits with the addresses
# to fund (from the validator, see docs/FLY-DEVNET.md). Run it again once funded.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
NODE="${NODE:-https://vaultl1-devnet.fly.dev}"
PROVIDER_URL="${PROVIDER_URL:-https://vaultl1-provider.fly.dev}"
HOME_DIR="${HOME_DIR:-${HOME}/.vaultl1-e2e}"
CHAIN_ID="${CHAIN_ID:-vault-devnet-1}"
FILE_MB="${FILE_MB:-3}"
V=./build/vaultd
[[ -x "${V}" ]] || { echo "build ${V} first"; exit 1; }

PASS=0; FAIL=0
ok()   { echo "PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "FAIL  $*"; FAIL=$((FAIL+1)); }
check(){ local n="$1"; shift; if "$@"; then ok "$n"; else bad "$n"; fi; }
code() { curl -s -o /dev/null -w "%{http_code}" "$@"; }
jq_()  { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }
tx()   { "${V}" tx "$@" --home "${HOME_DIR}" --node "${NODE}" --chain-id "${CHAIN_ID}"; }

if [[ ! -d "${HOME_DIR}/config" ]]; then "${V}" init e2e --home "${HOME_DIR}" --chain-id "${CHAIN_ID}" --lab-mode >/dev/null; fi
for k in alice bob; do
  "${V}" keys show "${k}" --home "${HOME_DIR}" >/dev/null 2>&1 || "${V}" keys add "${k}" --home "${HOME_DIR}" >/dev/null
done
ALICE="$("${V}" keys show alice --home "${HOME_DIR}")"
BOB="$("${V}" keys show bob --home "${HOME_DIR}")"
PROV="$(curl -sf "${PROVIDER_URL}/status" | jq_ "d['address']")"
bal() { curl -sf "${NODE}/cosmos/bank/v1beta1/balances/$1" | jq_ "next((int(b['amount']) for b in d['balances'] if b['denom']=='uvault'),0)"; }
echo "node=${NODE} provider=${PROVIDER_URL} (${PROV})"
echo "alice=${ALICE} bob=${BOB}"
for a in "${ALICE}" "${BOB}"; do
  if (( $(bal "${a}") < 500000000 )); then
    echo "fund ${a} with e.g. 2000000000uvault from the validator, then re-run"; exit 2
  fi
done

check "node height advances" bash -c '
  h1=$(curl -sf "'"${NODE}"'/cosmos/base/tendermint/v1beta1/node_info" | python3 -c "import sys,json;print(json.load(sys.stdin)[\"height\"])"); sleep 7
  h2=$(curl -sf "'"${NODE}"'/cosmos/base/tendermint/v1beta1/node_info" | python3 -c "import sys,json;print(json.load(sys.stdin)[\"height\"])"); echo "  height $h1 -> $h2"; (( h2 > h1 ))'
check "provider registered and active on chain" bash -c \
  'curl -sf "'"${NODE}"'/vaultchain/storage/providers/'"${PROV}"'" | python3 -c "import sys,json; p=json.load(sys.stdin)[\"provider\"]; print(\"  \", p[\"status\"], p[\"endpoint\"]); assert p[\"status\"]==\"active\""'

tx storage buy-capacity "$((1<<30))" 10000 uvault --from alice >/dev/null && sleep 5

W="$(mktemp -d)"
head -c "$((FILE_MB*1024*1024))" /dev/urandom >"${W}/big.bin"
printf 'hello vaultl1 e2e %s\nthis is a small text file\n' "$(date -u +%FT%TZ)" >"${W}/note.txt"

run_object() { # name mime
  local f="${W}/$1" name="$1" mime="$2"
  local root size
  root="$("${V}" tx storage commit-manifest "${f}" --mime "${mime}" --name "${name}" --dry-run | jq_ "d['root']")"
  size="$(stat -c %s "${f}")"
  echo "== ${name}: root=${root} size=${size}"
  check "${name}: commit-manifest" tx storage commit-manifest "${f}" --mime "${mime}" --name "${name}" --from alice
  sleep 5
  check "${name}: manifest visible on chain" bash -c "curl -sf '${NODE}/vaultchain/storage/manifests/${root}' >/dev/null"
  check "${name}: open-deal (pinned to provider)" tx storage open-deal market "${root}" "${size}" "${PROV}" 10000 uvault --from alice
  sleep 6
  local deal
  deal="$(curl -sf "${NODE}/vaultchain/storage/deals?cid=${root}" | jq_ "d['deals'][0]['id']")"
  echo "  deal=${deal}"
  check "${name}: upload to provider" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST --data-binary '@${f}' '${PROVIDER_URL}/v1/objects/${root}') =~ ^2 ]]"
  # The provider accepts the deal and proves by itself (it runs with -home/-key).
  local st="" ep=0
  for _ in $(seq 1 60); do
    read -r st ep < <(curl -sf "${NODE}/vaultchain/storage/deals?cid=${root}" | jq_ "d['deals'][0]['status'], d['deals'][0]['replicas'][0].get('last_proven_epoch',0)")
    [[ "${st}" == open && "${ep}" != 0 ]] && break
    sleep 3
  done
  echo "  deal status=${st} last_proven_epoch=${ep}"
  check "${name}: provider auto-accepted (deal open)" [ "${st}" = open ]
  check "${name}: automatic Merkle proof accepted on chain" [ "${ep}" != 0 ]

  local obj="${PROVIDER_URL}/v1/objects/${root}"
  check "${name}: anonymous read refused (401)" [ "$(code "${obj}")" = 401 ]
  local tb tok
  tb="$("${V}" read-token "${root}" --provider "${PROV}" --from bob --home "${HOME_DIR}")"
  check "${name}: reader without access refused (403)" [ "$(code -H "Authorization: Vault ${tb}" "${obj}")" = 403 ]
  tok="$("${V}" read-token "${root}" --provider "${PROV}" --from alice --home "${HOME_DIR}")"
  curl -sf -H "Authorization: Vault ${tok}" "${obj}" -o "${W}/got-${name}"
  check "${name}: owner download sha256 matches" [ "$(sha256sum <"${f}")" = "$(sha256sum <"${W}/got-${name}")" ]
  if (( size > 270000 )); then
    local from=262000 to=263000
    curl -sf -H "Authorization: Vault ${tok}" -H "Range: bytes=${from}-${to}" "${obj}" -o "${W}/r1"
    check "${name}: range ${from}-${to} (chunk boundary) bytes match" bash -c "[[ \$(stat -c %s '${W}/r1') == 1001 ]] && cmp -s -n 1001 -i ${from}:0 '${f}' '${W}/r1'"
    from=$((size/2)); to=$((from+99999))
    curl -sf -H "Authorization: Vault ${tok}" -H "Range: bytes=${from}-${to}" "${obj}" -o "${W}/r2"
    check "${name}: range mid-file bytes match" bash -c "[[ \$(stat -c %s '${W}/r2') == 100000 ]] && cmp -s -n 100000 -i ${from}:0 '${f}' '${W}/r2'"
  fi
  check "${name}: grant bob read" tx access grant "${root}" "${BOB}" read 0 --from alice
  local c=0
  for _ in $(seq 1 20); do c="$(code -H "Authorization: Vault ${tb}" "${obj}")"; [[ "${c}" == 200 ]] && break; sleep 1; done
  check "${name}: granted reader allowed (200)" [ "${c}" = 200 ]
  check "${name}: anonymous still refused before public grant" [ "$(code "${obj}")" = 401 ]
  check "${name}: public grant (*)" tx access grant "${root}" "*" read 0 --from alice
  c=0
  for _ in $(seq 1 20); do c="$(code "${obj}")"; [[ "${c}" == 200 ]] && break; sleep 1; done
  check "${name}: anonymous read allowed after public grant" [ "${c}" = 200 ]
  curl -sf "${obj}" -o "${W}/pub-${name}"
  check "${name}: public download sha256 matches" [ "$(sha256sum <"${f}")" = "$(sha256sum <"${W}/pub-${name}")" ]
}

run_object note.txt text/plain
run_object big.bin application/octet-stream

echo; echo "RESULT: ${PASS} passed, ${FAIL} failed"
[[ "${FAIL}" == 0 ]]