#!/usr/bin/env bash
# Initialize a local Vaultchain single-validator node (vault-local-1).
# Ports: RPC 26657, REST 1317
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"

HOME_DIR="${VAULTCHAIN_HOME:-${HOME}/.vaultchain}"
CHAIN_ID="vault-local-1"
BINARY="${ROOT}/build/vaultd"

if [[ ! -x "${BINARY}" ]]; then
  echo "Building vaultd..."
  mkdir -p "${ROOT}/build"
  (cd "${ROOT}" && go build -o "${BINARY}" ./cmd/vaultd)
fi

echo "Resetting home ${HOME_DIR}"
rm -rf "${HOME_DIR}"

"${BINARY}" init local-validator --home "${HOME_DIR}" --chain-id "${CHAIN_ID}" --lab-mode
"${BINARY}" keys add validator --home "${HOME_DIR}"
ADDR="$("${BINARY}" keys show validator --home "${HOME_DIR}" | tr -d '\r' | tail -n1)"
if [[ -z "${ADDR}" || "${ADDR}" != vault* ]]; then
  echo "failed to resolve validator address (got: '${ADDR}')" >&2
  exit 1
fi

# Fund validator with lots of uvault for lab use.
"${BINARY}" genesis add-genesis-account "${ADDR}" "100000000000000uvault" --home "${HOME_DIR}"

# Optional second account for users/providers.
"${BINARY}" keys add alice --home "${HOME_DIR}" >/dev/null
ALICE="$("${BINARY}" keys show alice --home "${HOME_DIR}" | tr -d '\r' | tail -n1)"
"${BINARY}" genesis add-genesis-account "${ALICE}" "50000000000000uvault" --home "${HOME_DIR}"

cat <<EOF

Vaultchain local genesis ready.

  Home:     ${HOME_DIR}
  Chain ID: ${CHAIN_ID}
  Validator:${ADDR}
  Alice:    ${ALICE}

Ports:
  RPC  http://127.0.0.1:26657
  REST http://127.0.0.1:1317

Start:
  ${BINARY} start --home ${HOME_DIR}

EOF
