#!/usr/bin/env bash
# End-to-end test of encrypted files and revocation against a live devnet + provider.
#
#   NODE=https://vaultl1-devnet.fly.dev PROVIDER_URL=https://vaultl1-provider.fly.dev \
#     scripts/e2e-private.sh
#
# Same setup as scripts/e2e-remote.sh (build/vaultd, curl, python3, funded alice and bob in
# $HOME_DIR). Covers: client-side encryption, ciphertext-only storage at the provider, owner
# decryption, tamper detection, key wrapping to a grantee, grantee decryption, revocation
# (provider refuses within seconds, the key envelope is gone from chain state) and the refusal
# to make an encrypted file public.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
NODE="${NODE:-https://vaultl1-devnet.fly.dev}"
PROVIDER_URL="${PROVIDER_URL:-https://vaultl1-provider.fly.dev}"
HOME_DIR="${HOME_DIR:-${HOME}/.vaultl1-e2e}"
CHAIN_ID="${CHAIN_ID:-vault-devnet-1}"
V=./build/vaultd
[[ -x "${V}" ]] || { echo "build ${V} first"; exit 1; }

PASS=0; FAIL=0
ok()   { echo "PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "FAIL  $*"; FAIL=$((FAIL+1)); }
check(){ local n="$1"; shift; if "$@"; then ok "$n"; else bad "$n"; fi; }
code() { curl -s -o /dev/null -w "%{http_code}" "$@"; }
jq_()  { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }
tx()   { "${V}" tx "$@" --home "${HOME_DIR}" --node "${NODE}" --chain-id "${CHAIN_ID}"; }
bal() { curl -sf "${NODE}/cosmos/bank/v1beta1/balances/$1" | jq_ "next((int(b['amount']) for b in d['balances'] if b['denom']=='uvault'),0)"; }

if [[ ! -d "${HOME_DIR}/config" ]]; then "${V}" init e2e --home "${HOME_DIR}" --chain-id "${CHAIN_ID}" --lab-mode >/dev/null; fi
for k in alice bob; do
  "${V}" keys show "${k}" --home "${HOME_DIR}" >/dev/null 2>&1 || "${V}" keys add "${k}" --home "${HOME_DIR}" >/dev/null
done
ALICE="$("${V}" keys show alice --home "${HOME_DIR}")"
BOB="$("${V}" keys show bob --home "${HOME_DIR}")"
PROV="$(curl -sf "${PROVIDER_URL}/status" | jq_ "d['address']")"
echo "node=${NODE} provider=${PROVIDER_URL} (${PROV})"
echo "alice=${ALICE} bob=${BOB}"
for a in "${ALICE}" "${BOB}"; do
  if (( $(bal "${a}") < 500000000 )); then
    echo "fund ${a} with e.g. 2000000000uvault from the validator, then re-run"; exit 2
  fi
done

W="$(mktemp -d)"
MARK="TOP-SECRET-MARKER-$(date +%s)"
{ for _ in $(seq 1 3000); do echo "${MARK} the launch code is 0000"; done; head -c 700000 /dev/urandom | base64; } >"${W}/secret.txt"
SIZE_PLAIN="$(stat -c %s "${W}/secret.txt")"

echo "== bob's public key must be on chain before alice can wrap a key for him"
tx bank-send "${ALICE}" 1uvault --from bob >/dev/null; sleep 5
check "bob's pubkey is in the on-chain registry" bash -c "curl -sf '${NODE}/vaultchain/auth/pubkeys/${BOB}' | python3 -c 'import sys,json; assert json.load(sys.stdin)[\"pubkey\"]'"

echo "== alice encrypts and commits"
OUT="$(tx storage commit-manifest "${W}/secret.txt" --mime text/plain --name secret.txt --encrypt --cipher-out "${W}/secret.vle" --from alice 2>&1)"
RT="$(printf '%s\n' "${OUT}" | grep -o 'root=[0-9a-f]\{64\}' | head -1 | cut -d= -f2)"
CSIZE="$(stat -c %s "${W}/secret.vle" 2>/dev/null || echo 0)"
echo "  root=${RT} plaintext=${SIZE_PLAIN} ciphertext=${CSIZE}"
check "commit-manifest --encrypt produced a root and larger ciphertext" bash -c "[[ -n '${RT}' && ${CSIZE} -gt ${SIZE_PLAIN} ]]"
[[ -n "${RT}" ]] || { echo "cannot continue without a root"; echo "${OUT}"; exit 1; }
check "ciphertext does not contain the plaintext marker" bash -c "! grep -qa '${MARK}' '${W}/secret.vle'"
sleep 5
check "manifest on chain is marked encrypted over the ciphertext size" bash -c "curl -sf '${NODE}/vaultchain/storage/manifests/${RT}' | python3 -c 'import sys,json; m=json.load(sys.stdin)[\"manifest\"]; assert m[\"encryption\"]==\"vle1\" and m[\"key_envelope\"] and m[\"size\"]==${CSIZE}'"

echo "== deal and upload (ciphertext only)"
check "open-deal pinned to provider" tx storage open-deal market "${RT}" "${CSIZE}" "${PROV}" 10000 uvault --from alice
sleep 6
check "upload ciphertext to provider" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST --data-binary '@${W}/secret.vle' '${PROVIDER_URL}/v1/objects/${RT}') =~ ^2 ]]"
OBJ="${PROVIDER_URL}/v1/objects/${RT}"
for _ in $(seq 1 60); do
  read -r st ep < <(curl -sf "${NODE}/vaultchain/storage/deals?cid=${RT}" | jq_ "d['deals'][0]['status'], d['deals'][0]['replicas'][0].get('last_proven_epoch',0)")
  [[ "${st}" == open && "${ep}" != 0 ]] && break
  sleep 3
done
check "provider accepted the deal and proved the ciphertext" bash -c "[[ '${st:-}' == open && '${ep:-0}' != 0 ]]"

echo "== owner: download and decrypt"
TA="$("${V}" read-token "${RT}" --provider "${PROV}" --from alice --home "${HOME_DIR}")"
curl -sf -H "Authorization: Vault ${TA}" "${OBJ}" -o "${W}/dl-alice.vle"
check "provider serves exactly the ciphertext" cmp -s "${W}/secret.vle" "${W}/dl-alice.vle"
check "provider-stored bytes hide the plaintext" bash -c "! grep -qa '${MARK}' '${W}/dl-alice.vle'"
check "owner decrypts to the original" bash -c "'${V}' decrypt '${RT}' --from alice --home '${HOME_DIR}' --node '${NODE}' --in '${W}/dl-alice.vle' --out '${W}/alice.txt' && cmp -s '${W}/secret.txt' '${W}/alice.txt'"
cp "${W}/dl-alice.vle" "${W}/tampered.vle"
printf '\xff' | dd of="${W}/tampered.vle" bs=1 seek=$((CSIZE/2)) conv=notrunc 2>/dev/null
check "tampered ciphertext is rejected and writes no output" bash -c "! '${V}' decrypt '${RT}' --from alice --home '${HOME_DIR}' --node '${NODE}' --in '${W}/tampered.vle' --out '${W}/tampered.txt' 2>/dev/null && [[ ! -e '${W}/tampered.txt' ]]"
head -c $((CSIZE-20)) "${W}/dl-alice.vle" >"${W}/truncated.vle"
check "truncated ciphertext is rejected" bash -c "! '${V}' decrypt '${RT}' --from alice --home '${HOME_DIR}' --node '${NODE}' --in '${W}/truncated.vle' --out '${W}/truncated.txt' 2>/dev/null"

echo "== bob: no access, then a grant that carries the wrapped key"
TB="$("${V}" read-token "${RT}" --provider "${PROV}" --from bob --home "${HOME_DIR}")"
check "bob refused before the grant (403)" [ "$(code -H "Authorization: Vault ${TB}" "${OBJ}")" = 403 ]
check "bob has no key before the grant" bash -c "! '${V}' decrypt '${RT}' --from bob --home '${HOME_DIR}' --node '${NODE}' --in '${W}/dl-alice.vle' --out '${W}/bob0.txt' 2>/dev/null"
check "public grant of an encrypted file is refused" bash -c "! '${V}' tx access grant '${RT}' '*' read 0 --from alice --home '${HOME_DIR}' --node '${NODE}' --chain-id '${CHAIN_ID}' 2>/dev/null"
check "grant bob read (with key envelope)" tx access grant "${RT}" "${BOB}" read 0 --from alice
sleep 5
check "grant on chain carries a key envelope" bash -c "curl -sf '${NODE}/vaultchain/access/grants?cid=${RT}' | python3 -c 'import sys,json; g=[x for x in json.load(sys.stdin)[\"grants\"] if x[\"grantee\"]==\"${BOB}\"]; assert g and g[0][\"key_envelope\"]'"
c=0
for _ in $(seq 1 20); do c="$(code -H "Authorization: Vault ${TB}" "${OBJ}")"; [[ "${c}" == 200 ]] && break; sleep 1; done
check "bob allowed after the grant (200)" [ "${c}" = 200 ]
curl -sf -H "Authorization: Vault ${TB}" "${OBJ}" -o "${W}/dl-bob.vle"
check "bob decrypts to the original" bash -c "'${V}' decrypt '${RT}' --from bob --home '${HOME_DIR}' --node '${NODE}' --in '${W}/dl-bob.vle' --out '${W}/bob.txt' && cmp -s '${W}/secret.txt' '${W}/bob.txt'"

echo "== revoke"
check "alice revokes bob" tx access revoke "${RT}" "${BOB}" --from alice
c=0
for _ in $(seq 1 20); do c="$(code -H "Authorization: Vault ${TB}" "${OBJ}")"; [[ "${c}" == 403 ]] && break; sleep 1; done
check "revoked bob refused (403) even with a still-valid read token" [ "${c}" = 403 ]
check "key envelope is gone from chain state" bash -c "curl -sf '${NODE}/vaultchain/access/grants?cid=${RT}' | python3 -c 'import sys,json; assert not [x for x in json.load(sys.stdin)[\"grants\"] if x[\"grantee\"]==\"${BOB}\"]'"
check "bob can no longer fetch the key from chain" bash -c "! '${V}' decrypt '${RT}' --from bob --home '${HOME_DIR}' --node '${NODE}' --in '${W}/dl-alice.vle' --out '${W}/bob2.txt' 2>/dev/null"
check "owner unaffected by the revoke" [ "$(code -H "Authorization: Vault ${TA}" "${OBJ}")" = 200 ]
echo "  (documented limit: bob's copy of the ciphertext and any key he saved keep working; see docs/ENCRYPTION.md)"

echo; echo "RESULT: ${PASS} passed, ${FAIL} failed"
[[ "${FAIL}" == 0 ]]
