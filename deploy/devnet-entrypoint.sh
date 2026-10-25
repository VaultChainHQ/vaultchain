#!/bin/sh
# Devnet node entrypoint. Idempotent: genesis and keys are created once, on the
# first boot, and live on the volume mounted at /data. Nothing secret is baked
# into the image or committed to git.
set -eu

HOME_DIR="${VAULTD_HOME:-/data/vaultd}"
CHAIN_ID="${CHAIN_ID:-vault-devnet-1}"
BLOCK_INTERVAL="${BLOCK_INTERVAL:-3s}"
# Genesis balance of the "validator" account, the devnet's only source of funds.
VALIDATOR_FUNDS="${VALIDATOR_FUNDS:-100000000000000uvault}"
# Lab mode (mintable ibc/* vouchers) is a genesis flag, fixed for the life of a
# chain. It is OFF unless LAB_MODE=1 at first boot. Even on a lab-mode chain, the
# public REST refuses lab messages without the VAULT_ADMIN_TOKEN secret.
LAB_MODE="${LAB_MODE:-0}"
# Node-local admission policy for the public REST (not consensus).
MIN_FEE="${MIN_FEE:-1000uvault}"
TX_RATE_PER_MIN="${TX_RATE_PER_MIN:-60}"

# The volume is mounted root-owned: fix that, then continue as the vault user.
if [ "$(id -u)" = "0" ]; then
  mkdir -p "$HOME_DIR"
  chown -R vault:vault /data
  exec su-exec vault:vault "$0" "$@"
fi

umask 077
if [ ! -f "$HOME_DIR/.genesis-done" ]; then
  echo "first boot: creating genesis for chain $CHAIN_ID in $HOME_DIR"
  # No marker means an earlier first boot did not finish: start clean.
  rm -rf "$HOME_DIR/config" "$HOME_DIR/data" "$HOME_DIR/keyring-test"
  LAB_FLAG=""
  if [ "$LAB_MODE" = "1" ]; then LAB_FLAG="--lab-mode"; fi
  # shellcheck disable=SC2086
  vaultd init vault-devnet --home "$HOME_DIR" --chain-id "$CHAIN_ID" $LAB_FLAG >/dev/null
  # The key (and its mnemonic) is written to the volume only; it is never printed.
  vaultd keys add validator --home "$HOME_DIR" >/dev/null
  VAL="$(vaultd keys show validator --home "$HOME_DIR")"
  vaultd genesis add-genesis-account "$VAL" "$VALIDATOR_FUNDS" --home "$HOME_DIR" >/dev/null
  touch "$HOME_DIR/.genesis-done"
  echo "genesis done; validator account: $VAL"
else
  echo "existing chain in $HOME_DIR (validator $(vaultd keys show validator --home "$HOME_DIR"))"
fi

# RPC stays on loopback (use `fly ssh console`); REST listens on all interfaces,
# IPv4 and IPv6, so Fly's proxy and the private network (.internal) can reach it.
exec vaultd start --home "$HOME_DIR" \
  --rpc-addr 127.0.0.1:26657 --api-addr ":1317" \
  --block-interval "$BLOCK_INTERVAL" \
  --min-fee "$MIN_FEE" --tx-rate "$TX_RATE_PER_MIN" --client-ip-header Fly-Client-IP