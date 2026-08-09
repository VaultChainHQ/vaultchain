#!/usr/bin/env bash
# Start vault-provider against a running vault-local-1 node.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="/usr/local/go/bin:${HOME}/go/bin:${ROOT}/build:${PATH}"

HOME_DIR="${VAULTCHAIN_HOME:-${HOME}/.vaultchain}"
REST="${VAULT_REST:-http://127.0.0.1:1317}"
LISTEN="${VAULT_PROVIDER_LISTEN:-:3333}"
DATA="${VAULT_PROVIDER_DATA:-${HOME}/.vault-provider/data}"

mkdir -p "${ROOT}/build"
if [[ ! -x "${ROOT}/build/vault-provider" ]]; then
  (cd "${ROOT}" && go build -o "${ROOT}/build/vault-provider" ./tools/vault-provider)
fi
if [[ ! -x "${ROOT}/build/vaultd" ]]; then
  (cd "${ROOT}" && go build -o "${ROOT}/build/vaultd" ./cmd/vaultd)
fi

# Ensure provider key exists and is registered on-chain.
if ! "${ROOT}/build/vaultd" keys show provider --home "${HOME_DIR}" >/dev/null 2>&1; then
  "${ROOT}/build/vaultd" keys add provider --home "${HOME_DIR}"
fi
PROV="$("${ROOT}/build/vaultd" keys show provider --home "${HOME_DIR}" | tr -d '\r' | tail -n1)"
ALICE="$("${ROOT}/build/vaultd" keys show alice --home "${HOME_DIR}" | tr -d '\r' | tail -n1)"

# Fund + register if not already a provider.
if ! curl -sf "${REST}/vaultchain/storage/providers/${PROV}" >/dev/null 2>&1; then
  echo "Funding and registering provider ${PROV}..."
  "${ROOT}/build/vaultd" tx bank-send "${PROV}" "5000000000000uvault" --from alice --home "${HOME_DIR}" --node "${REST}" || true
  sleep 2
  "${ROOT}/build/vaultd" tx storage register-provider "http://127.0.0.1${LISTEN}" "$((10*1024*1024*1024))" "1000000000uvault" \
    --from provider --home "${HOME_DIR}" --node "${REST}" || true
  sleep 2
fi

echo "Starting vault-provider on ${LISTEN} as ${PROV}"
exec "${ROOT}/build/vault-provider" \
  -address "${PROV}" \
  -listen "${LISTEN}" \
  -data "${DATA}" \
  -rest "${REST}" \
  -chain-id vault-local-1
