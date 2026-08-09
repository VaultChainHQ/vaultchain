# IBC Payments-Only Policy

Vaultchain uses IBC **only** to move payment value onto the chain.

## Allowed

- ICS-20 (`transfer` module) inbound/outbound token transfers  
- Paying for capacity plans and deals with whitelisted denoms (native `uvault` and IBC vouchers)

## Forbidden

- Cross-chain provider discovery or registration  
- Cross-chain proof submission or deal lifecycle  
- Storing or routing object bytes over IBC  
- ICS-27 ICA for storage control  

## Enforcement

`x/storage` params field `payment_denom_whitelist` lists denoms accepted for storage payments.  
Gas fees remain native `uvault` unless governance later changes fee market policy.
