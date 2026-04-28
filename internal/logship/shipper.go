package logship

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// pushStream is the on-the-wire shape of one stream entry in a Loki push.
type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type pushBody struct {
	Streams []pushStream `json:"streams"`
}

// buildPushBody groups batch by stream, attaches the cached labels, and
// returns a gzip-encoded JSON body suitable for POSTing to Loki.
//
// Returns (nil, nil) for an empty batch — callers must not POST in that
// case.
func buildPushBody(batch []record, labels map[string]map[string]string) ([]byte, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	groups := make(map[string][][2]string, 2)
	for _, r := range batch {
		groups[r.stream] = append(groups[r.stream], [2]string{
			strconv.FormatInt(r.tsNano, 10),
			r.line,
		})
	}

	body := pushBody{Streams: make([]pushStream, 0, len(groups))}
	for stream, values := range groups {
		lbl := labels[stream]
		if lbl == nil {
			return nil, fmt.Errorf("no labels cached for stream %q", stream)
		}
		body.Streams = append(body.Streams, pushStream{
			Stream: lbl,
			Values: values,
		})
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal push body: %w", err)
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		return nil, fmt.Errorf("gzip push body: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return buf.Bytes(), nil
}

const (
	maxBatch     = 500
	flushTimeout = 2 * time.Second
	httpTimeout  = 5 * time.Second
	backoffStart = 1 * time.Second
	backoffMax   = 10 * time.Second
)

type shipper struct {
	q      *queue
	url    string
	labels map[string]map[string]string
	clock  clock
	client *http.Client
}

func newShipper(q *queue, url string, labels map[string]map[string]string, clk clock) *shipper {
	return &shipper{
		q:      q,
		url:    url,
		labels: labels,
		clock:  clk,
		client: &http.Client{
			Timeout: httpTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        1,
				MaxIdleConnsPerHost: 1,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// run drains the queue forever — until ctx is done — and POSTs each
// batch with retry-on-5xx backoff and drop-on-4xx.
func (s *shipper) run(ctx context.Context) {
	for {
		s.q.waitNotify(ctx, flushTimeout)
		if ctx.Err() != nil {
			return
		}
		batch := s.q.drainUpTo(maxBatch)
		if len(batch) == 0 {
			continue
		}
		body, err := buildPushBody(batch, s.labels)
		if err != nil {
			slog.Warn("logship build body failed", "err", err)
			continue
		}
		s.postWithRetry(ctx, body)
	}
}

// postWithRetry holds a single batch, retrying on 5xx / transport
// errors with exponential backoff (1→2→5→10s, capped at 10s). 4xx drops
// the batch and returns. Returns when ctx is done or the batch is
// definitively handled.
func (s *shipper) postWithRetry(ctx context.Context, body []byte) {
	delay := backoffStart
	for {
		err := s.post(ctx, body)
		if err == nil {
			return
		}
		if hs, ok := err.(*httpStatusError); ok && hs.code/100 == 4 && hs.code != http.StatusTooManyRequests {
			slog.Warn("logship push rejected", "status", hs.code)
			return
		}
		// Retryable: 5xx, 429, transport errors.
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.clock.Sleep(delay)
		if delay < backoffMax {
			delay *= 2
			if delay > backoffMax {
				delay = backoffMax
			}
		}
	}
}

// post performs one POST; no retry. Returns nil on 2xx, an error otherwise.
func (s *shipper) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return &httpStatusError{code: resp.StatusCode}
	}
	return nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return http.StatusText(e.code) }
