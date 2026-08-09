#!/usr/bin/env bash
# Start Vault Lab UI against local vaultd + vault-provider.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="/usr/local/go/bin:${HOME}/go/bin:${ROOT}/build:${PATH}"

HOME_DIR="${VAULTCHAIN_HOME:-${HOME}/.vaultchain}"
LISTEN="${VAULT_LAB_LISTEN:-:3456}"

mkdir -p "${ROOT}/build"
if [[ ! -x "${ROOT}/build/vault-lab" ]]; then
  (cd "${ROOT}" && go build -o "${ROOT}/build/vault-lab" ./tools/vault-lab)
fi

IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo "Vault Lab → http://127.0.0.1${LISTEN}"
if [[ -n "${IP}" ]]; then
  echo "From Windows browser try: http://${IP}${LISTEN}"
fi

exec "${ROOT}/build/vault-lab" \
  -listen "${LISTEN}" \
  -home "${HOME_DIR}" \
  -vaultd "${ROOT}/build/vaultd" \
  -chain-id vault-local-1 \
  -rpc http://127.0.0.1:26657 \
  -rest http://127.0.0.1:1317 \
  -provider http://127.0.0.1:3333 \
  -key alice \
  -provider-key provider
