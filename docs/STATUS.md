# filcrate — build status

Last updated: 2026-05-08 (V0.2)

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

Global flags: `--network=calibration|mainnet`, `--rpc <url>`, `--wallet <key-file>`.

### Verified end-to-end

- **Chain RPC** — `Filecoin.StateMinerInfo` against public Glif endpoints. Multiaddrs parsed, base64-decoded, and converted to HTTP base URLs via `maurl.ToURL`.
- **CurioAuth header** — preimage matches the exact construction in `curio/market/mk20/auth.go`. Verified via unit test (EcRecover round-trip on the secp256k1 signature recovers our wallet address).
- **PieceCID v2 (FRC-0069)** — file → `bafkz...` PieceCID via `go-fil-commp-hashhash` + `go-fil-commcid`. Deterministic. Tested with 65-byte minimum.
- **Deal envelopes** — typed builders for DDO (cold) and PDP (hot) with default retrieval product, ULID identifiers, and validation. Mutual-exclusion checks (HTTP source vs HTTP-PUT source) enforced.
- **Capability prober** — single-SP and batch-concurrent. Real network responses surfaced: `t0143103` (`REDACTED-SP`), `t0181521` (`temp-calib.devtty.eu`), and others. Errors are `Stage`-tagged (`state_miner_info`, `parse_multiaddrs`, `no_http_multiaddr`, `products`, `sources`, `contracts`).
- **Catalog crawler** — pulls top-N miners from Filfox (calibration: 7 active SPs returned), batch-probes, persists JSON snapshots. Handles short-page detection (Filfox returns HTTP 500 on out-of-range pagination instead of empty pages).

### Smoke-test results (calibration, 2026-05-08)

```
$ filcrate sps catalog --top=20
  7 SPs probed, 0 speak Mk20

  t0143103 → REDACTED-SP    products timeout
  t0180698 → 127.0.0.1             products refused
  t0181521 → temp-calib.devtty.eu  products HTTP 500 (older Curio, returns 500 on auth-fail)
  t04040   → 10.122.69.25          products timeout (private IP)
  t0183240 → 192.168.110.99        products timeout (private IP)
  t0144416 → REDACTED-SP    products timeout
  t01013   → yablufc.ddns.net      products refused
```

The catalog correctly reports zero working Mk20 SPs on calibration; this matches the actual current state of calibration deployments. The infrastructure works; the network is just thin.

## What's not done

**Highest priority for V0.3:**
- **Live Mk20 SP test target** — point at one of Nicklas's calibration nodes (configured to allowlist filcrate's wallet) and exercise the full `store` flow end-to-end.
- **Filplus auto-allocator** — `--tier=cold --auto-allocate` so the user doesn't need an existing FIL+ allocation. Plug into a known auto-allocator's JSON-RPC.
- **Mk20 chunked upload** — for files larger than the SP's serial-PUT limit.

**Roadmap:**
- WebUI (Next.js + local daemon, reuses the filpay theme tokens).
- Hot-path integration via Synapse SDK or Go reimpl (FoC PDP).
- f4 / delegated wallet support (EVM-shaped signing).
- One-line installer (`curl | bash`, modeled on filbucket).
- Native chain walker (drop the Filfox dependency).

## Known limitations

- BLS (`f3`) wallets deferred — would require CGo `supranational/blst`, breaks the single-binary install story.
- The SP's response body is bounded to 8 MiB to prevent OOM-on-misbehaving-SP. If the upstream protocol grows larger response shapes, this needs review.
- Mainline Curio returns 401 on auth-fail; some older deployed builds return 500. We treat both as "unauthenticated" but currently surface the raw status in the prober output for clarity.
- The dry-run path skips the SP probe entirely, so `store --dry-run` works without network access. The non-dry-run path requires a live SP.

## How to verify locally

```bash
go build ./cmd/filcrate

# Compute a piece CID
./filcrate commp ./some-file.bin --json

# Probe a miner
./filcrate sps probe t0181521

# Batch-probe + persist a calibration catalog
./filcrate sps catalog --top=20 --out=calib.json

# Build a cold deal envelope without submitting
./filcrate store ./some-file.bin --provider=f01234 --tier=cold --allocation=42 --dry-run

go test ./...
```
