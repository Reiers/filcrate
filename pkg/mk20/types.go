// Package mk20 is a pure-Go implementation of the Curio Market 2.0 client
// protocol. It contains only the surface a client needs: deal envelope types,
// the auth-header builder, and an HTTP client. It does not import Curio's
// SP-side handler or any of its transitive heavy dependencies (filecoin-ffi,
// harmonydb, etc.).
//
// Protocol reference: https://github.com/filecoin-project/curio
//   `market/mk20/types.go`     - canonical type definitions
//   `market/mk20/http/http.go` - REST endpoint surface
//   `market/mk20/client/`      - SP-internal Go client (we mirror its shape)
package mk20

import (
	"net/http"

	"github.com/ipfs/go-cid"
	"github.com/oklog/ulid/v2"
)

// Deal mirrors curio/market/mk20.Deal. It is the JSON envelope POSTed to
// `<sp>/market/mk20/deal`.
type Deal struct {
	Identifier ulid.ULID   `json:"identifier"`
	Client     string      `json:"client"`
	Data       *DataSource `json:"data,omitempty"`
	Products   Products    `json:"products"`
}

// Products is the union of product-specific configs attached to a deal.
type Products struct {
	DDOV1       *DDOV1       `json:"ddo_v1,omitempty"`
	RetrievalV1 *RetrievalV1 `json:"retrieval_v1,omitempty"`
	PDPV1       *PDPV1       `json:"pdp_v1,omitempty"`
}

// DataSource describes how the SP will obtain the piece bytes.
type DataSource struct {
	PieceCID        cid.Cid              `json:"piece_cid"`
	Format          PieceDataFormat      `json:"format"`
	SourceHTTP      *DataSourceHTTP      `json:"source_http,omitempty"`
	SourceAggregate *DataSourceAggregate `json:"source_aggregate,omitempty"`
	SourceOffline   *DataSourceOffline   `json:"source_offline,omitempty"`
	SourceHTTPPut   *DataSourceHTTPPut   `json:"source_http_put,omitempty"`
}

// PieceDataFormat is one of the three supported wire formats.
type PieceDataFormat struct {
	Car       *FormatCar       `json:"car,omitempty"`
	Aggregate *FormatAggregate `json:"aggregate,omitempty"`
	Raw       *FormatBytes     `json:"raw,omitempty"`
}

// FormatCar marks the piece as a CAR file.
type FormatCar struct{}

// FormatBytes marks the piece as raw bytes.
type FormatBytes struct{}

// FormatAggregate marks the piece as a PoDSI aggregate (FRC-0058).
type FormatAggregate struct {
	Type AggregateType `json:"type"`
	Sub  []DataSource  `json:"sub"`
}

// AggregateType enumerates supported aggregation schemes.
type AggregateType int

const (
	AggregateTypeNone AggregateType = 0
	AggregateTypeV1   AggregateType = 1
)

// DataSourceHTTP fetches the piece from one of the supplied URLs (priority +
// fallback semantics).
type DataSourceHTTP struct {
	URLs []HTTPURL `json:"urls"`
}

// HTTPURL is a single HTTP source for a piece.
type HTTPURL struct {
	URL      string      `json:"url"`
	Headers  http.Header `json:"headers"`
	Priority int         `json:"priority"`
	Fallback bool        `json:"fallback"`
}

// DataSourceHTTPPut indicates the client will push the piece to the SP after
// the deal is accepted.
type DataSourceHTTPPut struct{}

// DataSourceOffline indicates the bytes are already locally available to the
// SP (out-of-band transfer).
type DataSourceOffline struct{}

// DataSourceAggregate is an aggregated piece composed of sub-pieces.
type DataSourceAggregate struct {
	Pieces []DataSource `json:"pieces"`
}

// DDOV1 (Direct Data Onboarding v1) is the sealed-storage product.
//
// AllocationId attaches a FIL+ allocation; without an allocation, the SP
// must enforce payment via a market_address contract.
type DDOV1 struct {
	Provider            string  `json:"provider"`
	StartEpoch          *int64  `json:"start_epoch,omitempty"`
	Duration            int64   `json:"duration"`
	AllocationID        *uint64 `json:"allocation_id,omitempty"`
	MarketAddress       string  `json:"market_address,omitempty"`
	MarketDealID        *uint64 `json:"market_deal_id,omitempty"`
	NotificationAddress string  `json:"notification_address,omitempty"`
	NotificationPayload []byte  `json:"notification_payload,omitempty"`
}

// PDPV1 (Proof of Data Possession v1) is the warm-storage product. Requires
// a pre-existing data set + record-keeper.
type PDPV1 struct {
	CreateDataSet bool    `json:"create_data_set,omitempty"`
	DeleteDataSet bool    `json:"delete_data_set,omitempty"`
	AddPiece      bool    `json:"add_piece,omitempty"`
	DeletePiece   bool    `json:"delete_piece,omitempty"`
	DataSetID     *uint64 `json:"data_set_id,omitempty"`
	PieceID       *uint64 `json:"piece_id,omitempty"`
	RecordKeeper  string  `json:"record_keeper,omitempty"`
	ExtraData     []byte  `json:"extra_data,omitempty"`
}

// RetrievalV1 declares the retrieval surface the client wants enabled for the
// deal.
type RetrievalV1 struct {
	IndexAnnounce bool `json:"index_announce,omitempty"`
	Indexing      bool `json:"indexing,omitempty"`
}

// SupportedProducts is the response of `GET /market/mk20/products`.
type SupportedProducts struct {
	Products []string `json:"products"`
}

// SupportedDataSources is the response of `GET /market/mk20/sources`.
type SupportedDataSources struct {
	Sources []string `json:"sources"`
}

// SupportedContracts is the response of `GET /market/mk20/contracts`.
type SupportedContracts struct {
	Contracts []string `json:"contracts"`
}

// DealCode mirrors the protocol-defined return codes Curio uses on deal
// submission. Values match HTTP status codes where it makes sense; some are
// in the 4xx range outside that mapping.
type DealCode int

const (
	DealCodeOK                   DealCode = 200
	DealCodeBadProposal          DealCode = 400
	DealCodeUnauthorized         DealCode = 401
	DealCodeNotFound             DealCode = 404
	DealCodeServiceOverloaded    DealCode = 429
	DealCodeMalformedDataSource  DealCode = 430
	DealCodeUnsupportedDataSource DealCode = 422
	DealCodeUnsupportedProduct   DealCode = 423
	DealCodeProductNotEnabled    DealCode = 424
	DealCodeProductValidation    DealCode = 425
	DealCodeRejectedByMarket     DealCode = 426
	DealCodeMarketNotEnabled     DealCode = 440
	DealCodeDurationTooShort     DealCode = 441
	DealCodeServerError          DealCode = 500
	DealCodeServiceMaintenance   DealCode = 503
)
