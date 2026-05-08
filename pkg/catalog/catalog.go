// Package catalog turns a Filecoin miner address (e.g. `f01000`) into a
// "do they support Mk20, what products, what data sources, what contracts"
// answer. It is the read-side of filcrate.
//
// Inputs:
//   - a chain.Client (so we can resolve on-chain multiaddrs to HTTP URLs)
//   - an mk20.Signer (because Curio's Mk20 endpoints, including the read-only
//     capability ones, gate behind a signed Authorization header)
//
// Output: a Capability snapshot per miner.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/ipni/go-libipni/maurl"
	"github.com/multiformats/go-multiaddr"

	"github.com/Reiers/filcrate/pkg/chain"
	"github.com/Reiers/filcrate/pkg/mk20"
)

// Capability is the result of probing a single miner.
type Capability struct {
	Miner       string         `json:"miner"`
	BaseURL     string         `json:"base_url"`
	CheckedAt   time.Time      `json:"checked_at"`
	Mk20        bool           `json:"mk20"`
	Products    []string       `json:"products,omitempty"`
	DataSources []string       `json:"data_sources,omitempty"`
	Contracts   []string       `json:"contracts,omitempty"`
	Latency     time.Duration  `json:"latency"`
	Multiaddrs  []string       `json:"multiaddrs,omitempty"`
	Errors      []ProbeError   `json:"errors,omitempty"`
}

// ProbeError captures a single failed probe attempt (e.g. one HTTP URL of
// many that didn't respond) so callers can render diagnostics.
type ProbeError struct {
	Stage  string `json:"stage"`
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason"`
}

// Prober probes a miner's Mk20 surface.
type Prober struct {
	Chain  *chain.Client
	Signer mk20.Signer

	// HTTPTimeout bounds each individual capability call. Default 10s.
	HTTPTimeout time.Duration
}

// Probe resolves multiaddrs from chain, picks the best HTTP base URL, and
// queries the three capability endpoints. It returns a partial Capability
// even on failure: callers can render whatever was learned.
func (p *Prober) Probe(ctx context.Context, miner string) (*Capability, error) {
	if p.Chain == nil {
		return nil, errors.New("Prober.Chain is required")
	}
	if p.Signer == nil {
		return nil, errors.New("Prober.Signer is required")
	}
	timeout := p.HTTPTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	cap := &Capability{Miner: miner, CheckedAt: time.Now().UTC()}

	info, err := p.Chain.StateMinerInfo(ctx, miner)
	if err != nil {
		cap.Errors = append(cap.Errors, ProbeError{Stage: "state_miner_info", Reason: err.Error()})
		return cap, fmt.Errorf("state miner info: %w", err)
	}
	maddrs, err := info.ParsedMultiaddrs()
	if err != nil {
		cap.Errors = append(cap.Errors, ProbeError{Stage: "parse_multiaddrs", Reason: err.Error()})
		return cap, err
	}
	for _, ma := range maddrs {
		cap.Multiaddrs = append(cap.Multiaddrs, ma.String())
	}

	candidates := httpCandidates(maddrs)
	if len(candidates) == 0 {
		cap.Errors = append(cap.Errors, ProbeError{Stage: "no_http_multiaddr", Reason: "miner has no HTTP-shaped multiaddr"})
		return cap, errors.New("no HTTP multiaddr advertised on chain")
	}

	for _, base := range candidates {
		started := time.Now()

		client, err := mk20.NewClient(base, p.Signer)
		if err != nil {
			cap.Errors = append(cap.Errors, ProbeError{Stage: "new_client", URL: base, Reason: err.Error()})
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		products, perr := client.Products(probeCtx)
		cancel()
		if perr != nil {
			cap.Errors = append(cap.Errors, ProbeError{Stage: "products", URL: base, Reason: perr.Error()})
			continue
		}

		// We hit a working Mk20 endpoint. Record it and finish enumerating.
		cap.Mk20 = true
		cap.BaseURL = base
		cap.Products = products
		cap.Latency = time.Since(started)

		// Best-effort sources + contracts.
		probeCtx, cancel = context.WithTimeout(ctx, timeout)
		if sources, err := client.DataSources(probeCtx); err == nil {
			cap.DataSources = sources
		} else {
			cap.Errors = append(cap.Errors, ProbeError{Stage: "sources", URL: base, Reason: err.Error()})
		}
		cancel()

		probeCtx, cancel = context.WithTimeout(ctx, timeout)
		if contracts, err := client.Contracts(probeCtx); err == nil {
			cap.Contracts = contracts
		} else if !mk20.IsNotFound(err) {
			// 404 here means "no contracts allowlisted" which is normal;
			// only surface non-404 errors.
			cap.Errors = append(cap.Errors, ProbeError{Stage: "contracts", URL: base, Reason: err.Error()})
		}
		cancel()
		break
	}

	return cap, nil
}

// httpCandidates converts on-chain multiaddrs to candidate HTTP base URLs,
// preferring https and stripping any path segments.
func httpCandidates(maddrs []multiaddr.Multiaddr) []string {
	type ranked struct {
		url   string
		score int
	}
	ranked0 := make([]ranked, 0, len(maddrs))

	for _, ma := range maddrs {
		u, err := maurl.ToURL(ma)
		if err != nil {
			continue
		}
		switch u.Scheme {
		case "wss":
			u.Scheme = "https"
		case "ws":
			u.Scheme = "http"
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		// Normalize: drop path/query, keep scheme+host(+port).
		base := &url.URL{Scheme: u.Scheme, Host: u.Host}
		score := 0
		if u.Scheme == "https" {
			score += 10
		}
		ranked0 = append(ranked0, ranked{url: base.String(), score: score})
	}

	sort.SliceStable(ranked0, func(i, j int) bool { return ranked0[i].score > ranked0[j].score })

	seen := map[string]struct{}{}
	out := make([]string, 0, len(ranked0))
	for _, r := range ranked0 {
		if _, ok := seen[r.url]; ok {
			continue
		}
		seen[r.url] = struct{}{}
		out = append(out, r.url)
	}
	return out
}

// errorList implements sort.Interface for stable rendering in CLIs.
type errorList []ProbeError

func (e errorList) Len() int           { return len(e) }
func (e errorList) Less(i, j int) bool { return e[i].Stage < e[j].Stage }
func (e errorList) Swap(i, j int)      { e[i], e[j] = e[j], e[i] }

// SortErrors sorts c.Errors in-place by stage.
func (c *Capability) SortErrors() { sort.Sort(errorList(c.Errors)) }

// poolGuard is a placeholder hook for a future concurrent batch prober.
// Kept as a package-level type so callers don't import sync just to satisfy
// linters when they wire one in.
type poolGuard struct{ mu sync.Mutex }

var _ = poolGuard{}
