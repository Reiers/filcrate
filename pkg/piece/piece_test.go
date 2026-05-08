package piece

import (
	"bytes"
	"strings"
	"testing"
)

func TestReader_DeterministicForFixedInput(t *testing.T) {
	// 1 MiB of repeating bytes — small enough to be fast, large enough to
	// exercise the streaming path.
	payload := bytes.Repeat([]byte("filcrate-test"), 80_000) // 1.04 MiB

	a, err := Reader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Reader (run 1): %v", err)
	}
	b, err := Reader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Reader (run 2): %v", err)
	}
	if a.PieceCID.String() != b.PieceCID.String() {
		t.Fatalf("PieceCID is not deterministic: %s vs %s", a.PieceCID, b.PieceCID)
	}
	if a.PayloadSize != uint64(len(payload)) {
		t.Fatalf("PayloadSize: got %d, want %d", a.PayloadSize, len(payload))
	}
	if a.PaddedPieceSize == 0 || (a.PaddedPieceSize&(a.PaddedPieceSize-1)) != 0 {
		t.Fatalf("PaddedPieceSize must be a non-zero power of two, got %d", a.PaddedPieceSize)
	}
	if a.PaddedPieceSize < a.PayloadSize {
		t.Fatalf("PaddedPieceSize (%d) must be >= PayloadSize (%d)", a.PaddedPieceSize, a.PayloadSize)
	}
}

func TestReader_RejectsNil(t *testing.T) {
	if _, err := Reader(nil); err == nil {
		t.Fatal("expected error on nil reader")
	}
}

func TestReader_PieceCIDv2Format(t *testing.T) {
	// commP is undefined for payloads shorter than 65 bytes, so use 128.
	info, err := Reader(bytes.NewReader(bytes.Repeat([]byte("x"), 128)))
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	s := info.PieceCID.String()
	// PieceCID v2 is a CIDv1 with a Filecoin-specific codec; serialized
	// strings under the default base32 multibase begin with `bafkz`.
	if !strings.HasPrefix(s, "bafk") {
		t.Fatalf("expected PieceCID v2 to start with bafk, got %q", s)
	}
	t.Logf("piece cid v2: %s (payload=%d, padded=%d)", s, info.PayloadSize, info.PaddedPieceSize)
}

func TestReader_RejectsTooShort(t *testing.T) {
	if _, err := Reader(bytes.NewReader([]byte("too short"))); err == nil {
		t.Fatal("expected error for input under 65 bytes")
	}
}
