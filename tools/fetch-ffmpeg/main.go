// Command fetch-ffmpeg downloads the pinned gyan.dev essentials ffmpeg
// build, verifies the archive's SHA-256, extracts ffmpeg.exe, verifies
// the binary's SHA-256, and writes it to the output path.
//
// Pins live in internal/streamer/ffmpeg_build.go. The CI release-build
// job runs `task fetch-ffmpeg` before `task installer` so the embedded
// payload is populated. Local developers building the installer can run
// the same task; it's a no-op if the output already exists with the
// right SHA-256.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

var (
	flagOut    = flag.String("out", "tools/installer/payload/ffmpeg.exe", "output path for the extracted ffmpeg.exe")
	flagForce  = flag.Bool("force", false, "re-download even if the output file is already valid")
	flagSilent = flag.Bool("silent", false, "suppress progress logging")
)

func main() {
	flag.Parse()
	if err := run(*flagOut, *flagForce, *flagSilent); err != nil {
		fmt.Fprintln(os.Stderr, "fetch-ffmpeg:", err)
		os.Exit(1)
	}
}

func run(outPath string, force, silent bool) error {
	logf := func(format string, args ...any) {
		if silent {
			return
		}
		fmt.Printf("fetch-ffmpeg: "+format+"\n", args...)
	}

	if !force {
		if ok, err := outputAlreadyValid(outPath); err == nil && ok {
			logf("output %s already has the pinned SHA-256, skipping download", outPath)
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	logf("downloading %s", streamer.PinnedFFmpegArchiveURL)
	archive, err := download(streamer.PinnedFFmpegArchiveURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	logf("archive size: %.1f MiB", float64(len(archive))/(1<<20))

	gotArchive := hex.EncodeToString(sha256OfBytes(archive))
	if gotArchive != streamer.PinnedFFmpegArchiveSHA256 {
		return fmt.Errorf("archive SHA-256 mismatch:\n  got:  %s\n  want: %s",
			gotArchive, streamer.PinnedFFmpegArchiveSHA256)
	}
	logf("archive SHA-256 verified")

	bin, err := extractMember(archive, streamer.PinnedFFmpegArchiveMember)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	logf("extracted %s (%.1f MiB)", streamer.PinnedFFmpegArchiveMember, float64(len(bin))/(1<<20))

	gotBin := hex.EncodeToString(sha256OfBytes(bin))
	if gotBin != streamer.PinnedFFmpegBinarySHA256 {
		return fmt.Errorf("ffmpeg.exe SHA-256 mismatch:\n  got:  %s\n  want: %s",
			gotBin, streamer.PinnedFFmpegBinarySHA256)
	}
	logf("ffmpeg.exe SHA-256 verified")

	if err := writeAtomic(outPath, bin); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	logf("wrote %s", outPath)
	return nil
}

func outputAlreadyValid(path string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the -out flag, supplied by the build pipeline
	if err != nil {
		return false, err
	}
	return hex.EncodeToString(sha256OfBytes(data)) == streamer.PinnedFFmpegBinarySHA256, nil
}

func download(url string) ([]byte, error) {
	hc := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	// Cap at 200 MiB defensively; gyan.dev essentials is ~88 MiB.
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func extractMember(archive []byte, member string) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		if f.Name == member {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = rc.Close() }()
			// Cap at 200 MiB defensively.
			return io.ReadAll(io.LimitReader(rc, 200<<20))
		}
	}
	return nil, errors.New("archive does not contain " + member)
}

func sha256OfBytes(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	// 0o600 is sufficient for an embed payload: the installer reads it
	// during `go build -tags production` and the bit doesn't transfer to
	// the eventual Windows binary anyway.
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
