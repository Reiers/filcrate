package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Reiers/filcrate/pkg/piece"
)

// NewCommPCommand returns `filcrate commp <file>`. It computes the
// PieceCID v2 (FRC-0069) for the given file and prints either a one-line
// summary or a JSON object suitable for piping into the deal-builder.
func NewCommPCommand() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "commp <file>",
		Short: "Compute the PieceCID v2 for a local file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := piece.File(args[0])
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Path            string `json:"path"`
					PieceCID        string `json:"piece_cid"`
					PayloadSize     uint64 `json:"payload_size"`
					PaddedPieceSize uint64 `json:"padded_piece_size"`
				}{
					Path:            args[0],
					PieceCID:        info.PieceCID.String(),
					PayloadSize:     info.PayloadSize,
					PaddedPieceSize: info.PaddedPieceSize,
				})
			}

			fmt.Printf("piece cid v2: %s\n", info.PieceCID.String())
			fmt.Printf("payload size: %s (%d bytes)\n", humanSize(info.PayloadSize), info.PayloadSize)
			fmt.Printf("padded size:  %s (%d bytes)\n", humanSize(info.PaddedPieceSize), info.PaddedPieceSize)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of human output")
	return cmd
}

// humanSize is a tiny helper avoiding a humanize dep for one call site.
func humanSize(n uint64) string {
	const (
		_ = 1 << (10 * iota)
		ki
		mi
		gi
		ti
	)
	switch {
	case n >= ti:
		return fmt.Sprintf("%.2f TiB", float64(n)/ti)
	case n >= gi:
		return fmt.Sprintf("%.2f GiB", float64(n)/gi)
	case n >= mi:
		return fmt.Sprintf("%.2f MiB", float64(n)/mi)
	case n >= ki:
		return fmt.Sprintf("%.2f KiB", float64(n)/ki)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
