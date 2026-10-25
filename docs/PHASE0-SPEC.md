# Vaultchain Phase 0 Specification

**Working name:** Vaultchain  
**Bech32 prefix:** `vault`  
**Native denom:** `uvault` (1 vault = 1_000_000 uvault)  
**Chain IDs:** `vault-1` (public), `vault-local-1` (local lab)  
**Stack:** Cosmos SDK–compatible application architecture; local single-validator net for v1 lab  

Inspired by research on Jackal, Filecoin, and Arweave models. This is a clean-room design and codebase — not a Jackal fork.

---

## 1. Product goals

One L1, three storage classes:

| Class | User need | Payment shape | Proof intensity |
|-------|-----------|---------------|-----------------|
| **Personal** | Encrypted personal cloud | Renewable capacity plan | Standard window |
| **Market** | Public object marketplace | Per-deal / metered | Standard window + collateral |
| **Archive** | Long-term / “permanent” | Prepaid N-year (v1); endowment later | Higher replication, longer horizon |

Client-side encryption is mandatory for **Personal**. Market/Archive may store ciphertext or plaintext at the user’s choice; chain never requires plaintext.

---

## 2. Trust boundaries

```
┌─────────────┐     txs / queries      ┌──────────────────┐
│  User app   │ ─────────────────────► │  Vaultchain L1   │
│  (keys)     │                        │  deals, payments │
└──────┬──────┘                        │  proofs, access  │
       │ HTTPS upload/download         └────────┬─────────┘
       ▼                                        │ proof txs
┌─────────────┐                                 │
│  Providers  │ ◄───────────────────────────────┘
│  (bytes)    │
└─────────────┘
```

- **Chain:** canonical deals, balances, provider registry, proof adjudication, access grants.  
- **Providers:** store object bytes; never authoritative for payments.  
- **Users:** hold encryption keys for Personal class.

### Threat model (v1)

| Threat | Mitigation |
|--------|------------|
| Provider deletes data | Missed proofs → slash collateral / fail deal; multi-provider replication for Archive |
| Provider serves wrong bytes | Content-addressed object ID (hash); client verifies on download |
| User loses Personal keys | Out of scope for v1 (document recovery as Phase 2+) |
| Archive insolvency | v1 = prepaid N-year only; true endowment deferred |
| Cross-chain infra takeover | **Forbidden** — IBC is payments-only (see §5) |

---

## 3. On-chain objects

### Provider
- Address, endpoint URL, total/free capacity, collateral escrowed, status (active/shutdown)

### CapacityPlan (Personal)
- Owner, bytes purchased, bytes used, start/end time, payment denom/amount

### Deal (Market / Archive)
- Class, owner, provider(s), object CID (hash), size, replication factor  
- Payment escrow, start height, expire height (or archive_until)  
- Proof interval, missed proof counter  

### Proof
- Deal ID, provider, height challenged, proof blob (v1: stub attestation + merkle root match)

### AccessGrant (Personal)
- Object CID, grantee, capability (read/write), expiry

---

## 4. Proof-of-storage (v1 stub → real)

**v1 stub:** Provider posts `MsgSubmitProof` containing object CID + simple signature over `(cid, deal_id, block_hash)`. Chain checks deal binding and updates last-proven height.

**Next:** challenge windows derived from block hash; Merkle inclusion of random chunk (Jackal/Filecoin-style lesson without copying wire formats).

---

## 5. IBC payments-only policy

**IBC (Inter-Blockchain Communication)** moves messages between Cosmos chains. Vaultchain enables **ICS-20 token transfers** so users can pay with bridged denoms.

### Allowed
- Receive ICS-20 vouchers into `bank`  
- Whitelist denoms that may fund CapacityPlans / Deals  
- Optionally refund/escrow in the same denom

### Forbidden
- Hosting providers on other chains  
- Accepting proofs or deal state from other chains  
- Using IBC to route or store object bytes  
- ICA / cross-chain account control for storage ops  

**Policy string (product):**  
> ICS-20 tokens may fund deals; all storage infrastructure is native to Vaultchain.

Non-Cosmos bridges (e.g. Ethereum USDC) are out of scope for v1; same product rule when added later.

---

## 6. Payment flows

1. **Personal:** `BuyCapacity(bytes, duration, denom)` → escrow to module → mint plan.  
2. **Market:** `OpenDeal(cid, size, provider, duration, denom)` → escrow → provider accepts → upload → proofs.  
3. **Archive:** `OpenArchiveDeal(cid, size, years, denom, replication)` → larger escrow / multi-provider assignment.

Native gas: `uvault`. Deal payments: `uvault` and any denom in `Params.PaymentDenomWhitelist` (includes IBC vouchers once relayed).

---

## 7. Archive economics (v1 vs later)

- **v1:** Prepaid archive until height/time `T` (e.g. 5–10 years). Explicit end; renewal tx.  
- **Later:** Endowment model (principal earns yield to pay providers). Requires staking/yield design — not in v1.

---

## 8. Inspiration & non-goals

Inspired by public designs of Jackal (deals/proofs), Filecoin (proving), Arweave (permanence narrative).  
**Non-goals:** Jackal wire compatibility, RNS, Sequoia, copying Jackal source.

---

## 9. Milestone acceptance (this repo)

- [x] `vault-local-1` boots locally  
- [x] Register provider; open Personal plan + Market deal  
- [x] Upload encrypted blob to provider; stub proof accepted  
- [x] IBC payment policy documented and enforced via denom whitelist in storage params

> **Update (M6):** the v1 stub attestation described above was replaced by Merkle chunk challenges with missed-proof tracking, failure/slashing and expiry payout. See README "Proof of storage".

> **Update (M7):** deals are replicated: the chain assigns N providers per deal, they `MsgAcceptDeal`, and proofs, slashing and payment are per replica with repair of failed replicas. See README "Replication".
