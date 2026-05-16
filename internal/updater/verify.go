package updater

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// VerifyFile computes the SHA-256 of filePath and compares it against the
// entry for `filename` in sumsBody. sumsBody is the body of a standard
// sha256sum-format file ("<lowercase-hex>  <filename>" per line).
//
// Returns an error if the file is missing, the filename isn't in sumsBody,
// or the hash differs.
func VerifyFile(filePath, sumsBody, filename string) error {
	slog.Info("updater verify start", "path", filePath)

	want, ok := lookupSum(sumsBody, filename)
	if !ok {
		return fmt.Errorf("verify: %q not found in checksum file", filename)
	}
	got, err := hashFile(filePath)
	if err != nil {
		return fmt.Errorf("verify: hash %s: %w", filePath, err)
	}
	if !strings.EqualFold(got, want) {
		slog.Warn("updater checksum mismatch", "path", filePath, "expected", want, "got", got)
		return fmt.Errorf("verify: SHA-256 mismatch for %s: expected %s, got %s", filename, want, got)
	}
	slog.Info("updater verify ok", "path", filePath)
	return nil
}

func lookupSum(body, filename string) (string, bool) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Standard format: "<hex><space><space><filename>". Some tools
		// emit a single space or a tab; accept any whitespace run between
		// the two fields.
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == filename {
			return fields[0], true
		}
	}
	return "", false
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is a local file we just downloaded; intentional
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
