package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Reiers/filcrate/pkg/catalog"
)

// NewSPsCommand returns the `filcrate sps ...` command tree.
//
// V0 surface:
//
//	filcrate sps probe <miner-address>          one-off capability probe
//	filcrate sps probe-batch <addr1> <addr2>... concurrent batch probe
//
// Output is human by default, switch to JSON with --json. The point of the
// command is to surface "does this SP speak Mk20, and if so what does it
// support" without a user needing to grep curio source.
func NewSPsCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "sps",
		Short: "Discover and inspect Filecoin storage providers",
	}
	c.AddCommand(newSPsProbeCommand())
	c.AddCommand(newSPsBatchCommand())
	return c
}

func newSPsProbeCommand() *cobra.Command {
	var asJSON bool
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "probe <miner-address>",
		Short: "Probe a single SP for Curio Mk20 capability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(timeoutSec)*time.Second)
			defer cancel()

			cap, err := probeOne(ctx, strings.TrimSpace(args[0]))
			if cap == nil && err != nil {
				return err
			}
			if asJSON {
				return writeJSON(os.Stdout, cap)
			}
			renderCapability(os.Stdout, cap)
			if err != nil {
				// Render but exit with a status so shell pipelines notice.
				return fmt.Errorf("probe finished with errors: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a human-readable summary")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 30, "overall probe timeout in seconds")
	return cmd
}

func newSPsBatchCommand() *cobra.Command {
	var asJSON bool
	var concurrency int
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "probe-batch <miner-address>...",
		Short: "Probe several SPs concurrently",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(timeoutSec)*time.Second)
			defer cancel()

			caps := probeBatch(ctx, args, concurrency)

			if asJSON {
				return writeJSON(os.Stdout, caps)
			}
			renderCapabilityList(os.Stdout, caps)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a human-readable summary")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "number of SPs to probe in parallel")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 90, "overall batch timeout in seconds")
	return cmd
}

func probeOne(ctx context.Context, miner string) (*catalog.Capability, error) {
	rpc, err := chainClient()
	if err != nil {
		return nil, err
	}
	signer, err := loadOrEphemeralSigner(ctx)
	if err != nil {
		return nil, fmt.Errorf("preparing signer: %w", err)
	}
	prober := &catalog.Prober{Chain: rpc, Signer: signer}
	cap, err := prober.Probe(ctx, miner)
	if cap != nil {
		cap.SortErrors()
	}
	return cap, err
}

func probeBatch(ctx context.Context, miners []string, concurrency int) []*catalog.Capability {
	if concurrency <= 0 {
		concurrency = 4
	}
	rpc, err := chainClient()
	if err != nil {
		// Fail fast: every probe needs the chain client.
		out := make([]*catalog.Capability, 0, len(miners))
		for _, m := range miners {
			out = append(out, &catalog.Capability{
				Miner: m,
				Errors: []catalog.ProbeError{
					{Stage: "chain_client", Reason: err.Error()},
				},
			})
		}
		return out
	}
	signer, err := loadOrEphemeralSigner(ctx)
	if err != nil {
		out := make([]*catalog.Capability, 0, len(miners))
		for _, m := range miners {
			out = append(out, &catalog.Capability{
				Miner: m,
				Errors: []catalog.ProbeError{
					{Stage: "signer", Reason: err.Error()},
				},
			})
		}
		return out
	}
	prober := &catalog.Prober{Chain: rpc, Signer: signer}

	results := make([]*catalog.Capability, len(miners))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, m := range miners {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, miner string) {
			defer wg.Done()
			defer func() { <-sem }()
			c, perr := prober.Probe(ctx, miner)
			if c == nil {
				c = &catalog.Capability{Miner: miner}
			}
			if perr != nil && len(c.Errors) == 0 {
				c.Errors = append(c.Errors, catalog.ProbeError{Stage: "probe", Reason: perr.Error()})
			}
			c.SortErrors()
			results[i] = c
		}(i, m)
	}
	wg.Wait()

	// Stable order: caller order is the natural one for batch probes (so a
	// user can read down the list in the same order they typed). No sort.
	return results
}

// renderCapability writes a compact human summary of one probe result.
func renderCapability(w *os.File, cap *catalog.Capability) {
	if cap == nil {
		fmt.Fprintln(w, "(no result)")
		return
	}
	statusGlyph := "✗"
	statusText := "no Mk20"
	if cap.Mk20 {
		statusGlyph = "✓"
		statusText = "Mk20"
	}
	fmt.Fprintf(w, "%s %s  %s\n", statusGlyph, cap.Miner, statusText)
	if cap.BaseURL != "" {
		fmt.Fprintf(w, "  endpoint   %s\n", cap.BaseURL)
	}
	if len(cap.Multiaddrs) > 0 {
		fmt.Fprintf(w, "  multiaddrs %s\n", strings.Join(cap.Multiaddrs, ", "))
	}
	if cap.Mk20 && cap.Latency > 0 {
		fmt.Fprintf(w, "  latency    %s\n", cap.Latency.Round(time.Millisecond))
	}
	if len(cap.Products) > 0 {
		fmt.Fprintf(w, "  products   %s\n", strings.Join(cap.Products, ", "))
	}
	if len(cap.DataSources) > 0 {
		fmt.Fprintf(w, "  sources    %s\n", strings.Join(cap.DataSources, ", "))
	}
	if len(cap.Contracts) > 0 {
		fmt.Fprintf(w, "  contracts  %s\n", strings.Join(cap.Contracts, ", "))
	}
	if len(cap.Errors) > 0 {
		fmt.Fprintln(w, "  errors:")
		for _, e := range cap.Errors {
			line := fmt.Sprintf("    [%s] %s", e.Stage, e.Reason)
			if e.URL != "" {
				line += " (" + e.URL + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
}

func renderCapabilityList(w *os.File, caps []*catalog.Capability) {
	// Group: working Mk20 first, broken after; stable within groups.
	working, broken := []*catalog.Capability{}, []*catalog.Capability{}
	for _, c := range caps {
		if c == nil {
			continue
		}
		if c.Mk20 {
			working = append(working, c)
		} else {
			broken = append(broken, c)
		}
	}
	sort.SliceStable(working, func(i, j int) bool { return working[i].Latency < working[j].Latency })

	for _, c := range working {
		renderCapability(w, c)
		fmt.Fprintln(w)
	}
	for _, c := range broken {
		renderCapability(w, c)
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "summary: %d / %d SPs speak Mk20\n", len(working), len(caps))
}

func writeJSON(w *os.File, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// errors used internally when a batch probe is fully empty.
var errNoMiners = errors.New("no miner addresses supplied")
