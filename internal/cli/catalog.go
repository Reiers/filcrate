package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Reiers/filcrate/pkg/catalog"
)

// addCatalogToSPs attaches the `filcrate sps catalog` subcommand. Lives in
// its own file so the sps tree stays small in sps.go.
func addCatalogToSPs(parent *cobra.Command) {
	parent.AddCommand(newSPsCatalogCommand())
}

func newSPsCatalogCommand() *cobra.Command {
	var (
		topN        int
		concurrency int
		timeoutSec  int
		outPath     string
		asJSON      bool
	)

	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Crawl top SPs and produce a Mk20 capability catalog",
		Long: `Pulls the top --top miners by raw byte power from Filfox, probes each
for Curio Mk20 capability, and produces a catalog snapshot.

Use --out to persist the snapshot as JSON for the WebUI or further
processing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(timeoutSec)*time.Second)
			defer cancel()

			rpc, err := chainClient()
			if err != nil {
				return err
			}
			signer, err := loadOrEphemeralSigner(ctx)
			if err != nil {
				return err
			}
			net, err := resolveNetwork()
			if err != nil {
				return err
			}

			crawler := &catalog.Crawler{
				Chain:       rpc,
				Prober:      &catalog.Prober{Chain: rpc, Signer: signer},
				Concurrency: concurrency,
			}

			fmt.Fprintf(os.Stderr, "▸ crawling top %d %s SPs (concurrency=%d)...\n", topN, net, concurrency)
			snap, err := crawler.Crawl(ctx, net, topN)
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "  %d SPs probed, %d speak Mk20\n", snap.Total, snap.Mk20Count)

			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("creating %s: %w", outPath, err)
				}
				defer f.Close()
				if err := catalog.WriteSnapshot(f, snap); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "  wrote %s\n", outPath)
			}

			if asJSON {
				return catalog.WriteSnapshot(os.Stdout, snap)
			}

			renderCatalogSummary(snap)
			return nil
		},
	}
	cmd.Flags().IntVar(&topN, "top", 50, "number of top SPs (by raw byte power) to probe")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "concurrent probes")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 600, "overall timeout in seconds")
	cmd.Flags().StringVar(&outPath, "out", "", "write the snapshot to this path as JSON")
	cmd.Flags().BoolVar(&asJSON, "json", false, "also emit the snapshot to stdout as JSON")
	return cmd
}

func renderCatalogSummary(snap *catalog.Snapshot) {
	working := []*catalog.Capability{}
	broken := []*catalog.Capability{}
	for _, c := range snap.Items {
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
	sort.SliceStable(broken, func(i, j int) bool { return broken[i].Miner < broken[j].Miner })

	if len(working) == 0 {
		fmt.Println("no SPs in this snapshot speak Mk20")
		return
	}

	fmt.Printf("Mk20 SPs (%d):\n", len(working))
	for _, c := range working {
		products := strings.Join(c.Products, ",")
		fmt.Printf("  %-12s %-50s  [%s] %s\n", c.Miner, c.BaseURL, products, c.Latency.Round(time.Millisecond))
	}
	fmt.Println()
	fmt.Printf("non-Mk20 (%d):\n", len(broken))
	for _, c := range broken {
		var why string
		if len(c.Errors) > 0 {
			why = c.Errors[0].Stage
		}
		fmt.Printf("  %-12s %s\n", c.Miner, why)
	}
}
