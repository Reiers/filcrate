// Package mockserver implements an in-process Curio Mk20 server, suitable
// for integration tests of filcrate's client.
//
// What it covers:
//
//   - GET  /market/mk20/products
//   - GET  /market/mk20/sources
//   - GET  /market/mk20/contracts
//   - POST /market/mk20/deal
//   - GET  /market/mk20/status/{id}
//   - PUT  /market/mk20/upload/{id}             (serial body)
//   - POST /market/mk20/upload/{id}             (serial finalize)
//   - POST /market/mk20/uploads/{id}            (chunked start)
//   - PUT  /market/mk20/uploads/{id}/{n}        (chunked body)
//   - POST /market/mk20/uploads/finalize/{id}   (chunked finalize)
//
// What it does NOT cover:
//
//   - On-chain interaction: deals are accepted into an in-memory state
//     machine, no allocation lookups, no contract calls.
//   - Real PieceCID validation: the mock trusts whatever the client says
//     the piece is (clients should still pass a well-formed PieceCID v2).
//   - Backpressure / rate limiting: the mock accepts everything.
//
// The auth flow is real: the mock parses the `Authorization` header using
// the same construction the SP-side does and returns 401 on mismatch.
// This lets us catch signing bugs in the client.
package mockserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	fcrypto "github.com/filecoin-project/go-state-types/crypto"
	gocrypto "github.com/filecoin-project/go-crypto"
	"golang.org/x/crypto/blake2b"

	"github.com/Reiers/filcrate/pkg/mk20"
)

// Server is a configurable Mk20-compatible HTTP server backed by httptest.
type Server struct {
	httpServer *httptest.Server

	mu sync.Mutex

	// Capability surface — match what a real SP would advertise.
	Products  []string
	Sources   []string
	Contracts []string

	// Allowed-clients mode: if non-nil, only these client addresses pass auth.
	// If nil, the mock accepts any wallet that produces a valid signature.
	AllowedClients map[string]bool

	// Deal state machine.
	deals    map[string]*dealState
	uploads  map[string][]byte
	chunks   map[string]map[int][]byte
	chunkMax map[string]int

	// Behaviour knobs for tests.
	//
	// SkipAuth disables the signed-header check entirely. Useful when a
	// test wants to focus on flow rather than auth shape.
	SkipAuth bool
	//
	// FailNextDeal returns DealCodeBadProposal once, then resumes normal
	// behaviour. For testing client error paths.
	FailNextDeal bool
	//
	// SimulateProcessing controls how long deals stay in "processing"
	// before transitioning to "active". 0 means immediate.
	SimulateProcessing time.Duration
}

// dealState is what the mock tracks per accepted deal.
type dealState struct {
	identifier string
	deal       *mk20.Deal
	state      string
	acceptedAt time.Time
	uploaded   bool
}

// New returns a fresh mock server with a sensible default capability set
// (advertises ddo_v1, pdp_v1, retrieval_v1; supports http, http_put,
// aggregate sources). Callers can override before calling Start().
func New() *Server {
	return &Server{
		Products:       []string{"ddo_v1", "pdp_v1", "retrieval_v1"},
		Sources:        []string{"http", "http_put", "aggregate", "offline"},
		Contracts:      nil,
		deals:          map[string]*dealState{},
		uploads:        map[string][]byte{},
		chunks:         map[string]map[int][]byte{},
		chunkMax:       map[string]int{},
	}
}

// Start spins the underlying httptest.Server.
func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/market/mk20/products", s.withAuth(s.handleProducts))
	mux.HandleFunc("/market/mk20/sources", s.withAuth(s.handleSources))
	mux.HandleFunc("/market/mk20/contracts", s.withAuth(s.handleContracts))
	mux.HandleFunc("/market/mk20/deal", s.withAuth(s.handleDeal))
	mux.HandleFunc("/market/mk20/status/", s.withAuth(s.handleStatus))
	mux.HandleFunc("/market/mk20/upload/", s.withAuth(s.handleSerialUpload))
	mux.HandleFunc("/market/mk20/uploads/", s.withAuth(s.handleChunkedUpload))
	s.httpServer = httptest.NewServer(mux)
}

// Close shuts the underlying server down.
func (s *Server) Close() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

// URL returns the base URL of the mock (without the /market/mk20 prefix).
// Pass this directly to mk20.NewClient.
func (s *Server) URL() string { return s.httpServer.URL }

// DealCount returns how many deals have been accepted.
func (s *Server) DealCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deals)
}

// UploadedBytes returns the bytes uploaded for the given deal id, or nil if
// none.
func (s *Server) UploadedBytes(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.uploads[id]; ok {
		return append([]byte(nil), b...)
	}
	return nil
}

// withAuth wraps a handler with mock-side auth verification.
//
// We replay Curio's preimage construction (`addr || METHOD || path || min`)
// and verify the secp256k1 signature. Any deviation from the real SP-side
// protocol surfaces as a 401 here.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.SkipAuth {
			next(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		if header == "" {
			http.Error(w, "Missing Authorization", http.StatusUnauthorized)
			return
		}
		clientAddr, err := s.verifyAuth(header, r.Method, r.URL.EscapedPath())
		if err != nil {
			http.Error(w, fmt.Sprintf("auth: %v", err), http.StatusUnauthorized)
			return
		}
		if s.AllowedClients != nil && !s.AllowedClients[clientAddr] {
			http.Error(w, "client not allowed", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) verifyAuth(header, method, path string) (string, error) {
	if !strings.HasPrefix(header, "CurioAuth ") {
		return "", errors.New("missing CurioAuth prefix")
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "CurioAuth "), ":", 3)
	if len(parts) != 3 {
		return "", errors.New("malformed header")
	}
	keyType := parts[0]
	addrBytes, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("addr base64: %w", err)
	}
	sigRaw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("sig base64: %w", err)
	}

	addr, err := address.NewFromBytes(addrBytes)
	if err != nil {
		return "", fmt.Errorf("addr decode: %w", err)
	}

	if path == "" {
		path = "/"
	}
	preimage := append([]byte{}, addrBytes...)
	preimage = append(preimage, []byte(strings.ToUpper(method))...)
	preimage = append(preimage, []byte(path)...)
	preimage = append(preimage, []byte(time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339))...)
	digest := sha256.Sum256(preimage)

	var sig fcrypto.Signature
	if err := sig.UnmarshalBinary(sigRaw); err != nil {
		return "", fmt.Errorf("sig unmarshal: %w", err)
	}
	if keyType != "secp256k1" {
		// We only verify secp here; bls/delegated would expand this switch.
		return "", fmt.Errorf("unsupported key type in mock: %s", keyType)
	}
	if sig.Type != fcrypto.SigTypeSecp256k1 {
		return "", fmt.Errorf("sig type mismatch: %d", sig.Type)
	}

	innerHasher, _ := blake2b.New256(nil)
	innerHasher.Write(digest[:])
	innerHash := innerHasher.Sum(nil)

	pub, err := gocrypto.EcRecover(innerHash, sig.Data)
	if err != nil {
		return "", fmt.Errorf("ecrecover: %w", err)
	}
	recovered, err := address.NewSecp256k1Address(pub)
	if err != nil {
		return "", fmt.Errorf("derive addr: %w", err)
	}
	if recovered != addr {
		return "", errors.New("signature does not match advertised address")
	}
	return addr.String(), nil
}

func (s *Server) handleProducts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, mk20.SupportedProducts{Products: s.Products})
}

func (s *Server) handleSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, mk20.SupportedDataSources{Sources: s.Sources})
}

func (s *Server) handleContracts(w http.ResponseWriter, _ *http.Request) {
	if len(s.Contracts) == 0 {
		http.Error(w, "no contracts", http.StatusNotFound)
		return
	}
	writeJSON(w, mk20.SupportedContracts{Contracts: s.Contracts})
}

func (s *Server) handleDeal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}
	var deal mk20.Deal
	if err := json.Unmarshal(body, &deal); err != nil {
		http.Error(w, fmt.Sprintf("decode deal: %v", err), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.FailNextDeal {
		s.FailNextDeal = false
		s.mu.Unlock()
		http.Error(w, "deal rejected (test fixture)", http.StatusBadRequest)
		return
	}
	s.deals[deal.Identifier.String()] = &dealState{
		identifier: deal.Identifier.String(),
		deal:       &deal,
		state:      "accepted",
		acceptedAt: time.Now(),
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/market/mk20/status/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	d, ok := s.deals[id]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "deal not found", http.StatusNotFound)
		return
	}
	state := d.state
	if d.uploaded {
		if s.SimulateProcessing > 0 && time.Since(d.acceptedAt) < s.SimulateProcessing {
			state = "processing"
		} else {
			state = "active"
		}
	}
	writeJSON(w, mk20.DealStatus{Identifier: id, State: state})
}

func (s *Server) handleSerialUpload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/market/mk20/upload/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	d, ok := s.deals[id]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "deal not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<30))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.uploads[id] = body
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodPost:
		s.mu.Lock()
		d.uploaded = true
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleChunkedUpload(w http.ResponseWriter, r *http.Request) {
	// /market/mk20/uploads/{id}                 POST start
	// /market/mk20/uploads/{id}/{n}             PUT  chunk
	// /market/mk20/uploads/finalize/{id}        POST finalize
	rest := strings.TrimPrefix(r.URL.Path, "/market/mk20/uploads/")
	switch {
	case strings.HasPrefix(rest, "finalize/"):
		id := strings.TrimPrefix(rest, "finalize/")
		s.mu.Lock()
		d, ok := s.deals[id]
		if !ok {
			s.mu.Unlock()
			http.Error(w, "deal not found", http.StatusNotFound)
			return
		}
		// Reassemble chunks in order.
		chunks := s.chunks[id]
		max := s.chunkMax[id]
		out := []byte{}
		for i := 0; i <= max; i++ {
			out = append(out, chunks[i]...)
		}
		s.uploads[id] = out
		d.uploaded = true
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case strings.Count(rest, "/") == 1:
		// PUT chunk
		parts := strings.SplitN(rest, "/", 2)
		id, chunkStr := parts[0], parts[1]
		var n int
		if _, err := fmt.Sscanf(chunkStr, "%d", &n); err != nil {
			http.Error(w, "bad chunk number", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 256<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if _, ok := s.deals[id]; !ok {
			s.mu.Unlock()
			http.Error(w, "deal not found", http.StatusNotFound)
			return
		}
		if s.chunks[id] == nil {
			s.chunks[id] = map[int][]byte{}
		}
		s.chunks[id][n] = body
		if n > s.chunkMax[id] {
			s.chunkMax[id] = n
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		// POST start: just accept it.
		w.WriteHeader(http.StatusOK)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
