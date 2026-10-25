#!/bin/sh
# Validator entrypoint (consensus=comet). The network is generated once, offline,
# with `vaultd testnet`; each node receives ITS OWN files through environment
# secrets and this script writes them to the volume. Nothing secret is baked
# into the image or committed to git, and nothing is printed.
#
# Files arrive base64-encoded (so no quoting or newline problems in `fly secrets`):
#   COMET_GENESIS_B64            config/cometgenesis.json (shared, not secret)
#   COMET_PRIV_VALIDATOR_KEY_B64 config/priv_validator_key.json (SECRET: consensus key)
#   COMET_NODE_KEY_B64           config/node_key.json (p2p identity)
#   VAULT_KEYRING_B64            keyring-test/keys.json (SECRET: the validator's fee account)
# Plain value:
#   PERSISTENT_PEERS             id@host:port,... (the other validators)
set -eu

HOME_DIR="${VAULTD_HOME:-/data/vaultd}"
BLOCK_INTERVAL="${BLOCK_INTERVAL:-3s}"
MIN_FEE="${MIN_FEE:-1000uvault}"
TX_RATE_PER_MIN="${TX_RATE_PER_MIN:-60}"
P2P_PORT="${P2P_PORT:-26656}"
MONIKER="${MONIKER:-${FLY_APP_NAME:-vaultd}}"
# Fly 6PN name of this node, advertised to peers (empty outside Fly).
EXTERNAL_HOST="${EXTERNAL_HOST:-}"
if [ -z "$EXTERNAL_HOST" ] && [ -n "${FLY_APP_NAME:-}" ]; then EXTERNAL_HOST="$FLY_APP_NAME.internal"; fi

if [ "$(id -u)" = "0" ]; then
  mkdir -p "$HOME_DIR"
  chown -R vault:vault /data
  exec su-exec vault:vault "$0" "$@"
fi

umask 077
mkdir -p "$HOME_DIR/config" "$HOME_DIR/keyring-test" "$HOME_DIR/cometdata" "$HOME_DIR/data"

# Write key material from secrets. Existing files win only when no secret is set
# (so the same image also runs from a pre-populated volume). priv_validator_state.json
# is never touched: it holds the double-sign protection state.
put() { # put ENV_VAR_NAME destination
  eval "val=\${$1:-}"
  if [ -n "$val" ]; then printf '%s' "$val" | base64 -d >"$2"; fi
}
put COMET_GENESIS_B64 "$HOME_DIR/config/cometgenesis.json"
put COMET_PRIV_VALIDATOR_KEY_B64 "$HOME_DIR/config/priv_validator_key.json"
put COMET_NODE_KEY_B64 "$HOME_DIR/config/node_key.json"
put VAULT_KEYRING_B64 "$HOME_DIR/keyring-test/keys.json"
for f in config/cometgenesis.json config/priv_validator_key.json config/node_key.json keyring-test/keys.json; do
  [ -s "$HOME_DIR/$f" ] || { echo "missing $f: set its secret or pre-populate the volume" >&2; exit 1; }
done

# Peers that are not resolvable yet (their machine has never started) are skipped
# for this boot: they dial us, and persistent peers reconnect on their own side.
PEERS=""
if [ -n "${PERSISTENT_PEERS:-}" ]; then
  OLDIFS="$IFS"; IFS=','
  for p in $PERSISTENT_PEERS; do
    host="${p#*@}"; host="${host%:*}"
    ok=0
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      if getent hosts "$host" >/dev/null 2>&1; then ok=1; break; fi
      sleep 1
    done
    if [ "$ok" = "1" ] || [ -z "${FLY_APP_NAME:-}" ]; then PEERS="${PEERS:+$PEERS,}$p"; else echo "peer host $host not resolvable yet, skipping for this boot"; fi
  done
  IFS="$OLDIFS"
fi

EXT=""
if [ -n "$EXTERNAL_HOST" ]; then EXT="$EXTERNAL_HOST:$P2P_PORT"; fi

# p2p listens on IPv4+IPv6 (Fly 6PN is IPv6); CometBFT RPC stays on loopback.
exec vaultd start --home "$HOME_DIR" --consensus comet \
  --moniker "$MONIKER" --p2p-addr "tcp://[::]:$P2P_PORT" \
  --persistent-peers "$PEERS" --external-addr "$EXT" \
  --api-addr ":1317" --block-interval "$BLOCK_INTERVAL" \
  --min-fee "$MIN_FEE" --tx-rate "$TX_RATE_PER_MIN" --client-ip-header Fly-Client-IP
