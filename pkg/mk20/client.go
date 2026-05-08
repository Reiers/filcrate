package mk20

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/oklog/ulid/v2"
)

// MarketPath is the canonical Curio Mk20 base path on an SP HTTP host.
const MarketPath = "/market/mk20"

// Client is a minimal Mk20 client. It speaks plain HTTPS, signs the
// `Authorization` header per request, and exposes the small set of
// operations a real client needs.
//
// Capability probing (`Products`, `DataSources`, `Contracts`) is
// authenticated by Curio for security reasons but is otherwise read-only and
// safe.
type Client struct {
	baseURL string
	signer  Signer
	http    *http.Client
}

// NewClient returns a Client targetting the given SP base URL (e.g.
// `https://sp.example.com`), signing as `signer`. The base URL must NOT
// already include `/market/mk20`; the client appends that itself.
func NewClient(baseURL string, signer Signer) (*Client, error) {
	if signer == nil {
		return nil, errors.New("signer is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("base URL %q must include scheme and host", baseURL)
	}
	return &Client{
		baseURL: u.String(),
		signer:  signer,
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

// SetHTTPClient swaps the underlying http.Client (for tests or custom
// transports / timeouts).
func (c *Client) SetHTTPClient(h *http.Client) {
	if h != nil {
		c.http = h
	}
}

// Products queries `GET /market/mk20/products`. Returns the list of products
// the SP has enabled (e.g. `ddo_v1`, `pdp_v1`, `retrieval_v1`).
func (c *Client) Products(ctx context.Context) ([]string, error) {
	var out SupportedProducts
	if err := c.doJSON(ctx, http.MethodGet, "/products", nil, &out); err != nil {
		return nil, err
	}
	return out.Products, nil
}

// DataSources queries `GET /market/mk20/sources`.
func (c *Client) DataSources(ctx context.Context) ([]string, error) {
	var out SupportedDataSources
	if err := c.doJSON(ctx, http.MethodGet, "/sources", nil, &out); err != nil {
		return nil, err
	}
	return out.Sources, nil
}

// Contracts queries `GET /market/mk20/contracts`. Returns the EVM addresses
// of DDO contracts the SP has whitelisted as paid-deal authorities.
func (c *Client) Contracts(ctx context.Context) ([]string, error) {
	var out SupportedContracts
	if err := c.doJSON(ctx, http.MethodGet, "/contracts", nil, &out); err != nil {
		return nil, err
	}
	return out.Contracts, nil
}

// SubmitDeal POSTs a Deal envelope to `/market/mk20/deal`. Returns the deal
// identifier on success. Caller is expected to have populated `deal.Client`
// with their address string and `deal.Identifier` with a fresh ULID.
func (c *Client) SubmitDeal(ctx context.Context, deal *Deal) (ulid.ULID, error) {
	if deal == nil {
		return ulid.ULID{}, errors.New("deal is nil")
	}
	if deal.Identifier == (ulid.ULID{}) {
		return ulid.ULID{}, errors.New("deal.Identifier is required (generate with ulid.Make())")
	}
	if deal.Client == "" {
		return ulid.ULID{}, errors.New("deal.Client (signer address string) is required")
	}
	if err := c.doJSON(ctx, http.MethodPost, "/deal", deal, nil); err != nil {
		return ulid.ULID{}, err
	}
	return deal.Identifier, nil
}

// DealStatus is a minimal shape of the `/status/{id}` response. Curio's
// schema is richer; we expose just the fields a client needs to render a
// useful timeline. Unknown fields are tolerated.
type DealStatus struct {
	Identifier string         `json:"identifier"`
	State      string         `json:"state"`
	Error      string         `json:"error,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// Status queries `GET /market/mk20/status/{id}`.
func (c *Client) Status(ctx context.Context, id ulid.ULID) (*DealStatus, error) {
	out := &DealStatus{}
	if err := c.doJSON(ctx, http.MethodGet, "/status/"+id.String(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// doJSON is the workhorse: builds the URL, signs the auth header, encodes
// the request body if provided, decodes the response body if `out` is
// non-nil. Treats any non-2xx as an error and surfaces the body so the
// caller can map Curio DealCode values.
func (c *Client) doJSON(ctx context.Context, method, p string, body, out any) error {
	endpoint := c.baseURL + MarketPath + path.Clean("/"+p)

	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		bodyReader = newReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := ApplyAuth(ctx, c.signer, req); err != nil {
		return fmt.Errorf("signing request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Status: resp.StatusCode,
			Body:   string(respBody),
			Method: method,
			URL:    endpoint,
		}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response body: %w", err)
	}
	return nil
}

// APIError is returned for any non-2xx HTTP response from the SP. The Body
// is bounded to 8 MiB so a misbehaving SP cannot OOM the client.
type APIError struct {
	Status int
	Body   string
	Method string
	URL    string
}

func (e *APIError) Error() string {
	short := e.Body
	if len(short) > 256 {
		short = short[:256] + "..."
	}
	return fmt.Sprintf("mk20: %s %s -> HTTP %d: %s", e.Method, e.URL, e.Status, short)
}

// IsAuthError reports whether err is a 401/403 HTTP error from the SP.
func IsAuthError(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden
	}
	return false
}

// IsNotFound reports whether err is a 404 HTTP error from the SP.
func IsNotFound(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusNotFound
	}
	return false
}
