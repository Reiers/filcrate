package mk20

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/oklog/ulid/v2"
)

// ChunkedUploadOpts configures the chunked upload flow.
//
// ChunkSize defaults to 16 MiB if zero. Each chunk is PUT to a separate
// endpoint, indexed sequentially starting at 0.
//
// Concurrency caps in-flight chunk PUTs. Default 4. Increase for high-RTT
// links to a fast SP, decrease if the SP rate-limits.
type ChunkedUploadOpts struct {
	ChunkSize   int64
	Concurrency int
}

// UploadChunked streams body in fixed-size chunks to the SP, then finalizes
// the upload.
//
// Flow (mirrors the SP-side intake described in
// curio/market/mk20/DEVELOPER_FLOW_MAP.md):
//
//	1. POST /market/mk20/uploads/{id}            (start)
//	2. PUT  /market/mk20/uploads/{id}/{n}        (one per chunk)
//	3. POST /market/mk20/uploads/finalize/{id}   (finalize)
//
// The deal must already have been accepted via SubmitDeal with a
// `source_http_put` data source. The chunk size is whatever the SP
// advertises as a per-chunk maximum; when in doubt 16 MiB is a safe
// default.
func (c *Client) UploadChunked(ctx context.Context, id ulid.ULID, body io.Reader, totalSize int64, opts *ChunkedUploadOpts) error {
	if id == (ulid.ULID{}) {
		return errors.New("deal identifier is required")
	}
	if body == nil {
		return errors.New("upload body is nil")
	}
	_ = totalSize // reserved for future progress reporting

	chunkSize := int64(16 << 20)
	concurrency := 4
	if opts != nil {
		if opts.ChunkSize > 0 {
			chunkSize = opts.ChunkSize
		}
		if opts.Concurrency > 0 {
			concurrency = opts.Concurrency
		}
	}

	// 1. Start the upload.
	if err := c.doJSON(ctx, http.MethodPost, "/uploads/"+id.String(), nil, nil); err != nil {
		return fmt.Errorf("upload start: %w", err)
	}

	// 2. Stream chunks. The reader produces chunks sequentially (input is
	// not random-access); workers PUT them in parallel. Memory usage is
	// bounded by `concurrency * chunkSize`.
	type chunkJob struct {
		idx  int
		body []byte
	}
	jobs := make(chan chunkJob)

	// Cancel-on-error: a single cancel propagates to all workers and the
	// reader goroutine.
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		firstErr   error
		firstErrMu sync.Mutex
	)
	recordErr := func(err error) {
		firstErrMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		firstErrMu.Unlock()
	}

	var workers sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-uploadCtx.Done():
					return
				case j, ok := <-jobs:
					if !ok {
						return
					}
					err := c.doUploadPUT(uploadCtx,
						"/uploads/"+id.String()+"/"+itoa(j.idx),
						newReader(j.body), int64(len(j.body)))
					if err != nil {
						recordErr(fmt.Errorf("chunk %d PUT: %w", j.idx, err))
						return
					}
				}
			}
		}()
	}

	// Reader goroutine fills jobs sequentially.
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		defer close(jobs)

		idx := 0
		buf := make([]byte, chunkSize)
		for {
			n, err := io.ReadFull(body, buf)
			if n > 0 {
				bodyCopy := append([]byte(nil), buf[:n]...)
				select {
				case jobs <- chunkJob{idx: idx, body: bodyCopy}:
					idx++
				case <-uploadCtx.Done():
					return
				}
			}
			switch {
			case err == nil:
				continue
			case err == io.EOF || err == io.ErrUnexpectedEOF:
				return
			default:
				recordErr(fmt.Errorf("read input: %w", err))
				return
			}
		}
	}()

	reader.Wait()
	workers.Wait()

	if firstErr != nil {
		return firstErr
	}

	// 3. Finalize.
	if err := c.doJSON(ctx, http.MethodPost, "/uploads/finalize/"+id.String(), nil, nil); err != nil {
		return fmt.Errorf("upload finalize: %w", err)
	}
	return nil
}

// itoa is a tiny non-fmt int formatter used in URL path construction.
// Avoids pulling in strconv just for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
