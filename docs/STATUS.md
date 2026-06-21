# filcrate — build status

Last updated: 2026-05-08 (V0.3)

## What works today

### CLI surface

```
filcrate sps probe <miner-address>
filcrate sps probe-batch <addr>... [--concurrency N] [--timeout S]
filcrate sps catalog [--top N] [--out file.json] [--json]
filcrate commp <file> [--json]
filcrate store <file> --provider <f0...> --tier=<cold|hot> [...]
filcrate version
```

`store` auto-selects serial (one PUT) vs chunked upload based on file size —
files ≤ 64 MiB use serial, larger use chunked at 16 MiB per chunk with
concurrency 4. Both paths are integration-tested against an in-process
Mk20 mock SP.

Global flags: `--network=calibration|mainnet`, `--rpc <url>`, `--wallet <key-file>`.

### Verified end-to-end (mock SP)

`pkg/mk20/mockserver/` is a protocol-compatible Mk20 mock that implements
the same auth verification as the real Curio handler — preimage construction,
secp256k1 signature unpacking, EcRecover-based address derivation. Any
signing bug in the client surfaces as a 401 here.

Integration tests against the mock cover the full client surface:

| Test | What it proves |
|---|---|
| `TestIntegration_CapabilityProbe` | `/products`, `/sources`, `/contracts` (incl. 404 on no contracts) all parse correctly |
| `TestIntegration_AuthRejectsBadSigner` | Disallowed-client wallet correctly produces 401 with `IsAuthError(err)` |
| `TestIntegration_FullDDOFlow_HTTPSource` | Submit DDO deal with HTTP source, look up status |
| `TestIntegration_PDPSerialUpload` | Submit PDP deal, PUT bytes, finalize, poll until "active". Bytes roundtrip exactly. |
| `TestIntegration_DealRejection` | SP returns 4xx; client surfaces error; second deal succeeds (no client-side state corruption) |
| `TestIntegration_ChunkedUpload_Roundtrip` | 1.5 MiB payload chunked at 256 KiB into 6 PUTs with concurrency 3, reassembled byte-perfect at SP |

### Verified end-to-end (real network)

- **Chain RPC**: `Filecoin.StateMinerInfo` against public Glif endpoints.
- **Capability prober**: real-network smoke tests against calibration miners; surfaces every failure mode (`no_http_multiaddr`, `products` timeout, HTTP 500 on older Curio, etc).
- **Catalog crawler**: top-N from Filfox, batch probe, persist JSON.

### Calibration network state (2026-05-08)

Of the 7 active calibration miners with raw byte power:
- 2 advertise a public DNS name but the backing Curio binary is on an old pre-Mk20 build (v1.27.3-rc1, Feb 2026) — `/market/mk20/products` returns 404.
- 2 advertise private-IP-only multiaddrs (not publicly reachable).
- 1 advertises `127.0.0.1`.
- 1 returns connection-refused on the public DNS name.
- 1 (`temp-calib.devtty.eu`) reaches the Mk20 endpoint but returns HTTP 500 (older Curio that emits 500 instead of 401 on auth-fail).

**Net: zero working Mk20 SPs on calibration today.** This is documented for posterity; it's not a bug in filcrate.

## What's not done yet

**Highest priority next:**
- **Dedicated calibration Curio on Hetzner** (or equivalent isolated infra) as the live Mk20 test target. Why not the existing Doctor calibration cluster: tried that on 2026-05-08 and learned that `EnableDealMarket=true` pulls in libp2p multiaddr publishing as a side-effect, which sent two `ChangeMultiaddrs` chain messages against the production miner IDs. Repaired the same session. Hard rules now in `AGENTS.md` to prevent recurrence; the cleanest path forward is a fresh Curio on infra we control with a wallet filcrate generates itself, not a shared keystore.
- **Filplus auto-allocator integration.** `--auto-allocate` for cold deals so the user doesn't need to pre-arrange DataCap.

**Roadmap:**
- WebUI scaffold (Next.js, talks to a `filcrated` daemon over HTTP)
- Hot-path integration via Synapse SDK or Go reimpl (FoC PDP)
- f4 / delegated wallet support (EVM-shaped signing)
- One-line installer modeled on filbucket's `install.sh`
- Native chain walker (drop the Filfox dependency)

## Known limitations

- BLS (`f3`) wallets deferred — would require CGo `supranational/blst`, breaks the single-binary install story.
- Mock SP currently only verifies secp256k1 signatures. BLS / delegated paths are stubbed for future expansion.
- Mainline Curio returns 401 on auth-fail; some older deployed builds return 500. The prober surfaces both clearly but treats them equivalently.
- Dry-run skips the SP probe entirely so envelope inspection works without network.

## How to verify locally

```bash
go build ./cmd/filcrate
go test ./...

# Probe live SPs
./filcrate sps probe t0181521

# Build a deal envelope without submitting
./filcrate store /path/to/file --provider=f01234 --tier=cold --allocation=42 --dry-run

# Compute PieceCID v2
./filcrate commp /path/to/file --json
```
