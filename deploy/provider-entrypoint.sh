#!/bin/sh
# Provider entrypoint. The provider key is generated once on the volume (never
# leaves it). A background loop registers the provider on chain as soon as its
# account has been funded; the provider process itself runs in the foreground.
set -eu

HOME_DIR="${VAULTD_HOME:-/data/vaultd}"
OBJECT_DIR="${OBJECT_DIR:-/data/objects}"
REST="${DEVNET_REST:-http://vaultl1-devnet.internal:1317}"
CHAIN_ID="${CHAIN_ID:-vault-devnet-1}"
PUBLIC_URL="${PUBLIC_URL:-https://${FLY_APP_NAME:-vaultl1-provider}.fly.dev}"
CAPACITY_BYTES="${CAPACITY_BYTES:-700000000}"
COLLATERAL="${COLLATERAL:-1000000000uvault}"
FEE="${FEE:-1000uvault}"
ALLOW_ORIGIN="${ALLOW_ORIGIN:-}"

if [ "$(id -u)" = "0" ]; then
  mkdir -p "$HOME_DIR" "$OBJECT_DIR"
  chown -R vault:vault /data
  exec su-exec vault:vault "$0" "$@"
fi

umask 077
if ! vaultd keys show provider --home "$HOME_DIR" >/dev/null 2>&1; then
  echo "first boot: creating provider key on the volume"
  vaultd keys add provider --home "$HOME_DIR" >/dev/null
fi
ADDR="$(vaultd keys show provider --home "$HOME_DIR")"
echo "provider address: $ADDR"

register() {
  need=$(( ${COLLATERAL%%[!0-9]*} + 100000 ))
  warned=0
  while :; do
    if curl -fsS "$REST/vaultchain/storage/providers/$ADDR" >/dev/null 2>&1; then
      echo "register: provider $ADDR is registered on chain"
      return 0
    fi
    bal="$(curl -fsS "$REST/cosmos/bank/v1beta1/balances/$ADDR" 2>/dev/null \
      | tr -d ' \n' | sed -n 's/.*"denom":"uvault","amount":"\([0-9]*\)".*/\1/p;s/.*"amount":"\([0-9]*\)","denom":"uvault".*/\1/p' | head -n1)"
    bal="${bal:-0}"
    if [ "$bal" -ge "$need" ]; then
      echo "register: balance $bal uvault, registering $PUBLIC_URL"
      vaultd tx storage register-provider "$PUBLIC_URL" "$CAPACITY_BYTES" "$COLLATERAL" \
        --from provider --home "$HOME_DIR" --node "$REST" --fee "$FEE" || true
      sleep 10
    else
      if [ "$warned" = 0 ]; then
        echo "register: waiting for funds: send at least $need uvault to $ADDR (see docs/FLY-DEVNET.md)"
        warned=1
      fi
      sleep 10
    fi
  done
}
register &

exec vault-provider -address "$ADDR" -listen ":3333" -data "$OBJECT_DIR" \
  -rest "$REST" -chain-id "$CHAIN_ID" -home "$HOME_DIR" -key provider \
  -fee "$FEE" -allow-origin "$ALLOW_ORIGIN"