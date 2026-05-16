package updater

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

const sampleReleaseJSON = `{
  "tag_name": "v0.7.0",
  "html_url": "https://github.com/bioexperiment-lab-devices/serialhop/releases/tag/v0.7.0",
  "assets": [
    {"name": "SerialHop-v0.7.0.exe",   "browser_download_url": "https://example.com/serialhop.exe", "size": 41943040},
    {"name": "SHA256SUMS.txt",          "browser_download_url": "https://example.com/sums.txt",      "size": 128}
  ]
}`

func TestLatestRelease_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "SerialHop/") {
			t.Errorf("User-Agent: got %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept: got %q, want application/vnd.github+json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleReleaseJSON))
	}))
	defer srv.Close()

	rel, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/0.6.1 (test)")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.TagName != "v0.7.0" {
		t.Errorf("TagName: got %q", rel.TagName)
	}
	if rel.HTMLURL == "" {
		t.Errorf("HTMLURL: empty")
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("Assets: got %d, want 2", len(rel.Assets))
	}
	exe := rel.AssetByName("SerialHop-v0.7.0.exe")
	if exe == nil {
		t.Fatal("AssetByName returned nil for the exe")
	}
	if exe.BrowserDownloadURL == "" {
		t.Errorf("BrowserDownloadURL: empty")
	}
}

func TestLatestRelease_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer srv.Close()

	_, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/0.6.1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("err should mention 403: %v", err)
	}
}

func TestLatestRelease_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{ not json"))
	}))
	defer srv.Close()

	_, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/0.6.1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAssetByName_NotFound(t *testing.T) {
	rel := Release{Assets: []Asset{{Name: "other.exe"}}}
	if rel.AssetByName("missing.exe") != nil {
		t.Error("expected nil for missing asset")
	}
}

func TestLatestRelease_LogsInfoOnSuccess(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleReleaseJSON))
	}))
	defer srv.Close()

	if _, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/test"); err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}

	rec.AssertRecord(t, slog.LevelInfo, "updater release fetch start", map[string]any{"url": srv.URL})
	rec.AssertRecord(t, slog.LevelInfo, "updater release fetch ok", map[string]any{"url": srv.URL, "tag": "v0.7.0"})
}

func TestLatestRelease_LogsErrorOnHTTPFailure(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, _ = LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/test")

	rec.AssertRecord(t, slog.LevelError, "updater release fetch failed", map[string]any{"url": srv.URL})
}
