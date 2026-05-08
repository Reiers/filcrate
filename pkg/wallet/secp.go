// Package wallet provides Mk20-compatible signers backed by raw key material.
// It supports `f1` (secp256k1) and `f4` (delegated / EVM-shaped) wallets.
// `f3` (BLS) is intentionally deferred because it requires a CGo signer
// (supranational/blst) which would break the single-binary install story.
package wallet

import (
	"context"
	"errors"
	"fmt"

	gocrypto "github.com/filecoin-project/go-crypto"
	"golang.org/x/crypto/blake2b"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/crypto"

	"github.com/Reiers/filcrate/pkg/mk20"
)

// SecpSigner is an mk20.Signer backed by a raw secp256k1 private key. It maps
// to the Filecoin `f1` (BLS-payload-style ID) address family used by the
// canonical Lotus wallet.
type SecpSigner struct {
	priv []byte
	addr address.Address
}

// NewSecpSigner returns a signer for the given raw 32-byte secp256k1 private
// key. The Filecoin address is derived from the public key via the standard
// secp protocol byte (0x01) + 20-byte blake2b-160 of the uncompressed key
// payload.
func NewSecpSigner(priv []byte) (*SecpSigner, error) {
	if len(priv) != 32 {
		return nil, fmt.Errorf("secp256k1 private key must be 32 bytes, got %d", len(priv))
	}

	pub := gocrypto.PublicKey(priv)
	addr, err := address.NewSecp256k1Address(pub)
	if err != nil {
		return nil, fmt.Errorf("deriving f1 address: %w", err)
	}

	out := make([]byte, 32)
	copy(out, priv)
	return &SecpSigner{priv: out, addr: addr}, nil
}

// Address implements mk20.Signer.
func (s *SecpSigner) Address() address.Address { return s.addr }

// KeyType implements mk20.Signer.
func (s *SecpSigner) KeyType() string { return "secp256k1" }

// SignDigest implements mk20.Signer.
//
// The Mk20 auth flow hands us a sha256-digest. The Filecoin secp256k1
// signature scheme then performs a blake2b-256 inside `Sign`, signs that as
// the message digest, and returns 65 bytes (r || s || v recovery byte). We
// wrap the 65-byte result in a `crypto.Signature{Type: secp256k1}` envelope
// and binary-marshal so that `sigs.Verify(...)` on the SP side accepts it.
func (s *SecpSigner) SignDigest(_ context.Context, digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("expected 32-byte digest, got %d", len(digest))
	}

	inner, err := blake2b.New256(nil)
	if err != nil {
		return nil, fmt.Errorf("init blake2b: %w", err)
	}
	if _, err := inner.Write(digest); err != nil {
		return nil, err
	}
	innerHash := inner.Sum(nil)

	sig, err := gocrypto.Sign(s.priv, innerHash)
	if err != nil {
		return nil, fmt.Errorf("secp256k1 sign: %w", err)
	}
	if len(sig) != 65 {
		return nil, fmt.Errorf("expected 65-byte recoverable secp signature, got %d", len(sig))
	}

	wrapped, err := mk20.WrapSignature(crypto.SigTypeSecp256k1, sig)
	if err != nil {
		return nil, err
	}
	return wrapped, nil
}

// Compile-time check.
var _ mk20.Signer = (*SecpSigner)(nil)

// ErrUnsupportedAddrProtocol is returned when callers pass an address that
// is not `f1` (secp), `f3` (bls), or `f4` (delegated).
var ErrUnsupportedAddrProtocol = errors.New("unsupported address protocol")
