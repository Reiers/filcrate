package mk20

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/crypto"
)

// stubSigner is a minimal Signer for testing the auth-header construction
// machinery without a real key. It returns deterministic bytes so callers
// can assert exact header values.
type stubSigner struct {
	addr        address.Address
	keyType     string
	signedBytes []byte
}

func (s *stubSigner) Address() address.Address { return s.addr }
func (s *stubSigner) KeyType() string           { return s.keyType }
func (s *stubSigner) SignDigest(_ context.Context, digest []byte) ([]byte, error) {
	s.signedBytes = append(s.signedBytes[:0], digest...)
	return []byte("DETERMINISTIC-SIG"), nil
}

func newStubSigner(t *testing.T) *stubSigner {
	t.Helper()
	addr, err := address.NewIDAddress(1234)
	if err != nil {
		t.Fatalf("NewIDAddress: %v", err)
	}
	return &stubSigner{addr: addr, keyType: "secp256k1"}
}

func TestAuthHeader_PreimageMatchesSPProtocol(t *testing.T) {
	signer := newStubSigner(t)

	header, err := AuthHeader(context.Background(), signer, "GET", "/market/mk20/products")
	if err != nil {
		t.Fatalf("AuthHeader: %v", err)
	}

	// Reconstruct the expected preimage server-side and compare digests.
	expected := bytes.Join([][]byte{
		signer.Address().Bytes(),
		[]byte("GET"),
		[]byte("/market/mk20/products"),
		[]byte(time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)),
	}, []byte{})
	expectedDigest := sha256.Sum256(expected)

	if !bytes.Equal(signer.signedBytes, expectedDigest[:]) {
		t.Fatalf("digest mismatch:\n  signed:   %x\n  expected: %x", signer.signedBytes, expectedDigest)
	}

	if !strings.HasPrefix(header, "CurioAuth secp256k1:") {
		t.Fatalf("header missing CurioAuth secp256k1 prefix: %q", header)
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "CurioAuth "), ":", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3-part header, got %d", len(parts))
	}
	if got, _ := base64.StdEncoding.DecodeString(parts[1]); !bytes.Equal(got, signer.Address().Bytes()) {
		t.Fatalf("middle segment is not base64(addr.Bytes())")
	}
	if got, _ := base64.StdEncoding.DecodeString(parts[2]); !bytes.Equal(got, []byte("DETERMINISTIC-SIG")) {
		t.Fatalf("last segment is not base64(signed bytes)")
	}
}

func TestApplyAuth_DefaultsRootPath(t *testing.T) {
	signer := newStubSigner(t)
	req := httptest.NewRequest("POST", "/", nil)

	if err := ApplyAuth(context.Background(), signer, req); err != nil {
		t.Fatalf("ApplyAuth: %v", err)
	}
	if got := req.Header.Get("Authorization"); !strings.HasPrefix(got, "CurioAuth ") {
		t.Fatalf("missing CurioAuth header: %q", got)
	}
}

func TestApplyAuth_UsesEscapedPath(t *testing.T) {
	signer := newStubSigner(t)
	// chi uses EscapedPath; ensure we use the escaped form so our digest
	// matches the SP-side computation when the URL contains special chars.
	req := httptest.NewRequest("GET", "/market/mk20/status/01J%2BZ", nil)
	if err := ApplyAuth(context.Background(), signer, req); err != nil {
		t.Fatalf("ApplyAuth: %v", err)
	}

	expectedPath := req.URL.EscapedPath()
	expected := bytes.Join([][]byte{
		signer.Address().Bytes(),
		[]byte("GET"),
		[]byte(expectedPath),
		[]byte(time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)),
	}, []byte{})
	expectedDigest := sha256.Sum256(expected)

	if !bytes.Equal(signer.signedBytes, expectedDigest[:]) {
		t.Fatalf("digest used wrong path. expected escaped %q", expectedPath)
	}
}

func TestWrapSignature_SecpRoundtrip(t *testing.T) {
	raw := bytes.Repeat([]byte{0xab}, 65)
	wrapped, err := WrapSignature(crypto.SigTypeSecp256k1, raw)
	if err != nil {
		t.Fatalf("WrapSignature: %v", err)
	}

	var got crypto.Signature
	if err := got.UnmarshalBinary(wrapped); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if got.Type != crypto.SigTypeSecp256k1 {
		t.Fatalf("type mismatch: %d", got.Type)
	}
	if !bytes.Equal(got.Data, raw) {
		t.Fatalf("data mismatch")
	}
}

func TestApplyAuth_RegeneratesPerRequest(t *testing.T) {
	// Header construction includes the minute timestamp; two consecutive
	// calls in the same minute MUST produce identical digests but the
	// design should remain free of any caching layer that pins old values.
	signer := newStubSigner(t)
	req1 := httptest.NewRequest(http.MethodGet, "/market/mk20/products", nil)
	req2 := httptest.NewRequest(http.MethodGet, "/market/mk20/products", nil)

	if err := ApplyAuth(context.Background(), signer, req1); err != nil {
		t.Fatal(err)
	}
	if err := ApplyAuth(context.Background(), signer, req2); err != nil {
		t.Fatal(err)
	}
	// In the same minute, the headers will match. That's fine; what we are
	// asserting is that ApplyAuth doesn't crash on repeated calls.
	if req1.Header.Get("Authorization") == "" || req2.Header.Get("Authorization") == "" {
		t.Fatal("expected both requests to carry an Authorization header")
	}
}
