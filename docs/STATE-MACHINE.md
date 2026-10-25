# State machine and storage layout

Lab-grade design notes for the local single-validator engine. Not production consensus.

## Block execution (`node.App.ExecuteBlock`)

Input: `BlockInput{Height, Time, Proposer, Txs}`. Nothing else is read (no wall clock, no randomness), so replaying
the same inputs on the same genesis gives the same state and the same app hash (tested on MemDB and bbolt).

- `Height` must be last+1; `Time` is normalised to UTC milliseconds and must be strictly after the previous block.
- Per tx:
  1. **Ante** (stateless + state checks): decode/verify signature, chain id, nonce == account nonce, fee payable.
     Any failure => the tx is *rejected* and changes nothing.
  2. Fee is moved from signer to the proposer (if it is a valid address) or the `vaultchain-fee-collector` account;
     nonce is incremented. These persist even if the messages fail (so failed txs cannot be replayed for free).
  3. Messages run on a cache branch (`kv.Cache`). If any message errors or panics, the branch is dropped, so there
     are no partial effects. Otherwise it is written into the block cache.
- The block cache is committed in one bbolt `Update` transaction containing only changed keys.
- Tx results are recorded (`ok` + log) so clients can see why a tx failed.

## Keys

`bucket/sub-key`, one bbolt bucket per `bucket`, one bbolt key per sub-key. Values are never empty.

| Key | Value |
|---|---|
| `bank/bal/<addr>` | JSON coins |
| `auth/nonce/<addr>` | u64 |
| `storage/params`, `storage/prov/<addr>`, `storage/manifest/<root>` (chunk manifest, written once by `MsgCommitManifest`), `storage/plan/<id>`, `storage/deal/<id>` (deal plus `replicas[]`, one entry per assigned provider/slot; status `pending`/`open`/`closed`/`failed`/`cancelled`), `storage/proof/<id>` (verified proof: deal, epoch, chunk index), `storage/epoch` (current challenge epoch `{height, seed}`, rewritten at each `proof_interval_blocks` boundary by the storage end-block hook, which runs before the block header/app hash is computed), `storage/seq/*` | JSON |
| `access/grant/<id>`, `access/seq` | JSON |
| `ibc/lab_mode` | `1`/`0`, set at genesis |
| `_meta/*`, `_blocks/<h>`, `_txs/<hash>`, `_txidx/<h><i>` | bookkeeping (chain id, height, time, headers, tx records) — **not** in the app hash |

## App hash

`AdHash` over all non-bookkeeping entries: `acc = sum(sha256(domain || len(k) || k || v)) mod 2^256`, published as
`sha256(domain || acc)`. It is order-independent and incremental (a block updates it from its write set only).
Trade-offs: no Merkle inclusion proofs for state, and weaker collision resistance than a Merkle tree. Fine for a lab
chain; replace with an IAVL/SMT-style commitment before any public network.

## Tx record retention

Raw tx + result are kept for the last `TxRetention` blocks (default 1000). Older records are deleted along with their
index entries; block headers (which carry `txs_hash`, `ok_count`, `app_hash`) are kept.

## Migration

There is none: earlier builds stored the whole chain as one JSON blob and used unsigned txs. Start a fresh devnet
(`rm -rf <home>/data` or re-run `scripts/init-vault-local.sh`). The engine refuses to start on the legacy file.