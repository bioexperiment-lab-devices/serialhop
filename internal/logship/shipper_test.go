package logship

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strconv"
	"testing"
)

func TestBuildPushBodyGroupsByStream(t *testing.T) {
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
		"stderr": {"client": "lab-1", "stream": "stderr", "service": "lab_devices_client", "version": "1.4.2"},
	}
	batch := []record{
		{stream: "stdout", tsNano: 100, line: `{"msg":"a"}`},
		{stream: "stderr", tsNano: 101, line: "panic line"},
		{stream: "stdout", tsNano: 102, line: `{"msg":"b"}`},
	}

	body, err := buildPushBody(batch, labels)
	if err != nil {
		t.Fatalf("buildPushBody: %v", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}

	var parsed struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if len(parsed.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(parsed.Streams))
	}

	byStream := map[string]int{}
	for _, s := range parsed.Streams {
		byStream[s.Stream["stream"]] = len(s.Values)
		if s.Stream["service"] != "lab_devices_client" {
			t.Errorf("service label = %q", s.Stream["service"])
		}
		if s.Stream["client"] != "lab-1" {
			t.Errorf("client label = %q", s.Stream["client"])
		}
		if s.Stream["version"] != "1.4.2" {
			t.Errorf("version label = %q", s.Stream["version"])
		}
		if len(s.Stream) != 4 {
			t.Errorf("expected exactly 4 labels, got %v", s.Stream)
		}
	}
	if byStream["stdout"] != 2 || byStream["stderr"] != 1 {
		t.Fatalf("stream counts: %+v (want stdout:2 stderr:1)", byStream)
	}

	// Spot-check a value pair: timestamp is a string of the unix-nano int.
	for _, s := range parsed.Streams {
		for _, v := range s.Values {
			if _, err := strconv.ParseInt(v[0], 10, 64); err != nil {
				t.Errorf("ts %q is not a valid int string: %v", v[0], err)
			}
			if v[1] == "" {
				t.Errorf("empty line in values")
			}
		}
	}
}

func TestBuildPushBodyEmptyBatch(t *testing.T) {
	body, err := buildPushBody(nil, nil)
	if err != nil {
		t.Fatalf("buildPushBody on empty batch: %v", err)
	}
	if body != nil {
		t.Fatalf("empty batch must return nil body, got %d bytes", len(body))
	}
}
