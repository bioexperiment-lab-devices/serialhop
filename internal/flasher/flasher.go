package flasher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// ErrBusy is returned by Flash when another Flash is already in flight.
var ErrBusy = errors.New("flasher: another flash is in flight")

// Flasher is the public interface used by internal/api. Concrete impl is
// returned by New; tests pass a stub.
type Flasher interface {
	Flash(ctx context.Context, port string, req Request) (*Result, error)
}

// Request is the input to Flash. Firmware is the parsed flash image (parsed
// from Intel HEX by the API layer before invoking Flash). An empty TestCommand
// means "skip the test phase".
type Request struct {
	Firmware         []byte
	TestCommand      []byte
	ExpectedResponse []byte
	Timeout          time.Duration
	InterByte        time.Duration
	PostOpenSettle   time.Duration
}

// Outcome is one of the six terminal states described in the spec.
type Outcome int

const (
	OutcomeSuccess Outcome = iota
	OutcomeRolledBackVerifyFailed
	OutcomeRolledBackTestFailed
	OutcomeFailedPreflight
	OutcomeFailedBackup
	OutcomeFailedNoRecovery
)

// String returns the JSON wire form.
func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeRolledBackVerifyFailed:
		return "rolled_back_verify_failed"
	case OutcomeRolledBackTestFailed:
		return "rolled_back_test_failed"
	case OutcomeFailedPreflight:
		return "failed_preflight"
	case OutcomeFailedBackup:
		return "failed_backup"
	case OutcomeFailedNoRecovery:
		return "failed_no_recovery"
	}
	return "unknown"
}

// StageResult is the per-stage record carried in Result.Stages.
type StageResult struct {
	Status              string // "ok" | "failed" | "skipped" | "n/a"
	Duration            time.Duration
	Error               string // non-empty when Status == "failed"
	FirstMismatchOffset *int   // verify only
	VerifyStatus        string // rollback only: "ok" | "failed"
}

// TestResult describes the result of the post-flash test phase.
type TestResult struct {
	Sent     []byte
	Expected []byte
	Received []byte
	Match    bool
}

// Result is the output of Flash.
type Result struct {
	Outcome      Outcome
	Port         string
	Stages       map[string]StageResult
	Backup       BackupInfo
	BackupHex    string
	TestResult   *TestResult
	RecoveryHint string
}

// flasherImpl is the production implementation of Flasher.
type flasherImpl struct {
	opener          labserial.Opener
	backupDir       string
	keepN           int
	settleAfterOpen time.Duration

	mu sync.Mutex // serializes Flash invocations (single-flight)
}

// New constructs a Flasher. backupDir must be a non-empty absolute path; the
// directory is created on demand by SaveBackup. settleAfterOpen is the default
// sleep between reopening the port at 9600 and sending the operator's test
// command (matches discovery.PostOpenSettle).
func New(opener labserial.Opener, backupDir string, keepN int, settleAfterOpen time.Duration) (Flasher, error) {
	if backupDir == "" {
		return nil, fmt.Errorf("flasher: backupDir must be non-empty")
	}
	if keepN < 0 {
		return nil, fmt.Errorf("flasher: keepN must be >= 0 (got %d)", keepN)
	}
	return &flasherImpl{
		opener:          opener,
		backupDir:       backupDir,
		keepN:           keepN,
		settleAfterOpen: settleAfterOpen,
	}, nil
}

// Flash is a stub implementation that will be replaced in Task 13.
func (f *flasherImpl) Flash(ctx context.Context, port string, req Request) (*Result, error) {
	return nil, errors.New("not implemented")
}
