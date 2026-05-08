package mk20

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/crypto"
)

// Signer is the minimal interface a Filecoin wallet must implement to act as
// an Mk20 client. It is wallet-shape, not key-shape: callers can back this
// with a local key file, an external signer, a hardware wallet, or a browser
// wallet relayed over WebSocket.
type Signer interface {
	// Address returns the Filecoin address that backs this signer.
	Address() address.Address

	// SignDigest produces a Filecoin-format binary signature over `digest`,
	// where `digest` is the sha256 of the auth-message preimage. The returned
	// bytes are what the Mk20 protocol expects to base64-encode into the
	// `Authorization` header.
	//
	// For a secp256k1 wallet (`f1`), this is a binary-marshaled
	// `crypto.Signature{Type: SigTypeSecp256k1, Data: <65-byte recoverable sig>}`
	// where the 65 bytes come from the ECDSA-recoverable scheme over a
	// blake2b-256(digest) inner hash. The wrapping is handled by the
	// implementation; callers only see the final byte slice.
	SignDigest(ctx context.Context, digest []byte) ([]byte, error)

	// KeyType returns the Filecoin key-type label used in the auth header
	// (`secp256k1`, `bls`, or `delegated`).
	KeyType() string
}

// AuthHeader builds the value of the `Authorization` header for a given
// Mk20 request, against the supplied Signer.
//
// The auth message preimage is:
//
//	addr.Bytes() || UPPER(method) || requestPath || RFC3339(now truncated to minute)
//
// Mirrors the exact construction in
// https://github.com/filecoin-project/curio/blob/main/market/mk20/auth.go
// `authMessage(...)`. Truncation to the minute means a generated header is
// only valid for ~60 seconds; clients should regenerate the header per
// request rather than reuse it.
func AuthHeader(ctx context.Context, s Signer, method, requestPath string) (string, error) {
	if requestPath == "" {
		requestPath = "/"
	}
	method = strings.ToUpper(method)

	addrBytes := s.Address().Bytes()
	timestamp := time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)

	preimage := bytes.Join([][]byte{
		addrBytes,
		[]byte(method),
		[]byte(requestPath),
		[]byte(timestamp),
	}, []byte{})
	digest := sha256.Sum256(preimage)

	sig, err := s.SignDigest(ctx, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing auth digest: %w", err)
	}

	return fmt.Sprintf(
		"CurioAuth %s:%s:%s",
		s.KeyType(),
		base64.StdEncoding.EncodeToString(addrBytes),
		base64.StdEncoding.EncodeToString(sig),
	), nil
}

// ApplyAuth attaches a fresh `Authorization` header to req using s. If req
// has no escaped path (e.g. a root URL), `/` is used as the canonical path.
func ApplyAuth(ctx context.Context, s Signer, req *http.Request) error {
	path := req.URL.EscapedPath()
	value, err := AuthHeader(ctx, s, req.Method, path)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", value)
	return nil
}

// WrapSignature is a helper that takes raw secp256k1 / BLS / delegated bytes
// produced by an external signer and wraps them in the Filecoin
// `crypto.Signature` binary envelope, ready to be base64-encoded into the
// auth header.
//
// Use this only when your signer returns naked algorithmic bytes; if you can
// implement Signer.SignDigest yourself, prefer that.
func WrapSignature(t crypto.SigType, raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty signature")
	}
	sig := crypto.Signature{Type: t, Data: raw}
	return sig.MarshalBinary()
}
