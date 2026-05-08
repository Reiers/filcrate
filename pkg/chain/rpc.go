// Package chain is a thin JSON-RPC client for the Filecoin chain. It does
// not depend on Lotus's full API surface; it only knows the handful of
// methods filcrate needs (`StateMinerInfo`, `ChainHead`, etc.) and uses
// minimal types decoded from the wire.
//
// Default endpoints:
//
//	mainnet:     https://api.node.glif.io/rpc/v1
//	calibration: https://api.calibration.node.glif.io/rpc/v1
//
// You can point this at any Lotus-compatible v1 RPC endpoint, including a
// local node.
package chain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/multiformats/go-multiaddr"
)

// Network identifies which Filecoin network the client targets.
type Network string

const (
	NetworkMainnet     Network = "mainnet"
	NetworkCalibration Network = "calibration"
)

// DefaultEndpoint returns the public Glif RPC URL for n.
func DefaultEndpoint(n Network) string {
	switch n {
	case NetworkMainnet:
		return "https://api.node.glif.io/rpc/v1"
	case NetworkCalibration:
		return "https://api.calibration.node.glif.io/rpc/v1"
	default:
		return ""
	}
}

// Client is a Filecoin v1 JSON-RPC client.
type Client struct {
	endpoint string
	http     *http.Client
	id       atomic.Int64
}

// New returns a new Client. endpoint may be empty to use the public Glif
// endpoint for the supplied network.
func New(network Network, endpoint string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint(network)
	}
	return &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// MinerInfo is the subset of the Lotus `MinerInfo` response we read. The
// full struct has many more fields; we only decode what filcrate uses.
type MinerInfo struct {
	Owner       string   `json:"Owner"`
	Worker      string   `json:"Worker"`
	NewWorker   string   `json:"NewWorker"`
	PeerID      string   `json:"PeerId"`
	Multiaddrs  [][]byte `json:"-"`
	RawMaddrs   []string `json:"Multiaddrs"`
	SectorSize  uint64   `json:"SectorSize"`
	WindowPoStPartitionSectors uint64 `json:"WindowPoStPartitionSectors"`
}

// ParsedMultiaddrs returns the on-chain multiaddrs as parsed multiaddr
// values. Filecoin returns them as base64-encoded raw multiaddr bytes.
func (m *MinerInfo) ParsedMultiaddrs() ([]multiaddr.Multiaddr, error) {
	out := make([]multiaddr.Multiaddr, 0, len(m.RawMaddrs))
	for _, raw := range m.RawMaddrs {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("decoding multiaddr base64: %w", err)
		}
		ma, err := multiaddr.NewMultiaddrBytes(decoded)
		if err != nil {
			return nil, fmt.Errorf("parsing multiaddr bytes: %w", err)
		}
		out = append(out, ma)
	}
	return out, nil
}

// StateMinerInfo calls `Filecoin.StateMinerInfo` for the given miner address
// at chain head (`tsk` is sent as null).
func (c *Client) StateMinerInfo(ctx context.Context, miner string) (*MinerInfo, error) {
	var out MinerInfo
	if err := c.call(ctx, "Filecoin.StateMinerInfo", []any{miner, nil}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// rpcRequest is the minimal JSON-RPC request shape.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int64  `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.id.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: id})
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("reading rpc response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rpc http %d: %s", resp.StatusCode, string(respBody))
	}

	var r rpcResponse
	if err := json.Unmarshal(respBody, &r); err != nil {
		return fmt.Errorf("decoding rpc response: %w", err)
	}
	if r.Error != nil {
		return r.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}
