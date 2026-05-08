package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Build-time version information. Populated by goreleaser / Makefile via
// `-ldflags "-X 'github.com/Reiers/filcrate/internal/cli.Version=...'"`. When
// missing, we fall back to the Go module build info.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// NewVersionCommand returns the `filcrate version` subcommand.
func NewVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the filcrate version",
		Run: func(cmd *cobra.Command, args []string) {
			v, c, d := Version, Commit, Date
			if v == "dev" {
				if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
					v = info.Main.Version
				}
			}
			line := "filcrate " + v
			if c != "" {
				line += " (" + shortCommit(c) + ")"
			}
			if d != "" {
				line += " " + d
			}
			fmt.Println(line)
		},
	}
}

func shortCommit(c string) string {
	if len(c) <= 8 {
		return c
	}
	return c[:8]
}
