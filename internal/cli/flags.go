// Package cli holds the cobra command tree and shared flag bag for the
// filcrate binary. Internal-only.
package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Reiers/filcrate/pkg/chain"
	"github.com/Reiers/filcrate/pkg/mk20"
	"github.com/Reiers/filcrate/pkg/wallet"
)

// Flags is the package-level flag bag wired up in main(). Keeping it here
// rather than threading through every NewXCommand keeps each command's
// signature small.
var Flags struct {
	Network       string
	RPCEndpoint   string
	WalletKeyPath string
}

// resolveNetwork validates --network and returns the chain.Network value.
func resolveNetwork() (chain.Network, error) {
	switch strings.ToLower(strings.TrimSpace(Flags.Network)) {
	case "calibration", "calib", "calibnet":
		return chain.NetworkCalibration, nil
	case "mainnet", "main":
		return chain.NetworkMainnet, nil
	case "":
		return chain.NetworkCalibration, nil
	default:
		return "", fmt.Errorf("unknown --network %q (valid: calibration, mainnet)", Flags.Network)
	}
}

// chainClient builds a chain.Client honoring --network and --rpc.
func chainClient() (*chain.Client, error) {
	net, err := resolveNetwork()
	if err != nil {
		return nil, err
	}
	return chain.New(net, Flags.RPCEndpoint), nil
}

// loadOrEphemeralSigner returns an mk20.Signer.
//
// If --wallet is set, we read 32 bytes of hex-encoded secp256k1 key material
// from the file. Otherwise we mint a fresh key in-memory; this is fine for
// read-only operations (which is what the V0 CLI does) but obviously not
// usable for deals where the SP needs to recognize a stable client address.
func loadOrEphemeralSigner(_ context.Context) (mk20.Signer, error) {
	if Flags.WalletKeyPath == "" {
		return ephemeralSecp()
	}

	path, err := filepath.Abs(Flags.WalletKeyPath)
	if err != nil {
		return nil, fmt.Errorf("resolving --wallet path: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading wallet key file: %w", err)
	}
	priv, err := decodeKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing wallet key from %s: %w", path, err)
	}
	return wallet.NewSecpSigner(priv)
}

// ephemeralSecp produces a fresh secp256k1 signer. Used for read-only probes
// where the SP only needs *some* signed CurioAuth header to satisfy the
// gate.
func ephemeralSecp() (mk20.Signer, error) {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return nil, fmt.Errorf("generating ephemeral secp key: %w", err)
	}
	return wallet.NewSecpSigner(priv)
}

// decodeKey accepts either raw 32 bytes or hex-encoded 32 bytes (with or
// without trailing whitespace and an optional `0x` prefix).
func decodeKey(raw []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	trimmed = strings.TrimPrefix(trimmed, "0x")
	trimmed = strings.TrimPrefix(trimmed, "0X")

	if len(trimmed) == 64 {
		out := make([]byte, 32)
		if _, err := fmt.Sscanf(trimmed, "%x", &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	if len(raw) == 32 {
		out := make([]byte, 32)
		copy(out, raw)
		return out, nil
	}
	return nil, errors.New("expected 32 raw bytes or 64 hex characters of secp256k1 private key")
}
