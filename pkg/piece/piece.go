// Package piece computes Filecoin Piece commitments.
//
// PieceCID v2 (FRC-0069) is the canonical content identifier used by the
// Curio Mk20 protocol. It encodes both the data commitment ("commP", a
// merkle root over the padded raw bytes) and the original payload size, so
// an SP can verify "this piece matches my expected commitment AND has the
// right size" from a single CID.
//
// We rely on `go-fil-commp-hashhash` for the commP computation and
// `go-fil-commcid` for the v2 CID wrapper. Both are pure Go.
package piece

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ipfs/go-cid"

	commcid "github.com/filecoin-project/go-fil-commcid"
	commp "github.com/filecoin-project/go-fil-commp-hashhash"
)

// Info is the result of computing a PieceCID over a stream of bytes.
type Info struct {
	// PieceCID is the FRC-0069 PieceCID v2.
	PieceCID cid.Cid `json:"piece_cid"`

	// PayloadSize is the raw byte count consumed from the input.
	PayloadSize uint64 `json:"payload_size"`

	// PaddedPieceSize is the size the SP will see after Filecoin's pre-padding
	// scheme — always a power of two ≥ 128 bytes. Useful for cost estimation.
	PaddedPieceSize uint64 `json:"padded_piece_size"`
}

// Reader streams `r` through the commP hasher and produces a PieceCID v2.
//
// `r` is read to EOF in 2 MiB chunks; for very large files this is the same
// chunk size Curio's own toolbox uses, so memory pressure is bounded.
//
// The returned PaddedPieceSize is the value the SP advertises on-chain; the
// PayloadSize is the original byte count (what the user uploaded).
func Reader(r io.Reader) (*Info, error) {
	if r == nil {
		return nil, errors.New("reader is nil")
	}

	hasher := new(commp.Calc)
	defer hasher.Reset()

	n, err := io.CopyBuffer(hasher, r, make([]byte, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("hashing input: %w", err)
	}

	digest, padded, err := hasher.Digest()
	if err != nil {
		return nil, fmt.Errorf("commp digest: %w", err)
	}

	pcid, err := commcid.DataCommitmentToPieceCidv2(digest, uint64(n))
	if err != nil {
		return nil, fmt.Errorf("wrapping data commitment as PieceCID v2: %w", err)
	}

	return &Info{
		PieceCID:        pcid,
		PayloadSize:     uint64(n),
		PaddedPieceSize: padded,
	}, nil
}

// File is a convenience wrapper around Reader for a path on disk. It opens
// the file, streams it through the hasher, and closes the file on the way
// out.
func File(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if stat.IsDir() {
		return nil, fmt.Errorf("%s is a directory; pass a file path", path)
	}

	info, err := Reader(f)
	if err != nil {
		return nil, err
	}
	return info, nil
}
