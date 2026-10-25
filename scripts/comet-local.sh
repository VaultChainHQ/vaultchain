#!/usr/bin/env bash
# Run an N-validator CometBFT vaultchain network on this machine (no docker).
#
#   scripts/comet-local.sh start      generate the network (first time) and start all nodes
#   scripts/comet-local.sh stop       stop all nodes (state is kept)
#   scripts/comet-local.sh stop 3     stop node 3 only
#   scripts/comet-local.sh start 3    (re)start node 3 only
#   scripts/comet-local.sh status     height / peers of every node
#   scripts/comet-local.sh clean      stop everything and delete the generated network
#
# Node i serves REST on 1317+(i-1)*100... see PORTS below. Env: N (default 3),
# DIR (default build/comet-local), VAULTD (binary, default build/vaultd),
# CHAIN_ID (default vault-localnet-1), BLOCK_INTERVAL (default 1s).
set -euo pipefail
cd "$(dirname "$0")/.."

N="${N:-3}"
DIR="${DIR:-build/comet-local}"
VAULTD="${VAULTD:-build/vaultd}"
CHAIN_ID="${CHAIN_ID:-vault-localnet-1}"
BLOCK_INTERVAL="${BLOCK_INTERVAL:-1s}"

# PORTS: node i -> p2p 26656+10(i-1), comet rpc 26657+10(i-1), REST 1317+(i-1)
p2p() { echo $((26656 + 10 * ($1 - 1))); }
rpc() { echo $((26657 + 10 * ($1 - 1))); }
rest() { echo $((1317 + $1 - 1)); }

gen() {
  [ -f "$DIR/persistent_peers.txt" ] && return 0
  "$VAULTD" testnet --validators "$N" --chain-id "$CHAIN_ID" --out "$DIR" \
    --hostname-pattern 127.0.0.1 --p2p-port 26656 --p2p-port-step 10 >/dev/null
}

start_node() {
  local i="$1" peers
  peers="$(cat "$DIR/persistent_peers.txt")"
  if [ -f "$DIR/node$i/pid" ] && kill -0 "$(cat "$DIR/node$i/pid")" 2>/dev/null; then
    echo "node$i already running"; return 0
  fi
  nohup "$VAULTD" start --home "$DIR/node$i" --consensus comet --moniker "val-$i" \
    --p2p-addr "tcp://127.0.0.1:$(p2p "$i")" --comet-rpc-addr "tcp://127.0.0.1:$(rpc "$i")" \
    --api-addr "127.0.0.1:$(rest "$i")" --persistent-peers "$peers" \
    --block-interval "$BLOCK_INTERVAL" --min-fee 1000uvault \
    >"$DIR/node$i/node.log" 2>&1 &
  echo $! >"$DIR/node$i/pid"
  echo "node$i started (pid $(cat "$DIR/node$i/pid"), REST :$(rest "$i"))"
}

stop_node() {
  local i="$1"
  if [ -f "$DIR/node$i/pid" ]; then
    kill "$(cat "$DIR/node$i/pid")" 2>/dev/null || true
    for _ in $(seq 1 50); do
      kill -0 "$(cat "$DIR/node$i/pid")" 2>/dev/null || break
      sleep 0.2
    done
    rm -f "$DIR/node$i/pid"
    echo "node$i stopped"
  fi
}

status() {
  for i in $(seq 1 "$N"); do
    printf 'node%s: ' "$i"
    curl -fsS --max-time 3 "http://127.0.0.1:$(rest "$i")/cosmos/base/tendermint/v1beta1/node_info" 2>/dev/null |
      python3 2>/dev/null -c 'import json,sys; d=json.load(sys.stdin); c=d["consensus"]; print("height",d["height"],"peers",c.get("peers"),"validators",c.get("validators"),"catching_up",c.get("catching_up"))' ||
      echo "down"
  done
}

case "${1:-}" in
  start)
    gen
    if [ -n "${2:-}" ]; then start_node "$2"; else for i in $(seq 1 "$N"); do start_node "$i"; done; fi ;;
  stop)
    if [ -n "${2:-}" ]; then stop_node "$2"; else for i in $(seq 1 "$N"); do stop_node "$i"; done; fi ;;
  status) status ;;
  clean)
    for i in $(seq 1 "$N"); do stop_node "$i"; done
    rm -rf "$DIR" ;;
  *) sed -n '2,13p' "$0"; exit 2 ;;
esac
