package mk20

import (
	"errors"
	"fmt"

	"github.com/ipfs/go-cid"
	"github.com/oklog/ulid/v2"
)

// DealOpts is a flat parameter bag for the most common deal shapes. It is
// shaped for ergonomics, not protocol fidelity — every field gets validated
// and translated into the canonical Deal envelope before submission.
//
// The two supported high-level shapes today:
//
//	NewDDODeal: cold storage, sealed sectors. Either ColdAllocationID
//	            (FIL+ DataCap) or ColdMarketAddress (paid via custom market
//	            contract) must be set; raw paid mk1.2 deals are not supported.
//
//	NewPDPDeal: warm storage. Requires HotDataSetID (created up-front via
//	            NewPDPCreateDataSetDeal) and a record-keeper contract.
//
// `RetrievalIndexing` defaults to true; flip it off only if you really do
// want a "deal exists but cannot be discovered" outcome.
type DealOpts struct {
	// Required for any deal: the f1/f4 client address as a string.
	ClientAddress string

	// Required for any deal that includes byte data (everything except
	// "control plane" PDP ops like CreateDataSet).
	PieceCID    cid.Cid
	PayloadSize uint64

	// HTTP source: SP fetches the bytes itself.
	HTTPSourceURL string

	// HTTP-PUT source: SP accepts client-pushed bytes after the deal is
	// accepted.
	HTTPPutSource bool

	// Cold-storage (DDO) parameters.
	ColdProvider       string
	ColdDuration       int64 // epochs; defaults to 518400 (~180 days)
	ColdAllocationID   *uint64
	ColdMarketAddress  string
	ColdMarketDealID   *uint64
	ColdNotifyAddress  string
	ColdStartEpoch     *int64

	// Warm-storage (PDP) parameters.
	HotDataSetID  *uint64
	HotRecordKeeper string

	// Retrieval product is attached to most deals so the piece is
	// announceable on IPNI and discoverable.
	RetrievalIndexing      *bool // defaults to true
	RetrievalIndexAnnounce *bool // defaults to true
}

// NewDDODeal builds a sealed-storage Mk20 deal envelope.
func NewDDODeal(opts DealOpts) (*Deal, error) {
	if err := opts.validateClient(); err != nil {
		return nil, err
	}
	if opts.ColdProvider == "" {
		return nil, errors.New("ColdProvider is required for DDO deals")
	}
	if opts.ColdAllocationID == nil && opts.ColdMarketAddress == "" {
		return nil, errors.New("DDO deals require either ColdAllocationID (FIL+) or ColdMarketAddress (paid)")
	}

	data, err := opts.buildDataSource()
	if err != nil {
		return nil, err
	}

	duration := opts.ColdDuration
	if duration == 0 {
		duration = 518400 // ~180 days at 30s epochs
	}

	return &Deal{
		Identifier: newULID(),
		Client:     opts.ClientAddress,
		Data:       data,
		Products: Products{
			DDOV1: &DDOV1{
				Provider:            opts.ColdProvider,
				StartEpoch:          opts.ColdStartEpoch,
				Duration:            duration,
				AllocationID:        opts.ColdAllocationID,
				MarketAddress:       opts.ColdMarketAddress,
				MarketDealID:        opts.ColdMarketDealID,
				NotificationAddress: opts.ColdNotifyAddress,
			},
			RetrievalV1: opts.buildRetrieval(),
		},
	}, nil
}

// NewPDPDeal builds a warm-storage AddPiece deal envelope.
//
// Note: PDP deals require a pre-existing data set on the SP. To create one,
// use NewPDPCreateDataSetDeal first; submit that, wait for the data set ID,
// then call NewPDPDeal with that ID.
func NewPDPDeal(opts DealOpts) (*Deal, error) {
	if err := opts.validateClient(); err != nil {
		return nil, err
	}
	if opts.HotDataSetID == nil {
		return nil, errors.New("PDP AddPiece deals require HotDataSetID")
	}
	if opts.HotRecordKeeper == "" {
		return nil, errors.New("PDP deals require HotRecordKeeper (FWSS contract address or equivalent)")
	}

	data, err := opts.buildDataSource()
	if err != nil {
		return nil, err
	}

	return &Deal{
		Identifier: newULID(),
		Client:     opts.ClientAddress,
		Data:       data,
		Products: Products{
			PDPV1: &PDPV1{
				AddPiece:     true,
				DataSetID:    opts.HotDataSetID,
				RecordKeeper: opts.HotRecordKeeper,
			},
			RetrievalV1: opts.buildRetrieval(),
		},
	}, nil
}

// NewPDPCreateDataSetDeal builds the control-plane deal that creates a new
// PDP data set on the SP. There's no piece data attached (`Data` is nil and
// the SP accepts that for control-plane ops).
func NewPDPCreateDataSetDeal(clientAddr, recordKeeper string, extraData []byte) (*Deal, error) {
	if clientAddr == "" {
		return nil, errors.New("clientAddr is required")
	}
	if recordKeeper == "" {
		return nil, errors.New("recordKeeper is required")
	}
	return &Deal{
		Identifier: newULID(),
		Client:     clientAddr,
		Products: Products{
			PDPV1: &PDPV1{
				CreateDataSet: true,
				RecordKeeper:  recordKeeper,
				ExtraData:     extraData,
			},
		},
	}, nil
}

// validateClient checks the bare-minimum fields any deal needs.
func (o DealOpts) validateClient() error {
	if o.ClientAddress == "" {
		return errors.New("ClientAddress is required")
	}
	return nil
}

// buildDataSource translates DealOpts into a canonical *DataSource. Returns
// an error if a piece is required (everything except control-plane PDP) and
// the inputs don't describe one.
func (o DealOpts) buildDataSource() (*DataSource, error) {
	if !o.PieceCID.Defined() {
		return nil, errors.New("PieceCID is required (compute with pkg/piece.File)")
	}
	if o.PayloadSize == 0 {
		return nil, errors.New("PayloadSize is required (compute with pkg/piece.File)")
	}

	ds := &DataSource{
		PieceCID: o.PieceCID,
		Format:   PieceDataFormat{Raw: &FormatBytes{}},
	}

	switch {
	case o.HTTPPutSource:
		if o.HTTPSourceURL != "" {
			return nil, errors.New("set either HTTPSourceURL or HTTPPutSource, not both")
		}
		ds.SourceHTTPPut = &DataSourceHTTPPut{}
	case o.HTTPSourceURL != "":
		ds.SourceHTTP = &DataSourceHTTP{
			URLs: []HTTPURL{
				{
					URL:      o.HTTPSourceURL,
					Priority: 0,
					Fallback: true,
				},
			},
		}
	default:
		return nil, fmt.Errorf("either HTTPSourceURL or HTTPPutSource must be set")
	}

	return ds, nil
}

// buildRetrieval honors the RetrievalIndexing / RetrievalIndexAnnounce
// pointers, defaulting both to true when callers don't override.
func (o DealOpts) buildRetrieval() *RetrievalV1 {
	indexing := true
	announce := true
	if o.RetrievalIndexing != nil {
		indexing = *o.RetrievalIndexing
	}
	if o.RetrievalIndexAnnounce != nil {
		announce = *o.RetrievalIndexAnnounce
	}
	return &RetrievalV1{
		Indexing:      indexing,
		IndexAnnounce: announce,
	}
}

// newULID returns a fresh, monotonic-friendly ULID.
func newULID() ulid.ULID {
	return ulid.Make()
}
