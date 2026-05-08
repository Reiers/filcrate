# filcrate

> Drop a file in. The crate keeps it on Filecoin.

`filcrate` is a friendly client for storing files on Filecoin storage providers. It speaks the modern protocols (Curio Mk20 over HTTPS, Filecoin Onchain Cloud / PDP for warm storage) and hides the protocol surface that has historically made client-side Filecoin storage painful.

If [filbucket](https://github.com/Reiers/filbucket) is "Dropbox on Filecoin," `filcrate` is "Filecoin storage you can actually use." It exposes wallets, providers, and deal types instead of hiding them — but makes everything that's normally hard (PieceCID computation, multiaddr resolution, libp2p, allocation handshakes, DataCap requests, chunk uploads) automatic.

This is a TSE Reiersen project, opened for the Filecoin community. Not affiliated with FilOzone, Curio Storage, or Protocol Labs.

## Status

🚧 **Pre-alpha.** Calibration testnet only. See `docs/STATUS.md` for the current build state.

## Design goals

- **Mk20 native.** The new Curio Market 2.0 REST protocol is the primary path. Boost / mk1.2 fallback later.
- **Hot + cold.** Hot via Filecoin Onchain Cloud (PDP + Filecoin Pay). Cold via DDO with FIL+ allocations.
- **Supports both wallet families.** `f1` (secp256k1) and `f4` (delegated / EVM via wagmi or external signer). BLS / `f3` deferred.
- **Discovers providers.** Live SP catalog with capability probes — what each SP supports, prices where advertised, recent activity.
- **Auto-DataCap.** When a cold deal needs an allocation, filcrate requests one from a configured Filplus auto-allocator and waits.
- **One-line installer.** `curl -fsSL https://get.filcrate.reiers.io | bash`. Local web UI at `http://localhost:3020`.

## What it isn't

- It is not a storage provider. We don't seal sectors. We submit deals.
- It is not a deal aggregator service. We run on your machine, with your wallet.
- It is not a wallet. We sign transactions and Mk20 auth headers, but key custody is the user's.

## Architecture (V0 sketch)

```
┌──────────────────────────────────────────────────────────────┐
│                      filcrate WebUI                          │
│   Next.js · drag-drop · SP picker · live deal status         │
└──────────────────────────┬──────────────────────────────────┘
                           │ HTTP/JSON
                           ▼
┌──────────────────────────────────────────────────────────────┐
│                   filcrated (local daemon)                   │
│   Go binary. Single static binary, no SP-side dependencies.  │
│                                                              │
│   ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
│   │ catalog  │  │  mk20    │  │  hotpath │  │  wallet  │    │
│   │ crawler  │  │  client  │  │  (FoC)   │  │  bridge  │    │
│   └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘    │
└────────┼─────────────┼─────────────┼─────────────┼──────────┘
         │             │             │             │
         ▼             ▼             ▼             ▼
   filcensus      SP HTTPS      Synapse SDK      f1/f4 keys
   chain crawl    Mk20 REST     (FoC contracts)  (signed)
```

## Repo layout

```
filcrate/
├── cmd/
│   ├── filcrate/        # CLI entrypoint
│   └── filcrated/       # local daemon (HTTP API for the WebUI)
├── pkg/
│   ├── mk20/            # pure-Go Mk20 client (types, HTTP, auth)
│   ├── catalog/         # SP discovery + capability probe
│   ├── chain/           # lightweight chain reader (Filfox + Glif RPC)
│   └── wallet/          # f1 / f4 signer adapters
├── web/                 # Next.js WebUI (added later)
├── docs/                # design docs, protocol notes
└── install.sh           # one-line installer (added later)
```

## License

Dual-licensed: MIT and Apache 2.0. Same Permissive License Stack used elsewhere in the Filecoin ecosystem.
