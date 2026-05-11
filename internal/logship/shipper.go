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
			// Final best-effort drain. Use a fresh background context
			// so postWithRetry isn't immediately short-circuited by the
			// already-cancelled ctx; the manager's caller-supplied
			// deadline bounds how long we wait via the select on done
			// in Manager.Shutdown.
			s.flushOnce(context.Background())
			return
		}
		s.flushOnce(ctx)
	}
}

func (s *shipper) flushOnce(ctx context.Context) {
	batch := s.q.drainUpTo(maxBatch)
	if len(batch) == 0 {
		return
	}
	body, err := buildPushBody(batch, s.labels)
	if err != nil {
		slog.Warn("logship build body failed", "err", err)
		return
	}
	if s.postWithRetry(ctx, body) {
		if dropped := s.q.takeDropped(); dropped > 0 {
			slog.Warn("logs dropped", "count", dropped)
		}
	}
}

// postWithRetry holds a single batch, retrying on 5xx / transport
// errors with exponential backoff (1→2→5→10s, capped at 10s). 4xx drops
// the batch and returns false. Returns true on success, false on
// ctx-cancellation or 4xx-drop.
//
// post() itself is intentionally not ctx-aware (see its godoc): a single
// in-flight HTTP call completes (bounded by http.Client.Timeout) even
// when ctx is cancelled, so records being POSTed at Shutdown time aren't
// silently dropped. ctx still bounds whether we retry after a failure.
func (s *shipper) postWithRetry(ctx context.Context, body []byte) bool {
	delay := backoffStart
	for {
		err := s.post(body)
		if err == nil {
			return true
		}
		if hs, ok := err.(*httpStatusError); ok && hs.code/100 == 4 && hs.code != http.StatusTooManyRequests {
			slog.Warn("logship push rejected", "status", hs.code)
			return false
		}
		// Retryable: 5xx, 429, transport errors.
		select {
		case <-ctx.Done():
			return false
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
//
// Does not take a context. The http.Client's own Timeout (httpTimeout)
// is the only bound on the request. This keeps an in-flight POST from
// being aborted mid-flight when the shipper's outer ctx is cancelled
// (e.g. during Manager.Shutdown) — otherwise records that were already
// drained from the queue would be dropped, breaking the "Shutdown
// drains pending records" contract.
func (s *shipper) post(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort drain; error irrelevant
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return &httpStatusError{code: resp.StatusCode}
	}
	return nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return http.StatusText(e.code) }
