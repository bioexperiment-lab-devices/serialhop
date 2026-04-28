package logship

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strconv"
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
