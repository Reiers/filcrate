# Protocol notes

These are working notes from reading lotus, boost, and curio in May 2026. They are intended to be useful to anyone else trying to build a Filecoin storage client today, not just to filcrate maintainers. Cross-references to upstream files use the canonical paths in `github.com/filecoin-project/<repo>/<path>`.

## Three eras of the storage market protocol

| Era | Protocol | Transport | Where it lives |
|---|---|---|---|
| Lotus client (legacy v1.1) | mk1.1 / `/fil/storage/mk/1.1.0` | libp2p | Effectively dead in mainline lotus. CLI commands (`lotus client deal`) removed. |
| Boost (current legacy) | mk1.2 / `/fil/storage/mk/1.2.0`, `/fil/storage/mk/1.2.1` | libp2p | `boost/cmd/boost/deal_cmd.go`. Still in production at many SPs. |
| Curio Market 2.0 (modern) | mk20 | plain HTTPS | `curio/market/mk20/`. Native to Curio, not Boost. |

filcrate targets mk20 first. mk12 fallback support is on the roadmap but not a V1 requirement.

## Mk20 endpoint surface

All under `<sp-base-url>/market/mk20/`. Auth-gated by a signed `Authorization` header (see below). Source: `curio/market/mk20/http/http.go`.

| Method | Path | Purpose |
|---|---|---|
| `GET`  | `/products` | Capability probe — returns `["ddo_v1", "pdp_v1", "retrieval_v1", ...]` |
| `GET`  | `/sources` | Returns supported data-source labels — `http`, `aggregate`, `offline`, `http_put` |
| `GET`  | `/contracts` | Returns EVM addresses of allowlisted DDO market contracts |
| `POST` | `/deal` | Submit a Deal envelope |
| `GET`  | `/status/{id}` | Look up deal status by ULID |
| `POST` | `/uploads/{id}` | Start a chunked upload |
| `PUT`  | `/uploads/{id}/{chunkNum}` | Upload a single chunk |
| `POST` | `/uploads/finalize/{id}` | Finalize a chunked upload |
| `PUT`  | `/upload/{id}` | Serial upload (single PUT) |
| `POST` | `/upload/{id}` | Finalize a serial upload |
| `POST` | `/update/{id}` | Update an accepted deal's data source |

## Mk20 deal envelope shape

```jsonc
{
  "identifier": "<ULID>",
  "client":     "<f1/f4 address string>",
  "data": {
    "piece_cid": "<PieceCID v2 / FRC-0069>",
    "format":    { "car": {} },
    "source_http": {
      "urls": [
        { "url": "https://example.com/piece.car", "priority": 0, "fallback": true }
      ]
    }
  },
  "products": {
    "ddo_v1": {
      "provider":      "<f0xxx miner address>",
      "duration":      518400,
      "allocation_id": 12345,
      "...":           "..."
    },
    "retrieval_v1": { "indexing": true, "index_announce": true }
  }
}
```

`Products` is a union: a Deal carries `DDOV1` (sealed storage) **or** `PDPV1` (warm storage) — usually with `RetrievalV1` attached. PDP needs an existing data set + record-keeper. DDO needs either an `allocation_id` (FIL+) or a `market_address` (paid deal via custom contract).

## CurioAuth header construction

The header is rebuilt for **every** request because the timestamp is truncated to the minute (≈60s validity).

```
preimage = addr.Bytes() || UPPER(method) || request_path || RFC3339(now truncated to minute)
digest   = sha256(preimage)
sig      = wallet.Sign(digest)            // wallet does its own internal blake2b
header   = "CurioAuth " + keyType + ":" + base64(addr.Bytes()) + ":" + base64(sig)
```

Where:
- `addr.Bytes()` is the **Filecoin-encoded address bytes** (1-byte protocol prefix + payload), not a raw secp256k1 public key. The SP reconstructs the address with `address.NewFromBytes(...)` and dispatches signature verification by protocol byte.
- `keyType` is `secp256k1`, `bls`, or `delegated`.
- `request_path` should be the URL-escaped path (chi's `r.URL.EscapedPath()`); important for paths containing reserved characters.
- For secp256k1, the wallet's `Sign(digest)` performs an **inner** blake2b-256 over the digest before producing a 65-byte ECDSA-recoverable signature. The auth flow has TWO hashes: sha256 outside (visible in the preimage) and blake2b inside the signer.
- The `sig` going into the header is the **binary-marshaled `crypto.Signature`** envelope (1-byte type prefix + signature data), not the bare 65 bytes.

References:
- Server side: `curio/market/mk20/auth.go` (`Auth`, `authMessage`, `verifyFilSignature`)
- Client side (Curio's own SDK): `curio/market/mk20/client/auth.go`
- Lotus signature dispatch: `lotus/lib/sigs/secp/init.go`

## Why "paid deals don't work"

Three different things get conflated under that statement:

1. **Paid mk1.2 deals** — still supported by Boost, but economically dead. SPs prefer FIL+ deals because the 10× QAP boost dominates block-reward economics.
2. **Paid mk20 DDO deals** — possible only via a `market_address` contract whitelisted in the SP's `ddo_contracts` table. Curio validates with `CurioDealViewV1.verifyDeal(...)`. There aren't widely-deployed paid market contracts at scale.
3. **Paid mk20 PDP deals** — these *do* work today. The "payment" is a Filecoin Pay rail in USDFC. FWSS (Filecoin Warm Storage Service) is the canonical product contract, $2.50/TiB/mo/copy at time of writing.

So the practical answer for "paid storage I can actually use today" is FWSS-backed PDP. For sealed storage at $0/TiB, the practical answer is FIL+ via a Filplus auto-allocator.

## DataCap path (the one that actually works for sealed storage today)

1. Client wallet has zero DataCap.
2. Auto-allocator (e.g. `allocator.tech` or our own) accepts a small allocation request keyed by the client address.
3. Allocator emits a `Verifreg.Allocation` on chain via the verified-registry actor (FIPs: FIP-0045, FIP-0079).
4. Client constructs a Mk20 DDO deal with `allocation_id` set to the new allocation.
5. SP accepts the deal because the allocation is on chain and provable.

filcrate's `--cold` path will hide all of this behind one waiting indicator.

## Discovery

Mk20 has no advertised price endpoint. Pricing is enforced by the SP's deal validation; the client finds out a price was wrong by getting a `DealCodeRejectedByMarket` (426). For paid deals, the contract at `market_address` carries the pricing logic.

For Mk12, there's a libp2p `/fil/storage/ask/1.1.0` protocol that returns a `StorageAsk` with separate verified and unverified prices. filcrate intentionally does not consume this for V1 — we go HTTP-only.

## Address protocols supported by filcrate

| Protocol | Filecoin term | Filcrate V1? |
|---|---|---|
| `0` | ID address (`f0...`) | Used to identify SPs only; cannot sign. |
| `1` | secp256k1 (`f1...`) | ✅ |
| `2` | BLS (`f3...`) | ❌ deferred (CGo dependency) |
| `3` | Actor (`f2...`) | n/a |
| `4` | Delegated / EVM-shaped (`f4...`) | 🟡 planned for V1 |

## Useful cross-references when extending filcrate

- Mk20 type definitions: `curio/market/mk20/types.go`
- Mk20 reference Go client (Curio's): `curio/market/mk20/client/`
- Mk20 SP intake flow map: `curio/market/mk20/DEVELOPER_FLOW_MAP.md`
- DDO product validation: `curio/market/mk20/ddo_v1.go`
- DDO `CurioDealView` ABI: `curio/market/mk20/contract/CurioDealViewV1.sol`
- PDP product validation: `curio/market/mk20/pdp_v1.go`
- Filplus client allocations: `lotus/cli/filplus.go`
