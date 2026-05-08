package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Reiers/filcrate/pkg/chain"
)

// Crawler walks a list of active Filecoin miners and probes each one's Mk20
// surface, producing a catalog snapshot. It does not enumerate the chain
// directly (that requires a long state walk); instead it pulls top miners
// from a public index API (Filfox by default) and probes them concurrently.
//
// The result is JSON-serializable. Persist with WriteSnapshot, render with
// the catalog command in the CLI, or feed to a future WebUI.
type Crawler struct {
	Chain  *chain.Client
	Prober *Prober

	// IndexEndpoint overrides the default Filfox URL. For calibration we
	// point at the calibration mirror.
	IndexEndpoint string

	// Concurrency caps in-flight probes. Default 8.
	Concurrency int
}

// Snapshot is the persistable shape produced by Crawl.
type Snapshot struct {
	Network    chain.Network `json:"network"`
	Source     string        `json:"source"`
	GeneratedAt time.Time    `json:"generated_at"`
	Total      int           `json:"total"`
	Mk20Count  int           `json:"mk20_count"`
	Items      []*Capability `json:"items"`
}

// Crawl pulls the top `top` miners by raw byte power and probes each.
func (c *Crawler) Crawl(ctx context.Context, network chain.Network, top int) (*Snapshot, error) {
	if c.Chain == nil || c.Prober == nil {
		return nil, errors.New("Crawler requires Chain and Prober")
	}
	if top <= 0 {
		top = 50
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}

	endpoint := c.IndexEndpoint
	if endpoint == "" {
		endpoint = defaultIndexEndpoint(network)
	}

	miners, err := fetchTopMiners(ctx, endpoint, top)
	if err != nil {
		return nil, fmt.Errorf("fetching top miners: %w", err)
	}

	caps := c.probeAll(ctx, miners)

	mk20 := 0
	for _, cap := range caps {
		if cap.Mk20 {
			mk20++
		}
	}

	return &Snapshot{
		Network:     network,
		Source:      endpoint,
		GeneratedAt: time.Now().UTC(),
		Total:       len(caps),
		Mk20Count:   mk20,
		Items:       caps,
	}, nil
}

// WriteSnapshot serializes a snapshot as indented JSON.
func WriteSnapshot(w io.Writer, snap *Snapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

// probeAll runs the prober concurrently, respecting Crawler.Concurrency.
func (c *Crawler) probeAll(ctx context.Context, miners []string) []*Capability {
	results := make([]*Capability, len(miners))
	sem := make(chan struct{}, c.Concurrency)
	var wg sync.WaitGroup

	for i, m := range miners {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, miner string) {
			defer wg.Done()
			defer func() { <-sem }()
			cap, perr := c.Prober.Probe(ctx, miner)
			if cap == nil {
				cap = &Capability{Miner: miner}
			}
			if perr != nil && len(cap.Errors) == 0 {
				cap.Errors = append(cap.Errors, ProbeError{Stage: "probe", Reason: perr.Error()})
			}
			cap.SortErrors()
			results[i] = cap
		}(i, m)
	}
	wg.Wait()
	return results
}

// defaultIndexEndpoint returns the Filfox API URL for the given network.
func defaultIndexEndpoint(n chain.Network) string {
	switch n {
	case chain.NetworkCalibration:
		return "https://calibration.filfox.info/api/v1/miner/list/power"
	case chain.NetworkMainnet:
		return "https://filfox.info/api/v1/miner/list/power"
	default:
		return ""
	}
}

// filfoxResponse mirrors the subset of Filfox's response shape we read.
type filfoxResponse struct {
	Miners []struct {
		Address string `json:"address"`
	} `json:"miners"`
}

// fetchTopMiners hits Filfox-shaped APIs and returns the top `top` miner
// addresses by raw byte power. Filfox caps page size at 100 entries; we
// loop if more is requested.
func fetchTopMiners(ctx context.Context, endpoint string, top int) ([]string, error) {
	pageSize := 100
	if top < pageSize {
		pageSize = top
	}

	out := make([]string, 0, top)
	httpc := &http.Client{Timeout: 20 * time.Second}

	for page := 0; len(out) < top; page++ {
		url := fmt.Sprintf("%s?pageSize=%d&page=%d", endpoint, pageSize, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "filcrate/0.1 (catalog crawler)")

		resp, err := httpc.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("filfox %s -> HTTP %d: %s", url, resp.StatusCode, string(body))
		}

		var parsed filfoxResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decoding filfox response: %w", err)
		}
		if len(parsed.Miners) == 0 {
			break
		}
		for _, m := range parsed.Miners {
			out = append(out, m.Address)
			if len(out) >= top {
				break
			}
		}
		// Short page → we've reached the end. The calibration Filfox
		// errors with HTTP 500 on out-of-range pages rather than returning
		// an empty list, so detect via short-page rather than empty-page.
		if len(parsed.Miners) < pageSize {
			break
		}
	}

	return out, nil
}
