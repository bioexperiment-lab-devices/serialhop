package flasher

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// runState carries mutable state between stages of a single Flash run.
type runState struct {
	port        string
	req         Request
	res         *Result
	backupBytes []byte // raw flash image read in the backup stage
}

func (s *runState) recordStage(name, status, errMsg string, dur time.Duration) {
	st := StageResult{Status: status, Duration: dur, Error: errMsg}
	s.res.Stages[name] = st
}

func (s *runState) skipDownstream(stages ...string) {
	for _, name := range stages {
		if _, ok := s.res.Stages[name]; !ok {
			s.res.Stages[name] = StageResult{Status: "skipped"}
		}
	}
}

// runPreflight validates the request shape and returns true on success.
// On failure it populates res.Outcome and marks downstream stages skipped.
func runPreflight(s *runState) bool {
	start := time.Now()
	if len(s.req.Firmware) == 0 {
		s.recordStage("preflight", "failed", "firmware empty", time.Since(start))
		s.res.Outcome = OutcomeFailedPreflight
		s.skipDownstream("backup", "erase", "program", "verify", "test", "rollback")
		return false
	}
	maxSize := avr.FlashSize - avr.BootloaderSize
	if len(s.req.Firmware) > maxSize {
		s.recordStage("preflight", "failed",
			fmt.Sprintf("firmware %d bytes exceeds user space %d", len(s.req.Firmware), maxSize),
			time.Since(start))
		s.res.Outcome = OutcomeFailedPreflight
		s.skipDownstream("backup", "erase", "program", "verify", "test", "rollback")
		return false
	}
	if (len(s.req.TestCommand) == 0) != (len(s.req.ExpectedResponse) == 0) {
		s.recordStage("preflight", "failed", "test_command and expected_response must both be set or both omitted", time.Since(start))
		s.res.Outcome = OutcomeFailedPreflight
		s.skipDownstream("backup", "erase", "program", "verify", "test", "rollback")
		return false
	}
	s.recordStage("preflight", "ok", "", time.Since(start))
	return true
}

// runBackup opens the port at the bootloader baud, pulses DTR to enter
// optiboot, syncs, then page-reads the entire flash. The image bytes are
// stored on s.backupBytes for use by rollback. The image is rendered to
// Intel HEX, saved to disk, and the inline copy stored on s.res.BackupHex.
// On any failure, marks downstream stages as skipped and returns false.
func runBackup(s *runState, c *stkClient) bool {
	start := time.Now()
	img := make([]byte, avr.FlashSize)
	for off := 0; off < avr.FlashSize; off += avr.PageSize {
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			s.recordStage("backup", "failed", "load_address: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "backup", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			s.res.Outcome = OutcomeFailedBackup
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			return false
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			s.recordStage("backup", "failed", "read_page: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "backup", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			s.res.Outcome = OutcomeFailedBackup
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			return false
		}
		copy(img[off:], page)
	}
	s.backupBytes = img
	s.recordStage("backup", "ok", "", time.Since(start))
	slog.Info("flash_stage", "port", s.port, "stage", "backup", "status", "ok", "duration_ms", time.Since(start).Milliseconds())
	return true
}

// runErase is a no-op on optiboot. The 512 B optiboot bootloader does not
// implement chip erase (avrdude requires `-D` for this reason); flash pages
// are erased implicitly by ProgPage as they are written. The stage is
// retained in the public state machine to keep the response shape stable —
// see bogdan-firmware/docs/firmware-backup-and-flash.md §3 and §8.
func runErase(s *runState, c *stkClient) bool {
	s.recordStage("erase", "ok", "", 0)
	slog.Info("flash_stage", "port", s.port, "stage", "erase", "status", "ok", "duration_ms", 0)
	return true
}

// runProgram writes the request's firmware image one page at a time.
func runProgram(s *runState, c *stkClient) bool {
	start := time.Now()
	img := s.req.Firmware
	for off := 0; off < len(img); off += avr.PageSize {
		end := off + avr.PageSize
		if end > len(img) {
			end = len(img)
		}
		page := img[off:end]
		// Pad short final page to PageSize with 0xFF.
		if len(page) < avr.PageSize {
			padded := make([]byte, avr.PageSize)
			for i := range padded {
				padded[i] = 0xFF
			}
			copy(padded, page)
			page = padded
		}
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			s.recordStage("program", "failed", "load_address: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "program", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			return false
		}
		if err := c.ProgPage(s.req.Timeout, page); err != nil {
			s.recordStage("program", "failed", "prog_page: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "program", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			return false
		}
	}
	s.recordStage("program", "ok", "", time.Since(start))
	slog.Info("flash_stage", "port", s.port, "stage", "program", "status", "ok", "duration_ms", time.Since(start).Milliseconds())
	return true
}

// runVerify page-reads the programmed region and compares against the
// source image. Returns true on byte-exact match. On mismatch, populates
// the verify stage with FirstMismatchOffset and returns false.
func runVerify(s *runState, c *stkClient) bool {
	start := time.Now()
	img := s.req.Firmware
	readback := make([]byte, 0, len(img))
	for off := 0; off < len(img); off += avr.PageSize {
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			s.recordStage("verify", "failed", "load_address: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "verify", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			return false
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			s.recordStage("verify", "failed", "read_page: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "verify", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			return false
		}
		readback = append(readback, page...)
	}
	for i, b := range img {
		if readback[i] != b {
			off := i
			st := StageResult{
				Status:              "failed",
				Duration:            time.Since(start),
				Error:               fmt.Sprintf("mismatch at offset 0x%04X (got %02X, want %02X)", off, readback[i], b),
				FirstMismatchOffset: &off,
			}
			s.res.Stages["verify"] = st
			slog.Info("flash_stage", "port", s.port, "stage", "verify", "status", "failed", "duration_ms", time.Since(start).Milliseconds(), "first_mismatch_offset", fmt.Sprintf("0x%04X", off))
			return false
		}
	}
	s.recordStage("verify", "ok", "", time.Since(start))
	slog.Info("flash_stage", "port", s.port, "stage", "verify", "status", "ok", "duration_ms", time.Since(start).Milliseconds())
	return true
}

// runRollback re-flashes the device with the backup image read in stage 2
// and verifies the rollback by reading the flash back. On success returns
// (res, nil) with outcome rolled_back_verify_failed OR rolled_back_test_failed
// depending on which upstream stage triggered the rollback. On failure of any
// step inside rollback, outcome is failed_no_recovery and the backup file is
// locked (renamed with -LOCKED-).
func runRollback(s *runState, c *stkClient, p labserial.Port) (*Result, error) {
	start := time.Now()
	st := StageResult{Status: "ok", VerifyStatus: "ok"}

	trigger := "verify"
	for _, name := range []string{"erase", "program", "verify", "test"} {
		if r, ok := s.res.Stages[name]; ok && r.Status == "failed" {
			trigger = name
			break
		}
	}

	// If the test phase ran, the bootloader has exited and the user firmware
	// is running at TargetBaud. STK commands sent now would be eaten by the
	// sketch. To recover, pulse DTR to reset the chip back into the bootloader,
	// re-sync, and re-enter programming mode. For verify/program/erase
	// triggers, the bootloader is still alive (no LeaveProgMode was sent), so
	// just continue with the existing session — no DTR pulse needed.
	if trigger == "test" {
		if err := p.SetBaudRate(avr.BootloaderBaud); err != nil {
			return rollbackFailed(s, st, start, "set_baud: "+err.Error())
		}
		_ = p.SetDTR(false)
		time.Sleep(50 * time.Millisecond)
		_ = p.SetDTR(true)
		time.Sleep(50 * time.Millisecond)
		if err := c.Sync(bootloaderSyncRetries * syncAttemptGap); err != nil {
			return rollbackFailed(s, st, start, "sync: "+err.Error())
		}
		if err := c.EnterProgMode(s.req.Timeout); err != nil {
			return rollbackFailed(s, st, start, "enter_progmode: "+err.Error())
		}
	}

	// No chip-erase: optiboot does per-page erase during ProgPage; sending
	// STK_CHIP_ERASE here would either be a no-op or fail outright depending
	// on the optiboot variant. See firmware-backup-and-flash.md §3 + §8.
	prog := s.backupBytes
	for off := 0; off < len(prog); off += avr.PageSize {
		end := off + avr.PageSize
		if end > len(prog) {
			end = len(prog)
		}
		page := prog[off:end]
		if len(page) < avr.PageSize {
			padded := make([]byte, avr.PageSize)
			for i := range padded {
				padded[i] = 0xFF
			}
			copy(padded, page)
			page = padded
		}
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			return rollbackFailed(s, st, start, "load_address: "+err.Error())
		}
		if err := c.ProgPage(s.req.Timeout, page); err != nil {
			return rollbackFailed(s, st, start, "prog_page: "+err.Error())
		}
	}
	for off := 0; off < len(prog); off += avr.PageSize {
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			return rollbackFailed(s, st, start, "verify load_address: "+err.Error())
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			return rollbackFailed(s, st, start, "verify read_page: "+err.Error())
		}
		end := off + avr.PageSize
		if end > len(prog) {
			end = len(prog)
		}
		for i := off; i < end; i++ {
			if page[i-off] != prog[i] {
				st.VerifyStatus = "failed"
				return rollbackFailed(s, st, start,
					fmt.Sprintf("verify mismatch at 0x%04X (got %02X, want %02X)", i, page[i-off], prog[i]))
			}
		}
	}

	st.Duration = time.Since(start)
	s.res.Stages["rollback"] = st
	switch trigger {
	case "test":
		s.res.Outcome = OutcomeRolledBackTestFailed
	default:
		s.res.Outcome = OutcomeRolledBackVerifyFailed
	}
	slog.Info("flash_stage", "port", s.port, "stage", "rollback", "status", "ok", "duration_ms", time.Since(start).Milliseconds(), "verify_status", st.VerifyStatus)
	return s.res, nil
}

func rollbackFailed(s *runState, st StageResult, start time.Time, errMsg string) (*Result, error) {
	st.Status = "failed"
	st.Error = errMsg
	st.Duration = time.Since(start)
	s.res.Stages["rollback"] = st
	s.res.Outcome = OutcomeFailedNoRecovery
	if s.res.Backup.Path != "" {
		locked, err := LockBackup(s.res.Backup.Path)
		if err == nil {
			s.res.Backup.Path = locked
		}
		s.res.RecoveryHint = fmt.Sprintf(
			"Rollback failed: %s. The device may need ISP-level recovery (e.g. AVRISP mkII). The saved backup at %s is the last known good image.",
			errMsg, s.res.Backup.Path)
	} else {
		s.res.RecoveryHint = "Rollback failed: " + errMsg
	}
	return s.res, nil
}

// runTest exits programming mode, switches the open port to TargetBaud,
// waits PostOpenSettle, drains, sends TestCommand, and reads exactly
// len(ExpectedResponse) bytes. Compares exact-match. Returns true on match,
// false on any failure (read error, mismatch, length mismatch).
func runTest(s *runState, c *stkClient, p labserial.Port) bool {
	start := time.Now()
	if len(s.req.TestCommand) == 0 {
		s.res.Stages["test"] = StageResult{Status: "skipped"}
		return true
	}
	if err := c.LeaveProgMode(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "leave_progmode: "+err.Error(), time.Since(start))
		slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
		return false
	}
	if err := p.SetBaudRate(avr.TargetBaud); err != nil {
		s.recordStage("test", "failed", "set_baud: "+err.Error(), time.Since(start))
		slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
		return false
	}
	time.Sleep(s.req.PostOpenSettle)
	_ = p.Drain(50 * time.Millisecond)

	if _, err := p.Write(s.req.TestCommand); err != nil {
		s.recordStage("test", "failed", "write: "+err.Error(), time.Since(start))
		slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
		return false
	}

	expected := s.req.ExpectedResponse
	received := make([]byte, 0, len(expected))
	if err := p.SetReadTimeout(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "set_read_timeout: "+err.Error(), time.Since(start))
		slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
		return false
	}
	deadline := time.Now().Add(s.req.Timeout)
	buf := make([]byte, len(expected))
	for len(received) < len(expected) {
		n, err := p.Read(buf[:len(expected)-len(received)])
		if err != nil {
			s.res.TestResult = &TestResult{
				Sent: s.req.TestCommand, Expected: expected, Received: received, Match: false,
			}
			s.recordStage("test", "failed", "read: "+err.Error(), time.Since(start))
			slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
			return false
		}
		received = append(received, buf[:n]...)
		if time.Now().After(deadline) {
			break
		}
	}

	match := bytesEqual(received, expected)
	s.res.TestResult = &TestResult{
		Sent: s.req.TestCommand, Expected: expected, Received: received, Match: match,
	}
	if !match {
		s.recordStage("test", "failed",
			fmt.Sprintf("test response mismatch (got %d bytes, want %d)", len(received), len(expected)),
			time.Since(start))
		slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "failed", "duration_ms", time.Since(start).Milliseconds())
		return false
	}
	s.recordStage("test", "ok", "", time.Since(start))
	slog.Info("flash_stage", "port", s.port, "stage", "test", "status", "ok", "duration_ms", time.Since(start).Milliseconds())
	return true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
