package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Reiers/filcrate/pkg/catalog"
	"github.com/Reiers/filcrate/pkg/mk20"
	"github.com/Reiers/filcrate/pkg/piece"
)

// NewStoreCommand returns `filcrate store`.
//
// V0.5 surface (this milestone):
//
//	filcrate store ./file --provider <f0...> --tier=cold --allocation=<id>
//	filcrate store ./file --provider <f0...> --tier=hot  --data-set=<id> --record-keeper=<addr>
//
// The command:
//   1. Computes PieceCID v2 over the file
//   2. Probes the SP for Mk20 capability
//   3. Builds the deal envelope (DDO for cold, PDP for hot)
//   4. Submits via POST /deal
//   5. PUTs the bytes to /upload/{id} (serial mode)
//   6. Polls /status/{id} until terminal state or timeout
//
// Bring-your-own-DataCap for now: --tier=cold requires --allocation. The
// auto-allocator integration arrives in a follow-up milestone.
func NewStoreCommand() *cobra.Command {
	var (
		tier         string
		provider     string
		allocation   uint64
		marketAddr   string
		dataSet      uint64
		recordKeeper string
		duration     int64
		startEpoch   int64
		timeoutSec   int
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "store <file>",
		Short: "Store a file on a Filecoin storage provider",
		Long: `Store a file on a Filecoin storage provider via Curio Mk20.

Tiers:
  --tier=cold   sealed long-term storage (DDO product). Requires either
                a FIL+ allocation (--allocation) or a paid market contract
                (--market-address).
  --tier=hot    warm retrievable storage (PDP product). Requires a
                pre-existing data set on the SP (--data-set) and a
                record-keeper contract address (--record-keeper).

V1 currently only supports the serial upload path (PUT /upload/{id}); files
larger than the SP's PUT limit will be rejected at upload time. Chunked
upload is on the roadmap.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(timeoutSec)*time.Second)
			defer cancel()

			path := args[0]
			tier = strings.ToLower(strings.TrimSpace(tier))
			if tier != "cold" && tier != "hot" {
				return fmt.Errorf("--tier must be cold or hot, got %q", tier)
			}
			if provider == "" {
				return errors.New("--provider <f0...> is required")
			}

			// 1. PieceCID v2 ------------------------------------------------
			fmt.Fprintln(os.Stderr, "▸ computing piece commitment...")
			info, err := piece.File(path)
			if err != nil {
				return fmt.Errorf("commp: %w", err)
			}
			fmt.Fprintf(os.Stderr, "  piece cid: %s\n", info.PieceCID.String())
			fmt.Fprintf(os.Stderr, "  payload:   %s (%d bytes)\n", humanSize(info.PayloadSize), info.PayloadSize)
			fmt.Fprintf(os.Stderr, "  padded:    %s (%d bytes)\n", humanSize(info.PaddedPieceSize), info.PaddedPieceSize)

			// 2. SP probe (skipped on --dry-run) ----------------------------
			signer, err := loadOrEphemeralSigner(ctx)
			if err != nil {
				return err
			}
			var (
				cap    *catalog.Capability
				client *mk20.Client
			)
			if !dryRun {
				fmt.Fprintln(os.Stderr, "▸ probing storage provider...")
				rpc, err := chainClient()
				if err != nil {
					return err
				}
				prober := &catalog.Prober{Chain: rpc, Signer: signer}
				cap, err = prober.Probe(ctx, provider)
				if err != nil {
					return fmt.Errorf("probe %s: %w", provider, err)
				}
				if !cap.Mk20 {
					return fmt.Errorf("provider %s does not advertise Mk20 over HTTP", provider)
				}
				fmt.Fprintf(os.Stderr, "  endpoint:  %s\n", cap.BaseURL)
				fmt.Fprintf(os.Stderr, "  products:  %s\n", strings.Join(cap.Products, ", "))

				required := "ddo_v1"
				if tier == "hot" {
					required = "pdp_v1"
				}
				if !contains(cap.Products, required) {
					return fmt.Errorf("provider %s does not advertise the %q product", provider, required)
				}
			}

			// 3. Build deal envelope ----------------------------------------
			opts := mk20.DealOpts{
				ClientAddress: signer.Address().String(),
				PieceCID:      info.PieceCID,
				PayloadSize:   info.PayloadSize,
				HTTPPutSource: true, // we'll push bytes after acceptance
			}

			var deal *mk20.Deal
			switch tier {
			case "cold":
				if allocation == 0 && marketAddr == "" {
					return errors.New("cold deals require --allocation <id> (FIL+) or --market-address (paid)")
				}
				opts.ColdProvider = provider
				opts.ColdDuration = duration
				if allocation != 0 {
					a := allocation
					opts.ColdAllocationID = &a
				}
				if marketAddr != "" {
					opts.ColdMarketAddress = marketAddr
				}
				if startEpoch != 0 {
					s := startEpoch
					opts.ColdStartEpoch = &s
				}
				deal, err = mk20.NewDDODeal(opts)
			case "hot":
				if dataSet == 0 {
					return errors.New("hot deals require --data-set <id> (create one with `filcrate pdp create-data-set` first)")
				}
				if recordKeeper == "" {
					return errors.New("hot deals require --record-keeper <evm-address>")
				}
				ds := dataSet
				opts.HotDataSetID = &ds
				opts.HotRecordKeeper = recordKeeper
				deal, err = mk20.NewPDPDeal(opts)
			}
			if err != nil {
				return fmt.Errorf("building deal: %w", err)
			}

			fmt.Fprintf(os.Stderr, "  deal id:   %s\n", deal.Identifier.String())

			if dryRun {
				fmt.Fprintln(os.Stderr, "▸ --dry-run set, printing envelope (no probe, no submit)")
				return printDeal(deal)
			}

			// 4. Submit deal ------------------------------------------------
			fmt.Fprintln(os.Stderr, "▸ submitting deal...")
			client, err = mk20.NewClient(cap.BaseURL, signer)
			if err != nil {
				return err
			}
			if _, err := client.SubmitDeal(ctx, deal); err != nil {
				return fmt.Errorf("submit: %w", err)
			}
			fmt.Fprintln(os.Stderr, "  deal accepted")

			// 5. Upload bytes -----------------------------------------------
			fmt.Fprintln(os.Stderr, "▸ uploading payload...")
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("opening payload: %w", err)
			}
			defer f.Close()
			stat, _ := f.Stat()
			if err := client.UploadSerial(ctx, deal.Identifier, f, stat.Size()); err != nil {
				return fmt.Errorf("upload: %w", err)
			}
			fmt.Fprintln(os.Stderr, "  upload finalized")

			// 6. Poll status ------------------------------------------------
			fmt.Fprintln(os.Stderr, "▸ waiting for SP to process...")
			finalState, err := client.PollStatus(ctx, deal.Identifier, time.Duration(timeoutSec)*time.Second, 5*time.Second)
			if finalState != nil {
				fmt.Fprintf(os.Stderr, "  final state: %s\n", finalState.State)
				if finalState.Error != "" {
					fmt.Fprintf(os.Stderr, "  sp error:    %s\n", finalState.Error)
				}
			}
			if err != nil {
				return err
			}

			fmt.Println(deal.Identifier.String())
			return nil
		},
	}

	cmd.Flags().StringVar(&tier, "tier", "cold", "storage tier: cold (DDO/sealed) or hot (PDP/warm)")
	cmd.Flags().StringVar(&provider, "provider", "", "storage provider on-chain address (e.g. f0...) [required]")
	cmd.Flags().Uint64Var(&allocation, "allocation", 0, "FIL+ allocation ID (cold tier; required unless --market-address is set)")
	cmd.Flags().StringVar(&marketAddr, "market-address", "", "EVM address of a whitelisted DDO market contract (cold tier paid deals)")
	cmd.Flags().Uint64Var(&dataSet, "data-set", 0, "PDP data set ID (hot tier; create one first via filcrate pdp create-data-set)")
	cmd.Flags().StringVar(&recordKeeper, "record-keeper", "", "PDP record-keeper EVM address (hot tier; e.g. FWSS)")
	cmd.Flags().Int64Var(&duration, "duration", 518400, "deal duration in epochs (cold tier; default ~180 days)")
	cmd.Flags().Int64Var(&startEpoch, "start-epoch", 0, "absolute start epoch (cold tier; defaults to head + buffer if 0)")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 600, "overall command timeout in seconds")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "build the deal but do not submit; print envelope JSON")
	return cmd
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func printDeal(d *mk20.Deal) error {
	return writeJSON(os.Stdout, d)
}
