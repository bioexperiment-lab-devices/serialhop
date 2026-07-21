package remoteupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

// fakeGitHub serves /latest and /tags/<t> plus asset + sums files.
func fakeGitHub(t *testing.T, version, exeBody string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(exeBody))
	asset := "SerialHop-v" + version + ".exe"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	mux := http.NewServeMux()
	var base string
	relJSON := func() string {
		return fmt.Sprintf(`{"tag_name":"v%s","html_url":"h","assets":[
			{"name":%q,"browser_download_url":%q,"size":%d},
			{"name":"SHA256SUMS.txt","browser_download_url":%q,"size":%d}]}`,
			version, asset, base+"/dl/"+asset, len(exeBody), base+"/dl/SHA256SUMS.txt", len(sums))
	}
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, relJSON()) })
	mux.HandleFunc("/tags/", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, relJSON()) })
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, exeBody) })
	mux.HandleFunc("/dl/SHA256SUMS.txt", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, sums) })
	// TLS so custom-URL mode (which requires https://) can be exercised against
	// the fake; gh.Client() trusts the test server's cert.
	srv := httptest.NewTLSServer(mux)
	base = srv.URL
	return srv
}

type spySpawn struct {
	mu   sync.Mutex
	args []string
}

func (s *spySpawn) fn(_ string, a []string) error {
	s.mu.Lock()
	s.args = a
	s.mu.Unlock()
	return nil
}

func triggerManager(t *testing.T, gh *httptest.Server, spawn func(string, []string) error) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	rp := filepath.Join(dir, "update_result.json")
	return New(Config{
		Enabled:       true,
		HTTPClient:    gh.Client(),
		StagingDir:    dir,
		ResultPath:    rp,
		CurVersion:    "2.2.0",
		ExePath:       filepath.Join(dir, "SerialHop.exe"),
		ReleasesURL:   gh.URL + "/latest",
		TagURL:        func(tag string) string { return gh.URL + "/tags/" + tag },
		Spawn:         spawn,
		RunBackground: func(f func()) { f() }, // synchronous
	}), rp
}

func TestTriggerDisabled(t *testing.T) {
	m, _ := testManager(t, false)
	_, err := m.Trigger(context.Background(), Request{})
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
}

func TestTriggerLatestSpawnsChild(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "new-binary-bytes")
	defer gh.Close()
	spy := &spySpawn{}
	m, _ := triggerManager(t, gh, spy.fn)

	acc, err := m.Trigger(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if acc.To != "2.3.0" || acc.Noop {
		t.Errorf("acc = %+v, want To=2.3.0 Noop=false", acc)
	}
	if got := m.Status(); got.State != updateresult.StateInstalling {
		t.Errorf("post-job State = %q, want installing", got.State)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	assertArg(t, spy.args, "--update-src", "SerialHop-v2.3.0.exe")
	assertArg(t, spy.args, "--update-to", "2.3.0")
	assertArg(t, spy.args, "--update-from", "2.2.0")
	assertHasArg(t, spy.args, "--admin-action=update")
}

func TestTriggerTagMode(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "bytes")
	defer gh.Close()
	spy := &spySpawn{}
	m, _ := triggerManager(t, gh, spy.fn)
	acc, err := m.Trigger(context.Background(), Request{Version: "v2.3.0"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if acc.To != "2.3.0" {
		t.Errorf("acc.To = %q, want 2.3.0", acc.To)
	}
}

func TestTriggerNoopWhenSameVersion(t *testing.T) {
	gh := fakeGitHub(t, "2.2.0", "x") // == CurVersion
	defer gh.Close()
	spy := &spySpawn{}
	m, _ := triggerManager(t, gh, spy.fn)
	acc, err := m.Trigger(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !acc.Noop {
		t.Error("expected Noop for same version")
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.args != nil {
		t.Error("no child should be spawned for noop")
	}
}

func TestTriggerCustomURLSpawns(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "good")
	defer gh.Close()
	spy := &spySpawn{}
	m, _ := triggerManager(t, gh, spy.fn)
	sum := sha256.Sum256([]byte("good"))
	acc, err := m.Trigger(context.Background(), Request{
		URL: gh.URL + "/dl/SerialHop-v2.3.0.exe", SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("Trigger (custom): %v", err)
	}
	if acc.To != "2.3.0" {
		t.Errorf("acc.To = %q, want 2.3.0 (parsed from basename)", acc.To)
	}
	if got := m.Status(); got.State != updateresult.StateInstalling {
		t.Errorf("State = %q, want installing", got.State)
	}
}

func TestTriggerChecksumMismatchFails(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "good")
	defer gh.Close()
	m, _ := triggerManager(t, gh, func(string, []string) error { t.Fatal("must not spawn"); return nil })
	_, err := m.Trigger(context.Background(), Request{
		URL: gh.URL + "/dl/SerialHop-v2.3.0.exe", SHA256: hex.EncodeToString(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("Trigger (custom) should be accepted then fail in job: %v", err)
	}
	if got := m.Status(); got.State != updateresult.StateFailed {
		t.Errorf("State = %q, want failed", got.State)
	}
}

func TestTriggerDownloadFailureFails(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "good")
	defer gh.Close()
	m, _ := triggerManager(t, gh, func(string, []string) error { t.Fatal("must not spawn"); return nil })
	_, err := m.Trigger(context.Background(), Request{
		URL: gh.URL + "/dl/does-not-exist-SerialHop-v2.3.0.exe", SHA256: hex.EncodeToString(make([]byte, 32)),
	})
	// basename doesn't match SerialHop-v*.exe -> BadRequest (no version)
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Errorf("err = %v, want BadRequestError for underivable version", err)
	}
}

func TestTriggerRejectsHTTPURL(t *testing.T) {
	m, _ := testManager(t, true)
	_, err := m.Trigger(context.Background(), Request{URL: "http://x/SerialHop-v2.3.0.exe", SHA256: strings.Repeat("a", 64)})
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Errorf("err = %v, want BadRequestError", err)
	}
}

func TestTriggerCustomURLNeedsSHA(t *testing.T) {
	m, _ := testManager(t, true)
	_, err := m.Trigger(context.Background(), Request{URL: "https://x/SerialHop-v2.3.0.exe"})
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Errorf("err = %v, want BadRequestError (missing sha256)", err)
	}
}

func TestTriggerInProgress(t *testing.T) {
	m, _ := testManager(t, true)
	if !m.tryAcquire() {
		t.Fatal("acquire")
	}
	defer m.release()
	_, err := m.Trigger(context.Background(), Request{})
	if !errors.Is(err, ErrInProgress) {
		t.Errorf("err = %v, want ErrInProgress", err)
	}
}

func TestTriggerUpstreamLookupError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := New(Config{
		Enabled: true, HTTPClient: srv.Client(), StagingDir: dir,
		ResultPath: filepath.Join(dir, "r.json"), CurVersion: "2.2.0",
		ExePath: filepath.Join(dir, "SerialHop.exe"), ReleasesURL: srv.URL + "/latest",
		Spawn: func(string, []string) error { return nil }, RunBackground: func(f func()) { f() },
	})
	_, err := m.Trigger(context.Background(), Request{})
	var up *UpstreamError
	if !errors.As(err, &up) {
		t.Errorf("err = %v, want UpstreamError", err)
	}
	// guard must be released so a retry is possible
	if !m.tryAcquire() {
		t.Error("guard not released after upstream error")
	}
}

func assertArg(t *testing.T, args []string, flag, mustContain string) {
	t.Helper()
	for _, a := range args {
		if strings.HasPrefix(a, flag+"=") {
			if mustContain != "" && !strings.Contains(a, mustContain) {
				t.Errorf("%s = %q, want to contain %q", flag, a, mustContain)
			}
			return
		}
	}
	t.Errorf("missing arg %s in %v", flag, args)
}

func assertHasArg(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("missing arg %q in %v", want, args)
}
