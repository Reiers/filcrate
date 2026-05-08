package mk20_test

// End-to-end integration tests: filcrate's Mk20 client against an in-process
// mock SP. The mock implements the same auth verification as the real
// Curio handler, so any signing bug in the client surfaces here.

import (
	"bytes"
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	commcid "github.com/filecoin-project/go-fil-commcid"

	"github.com/Reiers/filcrate/pkg/mk20"
	"github.com/Reiers/filcrate/pkg/mk20/mockserver"
	"github.com/Reiers/filcrate/pkg/wallet"
)

func freshSigner(t *testing.T) *wallet.SecpSigner {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatalf("rand: %v", err)
	}
	s, err := wallet.NewSecpSigner(priv)
	if err != nil {
		t.Fatalf("NewSecpSigner: %v", err)
	}
	return s
}

func TestIntegration_CapabilityProbe(t *testing.T) {
	srv := mockserver.New()
	srv.Start()
	defer srv.Close()

	signer := freshSigner(t)
	c, err := mk20.NewClient(srv.URL(), signer)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	products, err := c.Products(context.Background())
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("expected at least one product")
	}

	sources, err := c.DataSources(context.Background())
	if err != nil {
		t.Fatalf("DataSources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("expected at least one data source")
	}

	// Contracts: by default the mock has none, so we expect a 404.
	if _, err := c.Contracts(context.Background()); err == nil {
		t.Fatal("expected 404 on contracts when mock has none")
	} else if !mk20.IsNotFound(err) {
		t.Fatalf("expected 404 NotFound, got: %v", err)
	}
}

func TestIntegration_AuthRejectsBadSigner(t *testing.T) {
	srv := mockserver.New()
	srv.Start()
	defer srv.Close()

	// AllowedClients gates by recovered address. We deliberately allow only
	// a different address than the signer we'll use, so auth fails.
	srv.AllowedClients = map[string]bool{
		"f1someotherclient0000000000000000000000": true,
	}

	c, err := mk20.NewClient(srv.URL(), freshSigner(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Products(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !mk20.IsAuthError(err) {
		t.Fatalf("expected IsAuthError, got: %v", err)
	}
}

func TestIntegration_FullDDOFlow_HTTPSource(t *testing.T) {
	srv := mockserver.New()
	srv.Start()
	defer srv.Close()

	signer := freshSigner(t)
	c, _ := mk20.NewClient(srv.URL(), signer)

	digest := bytes.Repeat([]byte{0x42}, 32)
	pcid, err := commcid.DataCommitmentToPieceCidv2(digest, 1024)
	if err != nil {
		t.Fatalf("PieceCID: %v", err)
	}

	allocID := uint64(99)
	deal, err := mk20.NewDDODeal(mk20.DealOpts{
		ClientAddress:    signer.Address().String(),
		PieceCID:         pcid,
		PayloadSize:      1024,
		HTTPSourceURL:    "https://example.com/p.car",
		ColdProvider:     "f01000",
		ColdAllocationID: &allocID,
	})
	if err != nil {
		t.Fatalf("NewDDODeal: %v", err)
	}

	id, err := c.SubmitDeal(context.Background(), deal)
	if err != nil {
		t.Fatalf("SubmitDeal: %v", err)
	}
	if id != deal.Identifier {
		t.Fatalf("identifier mismatch")
	}
	if got := srv.DealCount(); got != 1 {
		t.Fatalf("expected 1 deal at SP, got %d", got)
	}

	status, err := c.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != "accepted" {
		t.Fatalf("expected accepted state, got %q", status.State)
	}
}

func TestIntegration_PDPSerialUpload(t *testing.T) {
	srv := mockserver.New()
	srv.Start()
	defer srv.Close()

	signer := freshSigner(t)
	c, _ := mk20.NewClient(srv.URL(), signer)

	payload := bytes.Repeat([]byte{0xab}, 1<<16) // 64 KiB
	digest := bytes.Repeat([]byte{0xcd}, 32)
	pcid, _ := commcid.DataCommitmentToPieceCidv2(digest, uint64(len(payload)))

	dataSet := uint64(7)
	deal, err := mk20.NewPDPDeal(mk20.DealOpts{
		ClientAddress:   signer.Address().String(),
		PieceCID:        pcid,
		PayloadSize:     uint64(len(payload)),
		HTTPPutSource:   true,
		HotDataSetID:    &dataSet,
		HotRecordKeeper: "0xfeedface",
	})
	if err != nil {
		t.Fatalf("NewPDPDeal: %v", err)
	}

	id, err := c.SubmitDeal(context.Background(), deal)
	if err != nil {
		t.Fatalf("SubmitDeal: %v", err)
	}

	if err := c.UploadSerial(context.Background(), id, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("UploadSerial: %v", err)
	}

	got := srv.UploadedBytes(id.String())
	if !bytes.Equal(got, payload) {
		t.Fatalf("uploaded bytes mismatch: got %d bytes, want %d", len(got), len(payload))
	}

	// PollStatus should now flip to "active" because the mock sets uploaded=true.
	final, err := c.PollStatus(context.Background(), id, 10*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("PollStatus: %v", err)
	}
	if final.State != "active" {
		t.Fatalf("expected active, got %q", final.State)
	}
}

func TestIntegration_DealRejection(t *testing.T) {
	srv := mockserver.New()
	srv.Start()
	defer srv.Close()
	srv.FailNextDeal = true

	signer := freshSigner(t)
	c, _ := mk20.NewClient(srv.URL(), signer)

	digest := bytes.Repeat([]byte{0x55}, 32)
	pcid, _ := commcid.DataCommitmentToPieceCidv2(digest, 4096)

	allocID := uint64(1)
	deal, err := mk20.NewDDODeal(mk20.DealOpts{
		ClientAddress:    signer.Address().String(),
		PieceCID:         pcid,
		PayloadSize:      4096,
		HTTPSourceURL:    "https://example.com/p.car",
		ColdProvider:     "f01000",
		ColdAllocationID: &allocID,
	})
	if err != nil {
		t.Fatalf("NewDDODeal: %v", err)
	}

	if _, err := c.SubmitDeal(context.Background(), deal); err == nil {
		t.Fatal("expected SubmitDeal to fail (FailNextDeal=true)")
	} else if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected 'rejected' in error, got: %v", err)
	}

	// Recovery: next deal should succeed.
	deal2, _ := mk20.NewDDODeal(mk20.DealOpts{
		ClientAddress:    signer.Address().String(),
		PieceCID:         pcid,
		PayloadSize:      4096,
		HTTPSourceURL:    "https://example.com/p.car",
		ColdProvider:     "f01000",
		ColdAllocationID: &allocID,
	})
	if _, err := c.SubmitDeal(context.Background(), deal2); err != nil {
		t.Fatalf("expected second deal to succeed, got: %v", err)
	}
}

func TestIntegration_ChunkedUpload_Roundtrip(t *testing.T) {
	srv := mockserver.New()
	srv.Start()
	defer srv.Close()

	signer := freshSigner(t)
	c, _ := mk20.NewClient(srv.URL(), signer)

	// 1.5 MiB payload chunked at 256 KiB → 6 chunks.
	payload := make([]byte, (1<<20)+(1<<19))
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	digest := bytes.Repeat([]byte{0x99}, 32)
	pcid, _ := commcid.DataCommitmentToPieceCidv2(digest, uint64(len(payload)))

	dataSet := uint64(11)
	deal, err := mk20.NewPDPDeal(mk20.DealOpts{
		ClientAddress:   signer.Address().String(),
		PieceCID:        pcid,
		PayloadSize:     uint64(len(payload)),
		HTTPPutSource:   true,
		HotDataSetID:    &dataSet,
		HotRecordKeeper: "0xfeedface",
	})
	if err != nil {
		t.Fatalf("NewPDPDeal: %v", err)
	}

	id, err := c.SubmitDeal(context.Background(), deal)
	if err != nil {
		t.Fatalf("SubmitDeal: %v", err)
	}

	if err := c.UploadChunked(context.Background(), id, bytes.NewReader(payload), int64(len(payload)),
		&mk20.ChunkedUploadOpts{ChunkSize: 1 << 18, Concurrency: 3}); err != nil {
		t.Fatalf("UploadChunked: %v", err)
	}

	got := srv.UploadedBytes(id.String())
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}
