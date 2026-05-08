package mk20

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ipfs/go-cid"

	commcid "github.com/filecoin-project/go-fil-commcid"
)

// fixturePieceCID returns a deterministic, well-formed PieceCID v2 for
// shape-only tests. The bytes don't have to round-trip; we just need a
// valid CID.
func fixturePieceCID(t *testing.T) cid.Cid {
	t.Helper()
	digest := bytes.Repeat([]byte{0xab}, 32)
	c, err := commcid.DataCommitmentToPieceCidv2(digest, 1024)
	if err != nil {
		t.Fatalf("DataCommitmentToPieceCidv2: %v", err)
	}
	return c
}

func TestNewDDODeal_RequiresAllocationOrMarketAddress(t *testing.T) {
	_, err := NewDDODeal(DealOpts{
		ClientAddress: "f1abc",
		PieceCID:      fixturePieceCID(t),
		PayloadSize:   1024,
		HTTPSourceURL: "https://example.com/p.car",
		ColdProvider:  "f01000",
	})
	if err == nil || !strings.Contains(err.Error(), "ColdAllocationID") {
		t.Fatalf("expected ColdAllocationID error, got: %v", err)
	}
}

func TestNewDDODeal_FILPlusShape(t *testing.T) {
	allocID := uint64(42)
	deal, err := NewDDODeal(DealOpts{
		ClientAddress:    "f1abc",
		PieceCID:         fixturePieceCID(t),
		PayloadSize:      1024,
		HTTPSourceURL:    "https://example.com/p.car",
		ColdProvider:     "f01000",
		ColdAllocationID: &allocID,
	})
	if err != nil {
		t.Fatalf("NewDDODeal: %v", err)
	}
	if deal.Products.DDOV1 == nil {
		t.Fatal("expected DDOV1 product set")
	}
	if deal.Products.DDOV1.Duration != 518400 {
		t.Fatalf("expected default duration 518400, got %d", deal.Products.DDOV1.Duration)
	}
	if deal.Data.SourceHTTP == nil || len(deal.Data.SourceHTTP.URLs) != 1 {
		t.Fatal("expected one HTTP source URL")
	}
	if deal.Data.SourceHTTP.URLs[0].URL != "https://example.com/p.car" {
		t.Fatalf("URL not preserved: %s", deal.Data.SourceHTTP.URLs[0].URL)
	}
	// RetrievalV1 defaults: indexing + announce both true.
	if r := deal.Products.RetrievalV1; r == nil || !r.Indexing || !r.IndexAnnounce {
		t.Fatalf("retrieval defaults wrong: %+v", r)
	}
}

func TestNewPDPDeal_RequiresDataSetAndRecordKeeper(t *testing.T) {
	_, err := NewPDPDeal(DealOpts{
		ClientAddress: "f1abc",
		PieceCID:      fixturePieceCID(t),
		PayloadSize:   1024,
		HTTPPutSource: true,
	})
	if err == nil {
		t.Fatal("expected error without HotDataSetID")
	}

	dataSet := uint64(1)
	_, err = NewPDPDeal(DealOpts{
		ClientAddress: "f1abc",
		PieceCID:      fixturePieceCID(t),
		PayloadSize:   1024,
		HTTPPutSource: true,
		HotDataSetID:  &dataSet,
	})
	if err == nil {
		t.Fatal("expected error without HotRecordKeeper")
	}

	deal, err := NewPDPDeal(DealOpts{
		ClientAddress:   "f1abc",
		PieceCID:        fixturePieceCID(t),
		PayloadSize:     1024,
		HTTPPutSource:   true,
		HotDataSetID:    &dataSet,
		HotRecordKeeper: "0xfeedface",
	})
	if err != nil {
		t.Fatalf("NewPDPDeal: %v", err)
	}
	if deal.Products.PDPV1 == nil || !deal.Products.PDPV1.AddPiece {
		t.Fatal("expected PDPV1 with AddPiece set")
	}
	if deal.Data.SourceHTTPPut == nil {
		t.Fatal("expected HTTP-PUT source")
	}
}

func TestNewPDPCreateDataSetDeal_NoData(t *testing.T) {
	deal, err := NewPDPCreateDataSetDeal("f1abc", "0xrk", []byte("hello"))
	if err != nil {
		t.Fatalf("NewPDPCreateDataSetDeal: %v", err)
	}
	if deal.Data != nil {
		t.Fatal("control-plane CreateDataSet must not carry data")
	}
	if !deal.Products.PDPV1.CreateDataSet {
		t.Fatal("expected CreateDataSet=true")
	}
}

func TestDealOpts_SourceMutualExclusion(t *testing.T) {
	allocID := uint64(1)
	_, err := NewDDODeal(DealOpts{
		ClientAddress:    "f1abc",
		PieceCID:         fixturePieceCID(t),
		PayloadSize:      1024,
		ColdProvider:     "f01000",
		ColdAllocationID: &allocID,
		HTTPSourceURL:    "https://example.com/p.car",
		HTTPPutSource:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected mutual-exclusion error, got: %v", err)
	}
}
