package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// ProgressFunc reports bytes received and total bytes if known (0 if not).
// Called from the download goroutine; implementations must marshal back to
// any UI thread themselves.
type ProgressFunc func(received, total int64)

// Download streams `url` into destPath via a `<destPath>.partial` staging
// file. On success the file is fsynced and atomically renamed to destPath.
// On context cancel or any error the partial file is removed; destPath is
// never partially populated.
//
// The caller owns timeouts via ctx — pass a `context.WithTimeout(parent, 5*time.Minute)`
// for asset downloads.
func Download(ctx context.Context, hc *http.Client, url, destPath, userAgent string, progress ProgressFunc) error {
	req, err := newRequest(ctx, url, userAgent)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("get %s: HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	partial := destPath + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // partial is destPath+".partial"; destPath is caller-supplied intentionally
	if err != nil {
		return fmt.Errorf("create %s: %w", partial, err)
	}
	// Ensure the partial file is removed on any error path below.
	cleanup := func() { _ = os.Remove(partial) }
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	total := resp.ContentLength
	if err := streamWithProgress(f, resp.Body, total, progress); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync %s: %w", partial, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", partial, err)
	}
	if err := os.Rename(partial, destPath); err != nil {
		return fmt.Errorf("rename %s → %s: %w", partial, destPath, err)
	}
	cleanup = nil // success: keep destPath
	return nil
}

// streamWithProgress copies src → dst, invoking progress after every read
// (typically every ~32 KiB given the buffer size). Returns the first error
// from either side.
func streamWithProgress(dst io.Writer, src io.Reader, total int64, progress ProgressFunc) error {
	buf := make([]byte, 32*1024)
	var received int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			received += int64(n)
			if progress != nil {
				progress(received, total)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
}
