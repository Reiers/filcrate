// filcrate is the friendly Filecoin storage client.
//
// V0 covers the read-side of the product: discovering which storage
// providers support Curio Mk20, what products and data sources they accept,
// and which DDO contracts they trust. Deal submission and the WebUI come in
// the following milestones.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Reiers/filcrate/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:           "filcrate",
		Short:         "Friendly Filecoin storage client",
		Long:          longHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&cli.Flags.Network, "network", "calibration",
		"Filecoin network: calibration | mainnet")
	root.PersistentFlags().StringVar(&cli.Flags.RPCEndpoint, "rpc", "",
		"Filecoin RPC endpoint URL (default: public Glif endpoint for the chosen network)")
	root.PersistentFlags().StringVar(&cli.Flags.WalletKeyPath, "wallet", "",
		"path to a hex-encoded 32-byte secp256k1 private key file (creates ephemeral wallet if empty)")

	root.AddCommand(
		cli.NewSPsCommand(),
		cli.NewCommPCommand(),
		cli.NewStoreCommand(),
		cli.NewVersionCommand(),
	)

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "filcrate: %v\n", err)
		os.Exit(1)
	}
}

const longHelp = `filcrate stores files on Filecoin storage providers.

It speaks the modern Curio Mk20 protocol over plain HTTPS and hides the
infrastructure plumbing (PieceCID computation, libp2p multiaddrs, on-chain
deal proposals, FIL+ DataCap negotiation) behind a small set of human-shaped
commands.

This is a pre-alpha. Calibration testnet only by default. See the project
repository at https://github.com/Reiers/filcrate for status.
`
