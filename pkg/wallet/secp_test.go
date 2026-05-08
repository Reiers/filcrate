package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/filecoin-project/go-address"
	gocrypto "github.com/filecoin-project/go-crypto"
	"github.com/filecoin-project/go-state-types/crypto"
	"golang.org/x/crypto/blake2b"
)

// Test fixture: a deterministic 32-byte private key. Generated once for this
// test and committed to the repo. The corresponding f1 address can be
// reproduced by anyone running the test — there's no secret material here.
//
//	priv (hex): 0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20
const fixturePrivHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func TestNewSecpSigner_RejectsBadKeyLength(t *testing.T) {
	if _, err := NewSecpSigner(make([]byte, 31)); err == nil {
		t.Fatal("expected error on 31-byte key")
	}
	if _, err := NewSecpSigner(make([]byte, 33)); err == nil {
		t.Fatal("expected error on 33-byte key")
	}
}

func TestSecpSigner_AddressIsF1(t *testing.T) {
	priv := mustHex(t, fixturePrivHex)
	signer, err := NewSecpSigner(priv)
	if err != nil {
		t.Fatalf("NewSecpSigner: %v", err)
	}
	addr := signer.Address()

	if addr.Protocol() != address.SECP256K1 {
		t.Fatalf("expected SECP256K1 (f1) address, got protocol %d", addr.Protocol())
	}
	if got := addr.String()[:1]; got != "f" && got != "t" {
		t.Fatalf("expected f-prefixed address, got %s", addr.String())
	}
	t.Logf("derived f1 address: %s", addr.String())
}

func TestSecpSigner_SignatureIsRecoverableAndVerifies(t *testing.T) {
	priv := mustHex(t, fixturePrivHex)
	signer, err := NewSecpSigner(priv)
	if err != nil {
		t.Fatalf("NewSecpSigner: %v", err)
	}

	// Imitate the Mk20 client preimage construction (`pubkey || ...`).
	preimage := []byte("filcrate-test")
	digest := sha256.Sum256(preimage)

	sigBytes, err := signer.SignDigest(context.Background(), digest[:])
	if err != nil {
		t.Fatalf("SignDigest: %v", err)
	}

	var sig crypto.Signature
	if err := sig.UnmarshalBinary(sigBytes); err != nil {
		t.Fatalf("decoding wrapped signature: %v", err)
	}
	if sig.Type != crypto.SigTypeSecp256k1 {
		t.Fatalf("expected SigTypeSecp256k1, got %d", sig.Type)
	}
	if len(sig.Data) != 65 {
		t.Fatalf("expected 65-byte recoverable signature, got %d", len(sig.Data))
	}

	// Filecoin's secp scheme verifies by EcRecover-ing the inner hash and
	// comparing to the address. We replicate that here.
	inner := blake2bSum(digest[:])
	pub, err := gocrypto.EcRecover(inner, sig.Data)
	if err != nil {
		t.Fatalf("EcRecover: %v", err)
	}
	recoveredAddr, err := address.NewSecp256k1Address(pub)
	if err != nil {
		t.Fatalf("deriving address from recovered pubkey: %v", err)
	}
	if recoveredAddr != signer.Address() {
		t.Fatalf("recovered %s, expected %s", recoveredAddr, signer.Address())
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding hex: %v", err)
	}
	return b
}

func blake2bSum(b []byte) []byte {
	h, err := blake2b.New256(nil)
	if err != nil {
		panic(err)
	}
	_, _ = h.Write(b)
	return h.Sum(nil)
}
