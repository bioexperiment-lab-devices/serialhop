package logship

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
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

func TestShipperHappyPath(t *testing.T) {
	var (
		mu        sync.Mutex
		requests  [][]byte
		gotHeader = make(map[string]string)
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, body)
		gotHeader["Content-Type"] = r.Header.Get("Content-Type")
		gotHeader["Content-Encoding"] = r.Header.Get("Content-Encoding")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	q := newQueue(1024)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
		"stderr": {"client": "lab-1", "stream": "stderr", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, realClock{})

	for i := 0; i < 600; i++ {
		stream := "stdout"
		if i%5 == 0 {
			stream = "stderr"
		}
		q.push(record{stream: stream, tsNano: int64(i), line: "line"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()

	// Wait for at least one request to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(requests)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("no POST received")
	}
	if gotHeader["Content-Encoding"] != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", gotHeader["Content-Encoding"])
	}
	if gotHeader["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotHeader["Content-Type"])
	}
}
