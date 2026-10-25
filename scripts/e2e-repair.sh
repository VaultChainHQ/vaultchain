#!/usr/bin/env bash
# Live REPAIR / reassignment test: three providers, one replication-2 deal.
#
#   NODE=https://vaultl1-devnet.fly.dev \
#   PROVIDER_URLS="https://vaultl1-provider.fly.dev https://vaultl1-provider-2.fly.dev https://vaultl1-provider-3.fly.dev" \
#   scripts/e2e-repair.sh
#
# The deal is pinned to the FIRST provider; the chain assigns the second slot to
# one of the others (the "victim"); the remaining one is the "spare". The script
# stops the victim's Fly machine until its replica fails (3 missed epochs, ~2 min,
# slashed 1/10), then checks that the chain reassigns the slot to the spare, that
# the spare fetches the object from the healthy replica, accepts, proves, and
# serves a hash-identical download. The victim's machine is always started again.
# Needs build/vaultd, curl, python3, sha256sum, flyctl (FLY) logged in, and alice
# funded (see scripts/e2e-remote.sh).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
NODE="${NODE:-https://vaultl1-devnet.fly.dev}"
URLS=(${PROVIDER_URLS:-https://vaultl1-provider.fly.dev https://vaultl1-provider-2.fly.dev https://vaultl1-provider-3.fly.dev})
FLY="${FLY:-fly}"
HOME_DIR="${HOME_DIR:-${HOME}/.vaultl1-e2e}"
CHAIN_ID="${CHAIN_ID:-vault-devnet-1}"
FILE_KB="${FILE_KB:-1024}"
V=./build/vaultd
[[ -x "${V}" ]] || { echo "build ${V} first"; exit 1; }
[[ ${#URLS[@]} -ge 3 ]] || { echo "need 3 provider URLs"; exit 1; }

PASS=0; FAIL=0
ok()   { echo "PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "FAIL  $*"; FAIL=$((FAIL+1)); }
check(){ local n="$1"; shift; if "$@"; then ok "$n"; else bad "$n"; fi; }
jq_()  { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }
tx()   { "${V}" tx "$@" --home "${HOME_DIR}" --node "${NODE}" --chain-id "${CHAIN_ID}"; }

ADDR=(); for u in "${URLS[@]}"; do ADDR+=("$(curl -sf "${u}/status" | jq_ "d['address']")"); done
echo "providers: ${ADDR[*]}"
A1="${ADDR[0]}"
VICTIM=""; MACHINE=""; VAPP=""
trap '[[ -n "${MACHINE}" ]] && "${FLY}" machine start "${MACHINE}" -a "${VAPP}" >/dev/null 2>&1 || true' EXIT

W="$(mktemp -d)"; f="${W}/rep.bin"
head -c "$((FILE_KB*1024))" /dev/urandom >"${f}"
root="$("${V}" tx storage commit-manifest "${f}" --mime application/octet-stream --name rep.bin --dry-run | jq_ "d['root']")"
size="$(stat -c %s "${f}")"
echo "== root=${root} size=${size}"
check "commit-manifest" tx storage commit-manifest "${f}" --mime application/octet-stream --name rep.bin --from alice
sleep 5
check "open-deal replication=2 pinned to provider 1" tx storage open-deal market "${root}" "${size}" "${A1}" 10000 uvault --replication 2 --from alice
sleep 6

deal_json() { curl -sf "${NODE}/vaultchain/storage/deals?cid=${root}"; }
rep_field() { deal_json | jq_ "next((r.get('$2',0) for r in d['deals'][0]['replicas'] if r['provider']=='$1' and r['status'] in ($3)),'none')"; }
show() { deal_json | jq_ "'deal %s status=%s | ' % (d['deals'][0]['id'], d['deals'][0]['status']) + ' | '.join('slot%s %s... %s proven=%s missed=%s' % (r['slot'], r['provider'][:10], r['status'], r.get('last_proven_epoch',0), r.get('missed_proofs',0)) for r in d['deals'][0]['replicas'])"; }
show
VICTIM="$(deal_json | jq_ "next(r['provider'] for r in d['deals'][0]['replicas'] if r['provider']!='${A1}')")"
SPARE=""; VURL=""; SURL=""
for i in "${!ADDR[@]}"; do
  [[ "${i}" == 0 ]] && continue
  if [[ "${ADDR[$i]}" == "${VICTIM}" ]]; then VURL="${URLS[$i]}"; else SPARE="${ADDR[$i]}"; SURL="${URLS[$i]}"; fi
done
VAPP="$(echo "${VURL}" | sed -E 's#https?://([^.]+)\..*#\1#')"
echo "victim=${VICTIM} (${VAPP}) spare=${SPARE}"
check "victim and spare identified" [ -n "${VICTIM}" -a -n "${SPARE}" ]

for u in "${URLS[0]}" "${VURL}"; do
  check "upload to ${u}" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST --data-binary '@${f}' '${u}/v1/objects/${root}') =~ ^2 ]]"
done
for _ in $(seq 1 60); do
  [[ "$(rep_field "${A1}" last_proven_epoch "'active'")" != 0 && "$(rep_field "${VICTIM}" last_proven_epoch "'active'")" != 0 && "$(rep_field "${A1}" last_proven_epoch "'active'")" != none && "$(rep_field "${VICTIM}" last_proven_epoch "'active'")" != none ]] && break
  sleep 3
done
show
check "provider 1 and victim both proving" [ "$(rep_field "${VICTIM}" last_proven_epoch "'active'")" != 0 ]

MACHINE="$("${FLY}" machine list -a "${VAPP}" --json | jq_ "d[0]['id']" | tr -d '\r')"
echo "== stopping victim machine ${MACHINE} (${VAPP})"
"${FLY}" machine stop "${MACHINE}" -a "${VAPP}" 2>&1 | tail -1
last=""
for _ in $(seq 1 90); do
  s="$(deal_json | jq_ "' '.join('%s:%s:%s' % (r['provider'][:8], r['status'], r.get('missed_proofs',0)) for r in d['deals'][0]['replicas'])")"
  [[ "${s}" != "${last}" ]] && { echo "  $(date +%T) ${s}"; last="${s}"; }
  [[ "$(rep_field "${SPARE}" status "'assigned','active'")" != none ]] && break
  sleep 5
done
show
check "victim replica failed" bash -c "[[ \"\$(curl -sf '${NODE}/vaultchain/storage/deals?cid=${root}' | python3 -c \"import sys,json; print(next(r['status'] for r in json.load(sys.stdin)['deals'][0]['replicas'] if r['provider']=='${VICTIM}'))\")\" == failed ]]"
check "slot reassigned to the spare provider" [ "$(rep_field "${SPARE}" status "'assigned','active'")" != none ]
for _ in $(seq 1 40); do
  [[ "$(rep_field "${SPARE}" status "'active'")" == active ]] && break; sleep 3
done
check "spare accepted after fetching from the healthy replica" [ "$(rep_field "${SPARE}" status "'active'")" = active ]
for _ in $(seq 1 40); do
  p="$(rep_field "${SPARE}" last_proven_epoch "'active'")"; [[ "${p}" != 0 && "${p}" != none ]] && break; sleep 5
done
show
check "spare is proving" [ "${p}" != 0 -a "${p}" != none ]
check "spare holds the object" [ "$(curl -sf "${SURL}/status" | jq_ "d['objects']")" -ge 1 ]
for pair in "1 ${A1} ${URLS[0]}" "spare ${SPARE} ${SURL}"; do
  set -- ${pair}
  tok="$("${V}" read-token "${root}" --provider "$2" --from alice --home "${HOME_DIR}")"
  curl -sf -H "Authorization: Vault ${tok}" "$3/v1/objects/${root}" -o "${W}/got$1"
  check "download from provider $1 matches sha256" [ "$(sha256sum <"${f}")" = "$(sha256sum <"${W}/got$1")" ]
done
check "deal still open with two active replicas" [ "$(deal_json | jq_ "d['deals'][0]['status']+str(sum(1 for r in d['deals'][0]['replicas'] if r['status']=='active'))")" = open2 ]

echo "== restoring victim"
"${FLY}" machine start "${MACHINE}" -a "${VAPP}" 2>&1 | tail -1
for _ in $(seq 1 30); do [[ "$(curl -s -o /dev/null -w '%{http_code}' "${VURL}/status")" = 200 ]] && break; sleep 3; done
check "victim healthy again" [ "$(curl -s -o /dev/null -w '%{http_code}' "${VURL}/status")" = 200 ]
echo "RESULT: ${PASS} passed, ${FAIL} failed"
exit $((FAIL>0))
