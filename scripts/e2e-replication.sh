#!/usr/bin/env bash
# Live replication + failure test: one deal, replication 2, across two providers.
#
#   NODE=https://vaultl1-devnet.fly.dev \
#   PROVIDER_URL_1=https://vaultl1-provider.fly.dev PROVIDER_URL_2=https://vaultl1-provider-2.fly.dev \
#   APP_2=vaultl1-provider-2 FAILURE_TEST=1 scripts/e2e-replication.sh
#
# Needs build/vaultd, curl, python3, sha256sum, and the keys/funding of
# scripts/e2e-remote.sh (alice in $HOME_DIR). FAILURE_TEST=1 also needs flyctl
# (FLY, default "fly") logged in: it STOPS provider 2's machine to miss proofs,
# first briefly (counter must recover), then long enough to be failed and
# slashed, and always starts the machine again before exiting.
#
# Epoch = proof_interval_blocks (10) x block time (3s) = ~30s; a replica fails
# after max_missed_proofs (3) consecutive missed epochs.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
NODE="${NODE:-https://vaultl1-devnet.fly.dev}"
P1_URL="${PROVIDER_URL_1:-https://vaultl1-provider.fly.dev}"
P2_URL="${PROVIDER_URL_2:-https://vaultl1-provider-2.fly.dev}"
APP_2="${APP_2:-vaultl1-provider-2}"
FLY="${FLY:-fly}"
HOME_DIR="${HOME_DIR:-${HOME}/.vaultl1-e2e}"
CHAIN_ID="${CHAIN_ID:-vault-devnet-1}"
FILE_KB="${FILE_KB:-1024}"
V=./build/vaultd
[[ -x "${V}" ]] || { echo "build ${V} first"; exit 1; }

PASS=0; FAIL=0
ok()   { echo "PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "FAIL  $*"; FAIL=$((FAIL+1)); }
check(){ local n="$1"; shift; if "$@"; then ok "$n"; else bad "$n"; fi; }
code() { curl -s -o /dev/null -w "%{http_code}" "$@"; }
jq_()  { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }
tx()   { "${V}" tx "$@" --home "${HOME_DIR}" --node "${NODE}" --chain-id "${CHAIN_ID}"; }

ALICE="$("${V}" keys show alice --home "${HOME_DIR}")" || { echo "run scripts/e2e-remote.sh once to create and fund alice"; exit 2; }
A1="$(curl -sf "${P1_URL}/status" | jq_ "d['address']")"
A2="$(curl -sf "${P2_URL}/status" | jq_ "d['address']")"
echo "provider1=${A1} provider2=${A2}"
bal() { curl -sf "${NODE}/cosmos/bank/v1beta1/balances/$1" | jq_ "next((int(b['amount']) for b in d['balances'] if b['denom']=='uvault'),0)"; }
collateral() { curl -sf "${NODE}/vaultchain/storage/providers/$1" | jq_ "d['provider']['collateral']['amount']"; }

MACHINE=""
if [[ "${FAILURE_TEST:-0}" = 1 ]]; then
  MACHINE="$("${FLY}" machine list -a "${APP_2}" --json | jq_ "d[0]['id']" | tr -d '\r')"
  echo "provider 2 machine: ${MACHINE}"
  trap '"${FLY}" machine start "${MACHINE}" -a "${APP_2}" >/dev/null 2>&1 || true' EXIT
fi

W="$(mktemp -d)"
head -c "$((FILE_KB*1024))" /dev/urandom >"${W}/rep.bin"
f="${W}/rep.bin"
root="$("${V}" tx storage commit-manifest "${f}" --mime application/octet-stream --name rep.bin --dry-run | jq_ "d['root']")"
size="$(stat -c %s "${f}")"
echo "== root=${root} size=${size}"
check "commit-manifest" tx storage commit-manifest "${f}" --mime application/octet-stream --name rep.bin --from alice
sleep 5
check "open-deal replication=2 (pinned first to provider 1)" tx storage open-deal market "${root}" "${size}" "${A1}" 10000 uvault --replication 2 --from alice
sleep 6

deal_json() { curl -sf "${NODE}/vaultchain/storage/deals?cid=${root}"; }
# rep_field <provider> <field>
rep_field() { deal_json | jq_ "next((r.get('$2',0) for r in d['deals'][0]['replicas'] if r['provider']=='$1'),'none')"; }
show() { deal_json | jq_ "'deal %s status=%s | ' % (d['deals'][0]['id'], d['deals'][0]['status']) + ' | '.join('%s... %s proven_epoch=%s missed=%s' % (r['provider'][:10], r['status'], r.get('last_proven_epoch',0), r.get('missed_proofs',0)) for r in d['deals'][0]['replicas'])"; }
show

check "chain assigned both providers" bash -c "[[ \"\$(curl -sf '${NODE}/vaultchain/storage/deals?cid=${root}' | python3 -c \"import sys,json; print(sorted(r['provider'] for r in json.load(sys.stdin)['deals'][0]['replicas']))\")\" == \"\$(python3 -c \"print(sorted(['${A1}','${A2}']))\")\" ]]"
check "deal is pending before any upload" bash -c "[[ \"\$(curl -sf '${NODE}/vaultchain/storage/deals?cid=${root}' | python3 -c \"import sys,json; print(json.load(sys.stdin)['deals'][0]['status'])\")\" == pending ]]"
for u in "${P1_URL}" "${P2_URL}"; do
  check "upload to ${u}" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST --data-binary '@${f}' '${u}/v1/objects/${root}') =~ ^2 ]]"
done

st=""
for _ in $(seq 1 60); do
  st="$(deal_json | jq_ "d['deals'][0]['status']")"
  [[ "${st}" == open && "$(rep_field "${A1}" last_proven_epoch)" != 0 && "$(rep_field "${A2}" last_proven_epoch)" != 0 ]] && break
  sleep 3
done
show
check "both providers accepted (deal open)" [ "${st}" = open ]
check "provider 1 proved" [ "$(rep_field "${A1}" last_proven_epoch)" != 0 ]
check "provider 2 proved" [ "$(rep_field "${A2}" last_proven_epoch)" != 0 ]

for pair in "1 ${A1} ${P1_URL}" "2 ${A2} ${P2_URL}"; do
  set -- ${pair}
  tok="$("${V}" read-token "${root}" --provider "$2" --from alice --home "${HOME_DIR}")"
  curl -sf -H "Authorization: Vault ${tok}" "$3/v1/objects/${root}" -o "${W}/got$1"
  check "download from provider $1 matches sha256" [ "$(sha256sum <"${f}")" = "$(sha256sum <"${W}/got$1")" ]
done

if [[ "${FAILURE_TEST:-0}" != 1 ]]; then
  echo "RESULT: ${PASS} passed, ${FAIL} failed (set FAILURE_TEST=1 for the outage phases)"; exit $((FAIL>0))
fi

stop2()  { "${FLY}" machine stop "${MACHINE}" -a "${APP_2}" 2>&1 | tail -1; }
start2() { "${FLY}" machine start "${MACHINE}" -a "${APP_2}" 2>&1 | tail -1; }

echo; echo "== phase B: short outage (counter must recover)"
stop2
for _ in $(seq 1 40); do
  m="$(rep_field "${A2}" missed_proofs)"; [[ "${m}" -ge 1 ]] && break; sleep 5
done
show
check "provider 2 missed epoch counted (missed>=1)" [ "${m}" -ge 1 ]
check "replica still active after a short outage" [ "$(rep_field "${A2}" status)" = active ]
start2
for _ in $(seq 1 40); do
  m="$(rep_field "${A2}" missed_proofs)"; [[ "${m}" == 0 ]] && break; sleep 5
done
show
check "missed counter reset after provider 2 proved again" [ "${m}" == 0 ]

echo; echo "== phase C: long outage (fail + slash)"
col0="$(collateral "${A2}")"; bal0="$(bal "${ALICE}")"
echo "  provider 2 collateral ${col0}; alice balance ${bal0}"
stop2
last=""
for _ in $(seq 1 80); do
  s="$(rep_field "${A2}" status)"; m="$(rep_field "${A2}" missed_proofs)"
  [[ "${m}:${s}" != "${last}" ]] && { echo "  $(date +%T) missed=${m} status=${s}"; last="${m}:${s}"; }
  [[ "${s}" == failed ]] && break; sleep 5
done
show
check "provider 2 replica failed after max missed epochs" [ "$(rep_field "${A2}" status)" = failed ]
col1="$(collateral "${A2}")"; bal1="$(bal "${ALICE}")"
echo "  provider 2 collateral ${col0} -> ${col1}; alice balance ${bal0} -> ${bal1}"
check "provider 2 slashed 1/10 of collateral" [ "$((col0 - col1))" = "$((col0 / 10))" ]
check "slash credited to the deal owner (alice, net of her fees)" [ "$((bal1 - bal0))" -gt 0 ]
check "deal stays open on the surviving replica" [ "$(deal_json | jq_ "d['deals'][0]['status']")" = open ]
sleep 35; show
check "provider 1 keeps proving" [ "$(rep_field "${A1}" missed_proofs)" = 0 ]
check "no replacement exists with only two providers (still 2 replicas)" [ "$(deal_json | jq_ "len(d['deals'][0]['replicas'])")" = 2 ]
tok="$("${V}" read-token "${root}" --provider "${A1}" --from alice --home "${HOME_DIR}")"
curl -sf -H "Authorization: Vault ${tok}" "${P1_URL}/v1/objects/${root}" -o "${W}/got1b"
check "download from surviving provider 1 still matches" [ "$(sha256sum <"${f}")" = "$(sha256sum <"${W}/got1b")" ]

echo; echo "== restore provider 2"
start2
for _ in $(seq 1 30); do [[ "$(code "${P2_URL}/status")" = 200 ]] && break; sleep 3; done
check "provider 2 healthy again" [ "$(code "${P2_URL}/status")" = 200 ]
check "provider 2 still registered and active on chain" [ "$(curl -sf "${NODE}/vaultchain/storage/providers/${A2}" | jq_ "d['provider']['status']")" = active ]
show
echo "RESULT: ${PASS} passed, ${FAIL} failed"
exit $((FAIL>0))