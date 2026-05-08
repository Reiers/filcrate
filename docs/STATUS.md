# filcrate — build status

Last updated: 2026-05-08

## What works today (V0)

- `filcrate sps probe <miner-address>` — resolves multiaddrs from chain, picks an HTTP base URL, builds a Curio-compatible `CurioAuth` header, queries `/market/mk20/products`, `/sources`, `/contracts`. Pretty-printed or `--json`.
- `filcrate sps probe-batch <addr>...` — concurrent batch probe with `--concurrency` and `--timeout` flags.
- `filcrate version` — version info from build tags or Go module info.
- `--network=calibration` (default) and `--network=mainnet`. Custom RPC via `--rpc <url>`.
- `--wallet <path>` accepts a hex-encoded 32-byte secp256k1 key. Without it, an ephemeral key is generated for read-only probes.

Real-network smoke tests against calibration miners surfaced the expected mix of:
- Working endpoints (e.g. `temp-calib.devtty.eu` returns a structured response, though it's running an older Curio that returns 500 on auth-fail rather than 401).
- SPs that advertise libp2p-only multiaddrs (no Mk20 surface).
- SPs advertising private / `127.0.0.1` multiaddrs (network misconfiguration on their side).
- Connection refused / DNS / timeout cases.

The prober reports each cleanly with a `Stage`-tagged error so downstream tooling can render diagnostics.

## What's verified by tests

- secp256k1 signer derives the correct `f1` Filecoin address from a private key.
- `SignDigest` produces a 65-byte recoverable signature, wrapped in `crypto.Signature{Type: SigTypeSecp256k1}`. EcRecover round-trips back to the original wallet address.
- `AuthHeader` constructs the exact preimage Curio's SP-side `mk20.Auth` recomputes — `addr.Bytes() || UPPER(method) || requestPath || RFC3339(now truncated to minute)`.
- `ApplyAuth` uses `req.URL.EscapedPath()` so URL-encoded path segments (e.g. ULID status lookups) match the SP's chi router behavior.

## What's not done yet

- **Deal submission** — `filcrate store ./file.bin` end-to-end. Needs PieceCID v2 computation, deal envelope construction, and chunked or serial upload handling.
- **DataCap auto-request** — Filplus auto-allocator integration so a "free" cold deal works without the user touching DataCap.
- **WebUI** — Next.js shell talking to a `filcrated` daemon. Not started.
- **Hot path** — FoC PDP / Filecoin Pay rails via Synapse SDK or a Go reimpl. Not started.
- **Wallet flexibility** — only secp256k1 (`f1`) is wired in. Delegated (`f4`) needs an EVM signer adapter.
- **Installer** — `curl | bash` flow modeled on filbucket. Not written.
- **Native batch crawl** — top-N miner enumeration from chain (we currently rely on the user supplying addresses).

## Known limitations

- BLS (`f3`) wallets intentionally deferred — would require a CGo dependency on `supranational/blst` and break the single-binary install story.
- Calibration testnet has many miners that do not run Curio Mk20 yet; the V0 prober surfaces this rather than hiding it.
- The mainline Curio AuthMiddleware returns 401 on auth-fail; some older deployed builds return 500. We treat both as "unauthenticated" but currently surface the raw status to the user for clarity.

## How to verify locally

```bash
go build ./cmd/filcrate
./filcrate sps probe-batch t0143103 t0181521 t04040 --network=calibration
go test ./...
```

Set `--rpc` to a private Glif token URL or your own Lotus to avoid public rate limiting.
