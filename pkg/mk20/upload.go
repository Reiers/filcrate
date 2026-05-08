package mk20

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"time"

	"github.com/oklog/ulid/v2"
)

// UploadSerial PUTs an entire piece body in a single HTTP request, then
// finalizes the upload with a POST.
//
// Use this for pieces small enough to fit in one PUT request (a few hundred
// MiB at most for most SP configurations). For larger pieces use the
// chunked upload flow (`UploadChunked`, planned).
//
// Flow:
//
//	1. PUT  /market/mk20/upload/{id}   <body>
//	2. POST /market/mk20/upload/{id}   (finalize, body is the optional Deal
//	                                     update; we send empty body)
//
// The deal must have already been accepted via SubmitDeal with a Data
// source of `source_http_put`.
func (c *Client) UploadSerial(ctx context.Context, id ulid.ULID, body io.Reader, contentLength int64) error {
	if id == (ulid.ULID{}) {
		return errors.New("deal identifier is required")
	}
	if body == nil {
		return errors.New("upload body is nil")
	}

	if err := c.doUploadPUT(ctx, "/upload/"+id.String(), body, contentLength); err != nil {
		return fmt.Errorf("upload PUT: %w", err)
	}
	if err := c.doJSON(ctx, http.MethodPost, "/upload/"+id.String(), nil, nil); err != nil {
		return fmt.Errorf("upload finalize: %w", err)
	}
	return nil
}

// doUploadPUT is a streaming variant of doJSON: bypasses JSON encoding,
// honors Content-Length so the SP can pre-allocate, and signs the auth
// header just like every other request.
func (c *Client) doUploadPUT(ctx context.Context, p string, body io.Reader, contentLength int64) error {
	endpoint := c.baseURL + MarketPath + path.Clean("/"+p)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, body)
	if err != nil {
		return fmt.Errorf("building upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	if err := ApplyAuth(ctx, c.signer, req); err != nil {
		return fmt.Errorf("signing upload request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return &APIError{
			Status: resp.StatusCode,
			Body:   string(respBody),
			Method: http.MethodPut,
			URL:    endpoint,
		}
	}
	return nil
}

// PollStatus repeatedly queries `/market/mk20/status/{id}` until the deal
// transitions to a terminal state, ctx is cancelled, or `until` (relative
// timeout) elapses. The terminal-state check is intentionally generous —
// any state name containing one of the words below is treated as terminal.
//
// Callers that need fine-grained control should drive Status() in a loop
// themselves; this helper is for one-shot CLI flows.
func (c *Client) PollStatus(ctx context.Context, id ulid.ULID, until time.Duration, every time.Duration) (*DealStatus, error) {
	if every <= 0 {
		every = 5 * time.Second
	}
	deadline := time.Now().Add(until)
	if until <= 0 {
		deadline = time.Time{} // no relative cap
	}

	for {
		status, err := c.Status(ctx, id)
		if err != nil {
			return nil, err
		}
		if isTerminal(status.State) {
			return status, nil
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			return status, fmt.Errorf("polling timed out in state %q", status.State)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(every):
		}
	}
}

// isTerminal applies a tolerant heuristic over Curio's deal state names so
// we don't break on a state-name change between Curio versions.
func isTerminal(state string) bool {
	switch state {
	case "":
		return false
	case "complete", "completed", "finalized", "active",
		"failed", "rejected", "error", "expired":
		return true
	}
	// Heuristic fallback — if the SP starts emitting `*_failed` or
	// `*_complete` we still want to stop.
	for _, suffix := range []string{"_failed", "_complete", "_done", "_error"} {
		if len(state) >= len(suffix) && state[len(state)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}
