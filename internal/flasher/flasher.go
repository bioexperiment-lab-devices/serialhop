package flasher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
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
//
// SkipBackup, when true, omits the pre-flash flash-memory backup. The device
// is flashed without first reading its current image. The trade-off: ~8 s
// faster on a 32 KB sketch, but any post-erase failure becomes
// failed_no_recovery — there is no image to roll back to. Use only when the
// caller does not need post-failure recovery (e.g. blank/new boards, or when
// the operator already has a known-good image archived elsewhere).
type Request struct {
	Firmware         []byte
	TestCommand      []byte
	ExpectedResponse []byte
	Timeout          time.Duration
	InterByte        time.Duration
	PostOpenSettle   time.Duration
	SkipBackup       bool
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

// Flash runs the full state machine. Returns (nil, ErrBusy) if another
// Flash is in flight; otherwise returns a populated *Result (and nil
// error) describing every stage that ran.
func (f *flasherImpl) Flash(ctx context.Context, port string, req Request) (*Result, error) {
	if !f.mu.TryLock() {
		return nil, ErrBusy
	}
	defer f.mu.Unlock()

	s := &runState{
		port: port,
		req:  req,
		res: &Result{
			Port:   port,
			Stages: map[string]StageResult{},
		},
	}

	if !runPreflight(s) {
		logFlashSummary(s, port)
		return s.res, nil
	}

	// Open port at bootloader baud, pulse DTR, sync.
	p, err := f.opener.OpenWithBaud(port, avr.BootloaderBaud)
	if err != nil {
		s.res.Stages["backup"] = StageResult{Status: "failed", Error: "open: " + err.Error()}
		s.skipDownstream("erase", "program", "verify", "test", "rollback")
		s.res.Outcome = OutcomeFailedBackup
		logFlashSummary(s, port)
		return s.res, nil
	}
	defer func() { _ = p.Close() }()

	_ = p.SetDTR(false)
	time.Sleep(50 * time.Millisecond)
	_ = p.SetDTR(true)
	time.Sleep(50 * time.Millisecond)

	c := newSTKClient(p)
	if err := c.Sync(bootloaderSyncRetries * syncAttemptGap); err != nil {
		s.res.Stages["backup"] = StageResult{Status: "failed", Error: "sync: " + err.Error()}
		s.skipDownstream("erase", "program", "verify", "test", "rollback")
		s.res.Outcome = OutcomeFailedBackup
		logFlashSummary(s, port)
		return s.res, nil
	}
	// STK500v1 requires ENTER_PROGMODE before any LoadAddress / ProgPage /
	// ReadPage. Optiboot replies INSYNC+OK as a no-op, but skipping it is
	// non-spec — see bogdan-firmware/docs/firmware-backup-and-flash.md §11.3.
	if err := c.EnterProgMode(req.Timeout); err != nil {
		s.res.Stages["backup"] = StageResult{Status: "failed", Error: "enter_progmode: " + err.Error()}
		s.skipDownstream("erase", "program", "verify", "test", "rollback")
		s.res.Outcome = OutcomeFailedBackup
		logFlashSummary(s, port)
		return s.res, nil
	}

	if req.SkipBackup {
		s.res.Stages["backup"] = StageResult{Status: "skipped"}
		slog.Info("flash_stage", "port", port, "stage", "backup", "status", "skipped")
	} else {
		if !runBackup(s, c) {
			logFlashSummary(s, port)
			return s.res, nil
		}

		hexText := RenderIntelHex(s.backupBytes)
		s.res.BackupHex = hexText
		info, saveErr := SaveBackup(f.backupDir, port, hexText)
		if saveErr != nil {
			st := s.res.Stages["backup"]
			st.Status = "failed"
			st.Error = "save: " + saveErr.Error()
			s.res.Stages["backup"] = st
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			s.res.Outcome = OutcomeFailedBackup
			logFlashSummary(s, port)
			return s.res, nil
		}
		s.res.Backup = info
	}

	// handleStageFailure dispatches a failure into either rollback (if a
	// backup is on hand) or directly into failed_no_recovery (if SkipBackup
	// was requested and there is no backup to restore from).
	handleStageFailure := func(stageName string) (*Result, error) {
		if s.backupBytes == nil {
			s.res.Stages["rollback"] = StageResult{Status: "skipped"}
			s.res.Outcome = OutcomeFailedNoRecovery
			s.res.RecoveryHint = fmt.Sprintf(
				"%s failed and skip_backup was requested, so no rollback was attempted. "+
					"The device may need ISP-level recovery (e.g. AVRISP mkII).",
				stageName)
			slog.Info("flash_stage", "port", port, "stage", "rollback", "status", "skipped")
			logFlashSummary(s, port)
			return s.res, nil
		}
		res, rollbackErr := runRollback(s, c, p)
		logFlashSummary(s, port)
		return res, rollbackErr
	}

	if !runErase(s, c) {
		return handleStageFailure("erase")
	}
	if !runProgram(s, c) {
		return handleStageFailure("program")
	}
	if !runVerify(s, c) {
		return handleStageFailure("verify")
	}
	if !runTest(s, c, p) {
		return handleStageFailure("test")
	}

	s.res.Stages["rollback"] = StageResult{Status: "n/a"}
	s.res.Outcome = OutcomeSuccess

	_ = PruneBackups(f.backupDir, port, f.keepN)
	logFlashSummary(s, port)
	return s.res, nil
}

func logFlashSummary(s *runState, port string) {
	fwSum := sha256.Sum256(s.req.Firmware)
	totalMs := int64(0)
	for _, st := range s.res.Stages {
		totalMs += st.Duration.Milliseconds()
	}
	slog.Info("flash_summary",
		"port", port,
		"outcome", s.res.Outcome.String(),
		"firmware_sha256", hex.EncodeToString(fwSum[:]),
		"backup_sha256", s.res.Backup.SHA256,
		"total_duration_ms", totalMs,
	)
}
