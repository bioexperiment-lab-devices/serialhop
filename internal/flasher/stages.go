package flasher

import (
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
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
			s.res.Outcome = OutcomeFailedBackup
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			return false
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			s.recordStage("backup", "failed", "read_page: "+err.Error(), time.Since(start))
			s.res.Outcome = OutcomeFailedBackup
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			return false
		}
		copy(img[off:], page)
	}
	s.backupBytes = img
	s.recordStage("backup", "ok", "", time.Since(start))
	return true
}

// runErase issues STK_CHIP_ERASE. On failure transitions into rollback.
func runErase(s *runState, c *stkClient) bool {
	start := time.Now()
	if err := c.ChipErase(s.req.Timeout); err != nil {
		s.recordStage("erase", "failed", err.Error(), time.Since(start))
		return false
	}
	s.recordStage("erase", "ok", "", time.Since(start))
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
			return false
		}
		if err := c.ProgPage(s.req.Timeout, page); err != nil {
			s.recordStage("program", "failed", "prog_page: "+err.Error(), time.Since(start))
			return false
		}
	}
	s.recordStage("program", "ok", "", time.Since(start))
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
			return false
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			s.recordStage("verify", "failed", "read_page: "+err.Error(), time.Since(start))
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
			return false
		}
	}
	s.recordStage("verify", "ok", "", time.Since(start))
	return true
}

// runRollback is replaced in Task 16. This stub keeps the package compiling.
func runRollback(s *runState, c *stkClient, p labserialPort) (*Result, error) {
	s.res.Stages["rollback"] = StageResult{Status: "failed", Error: "rollback not implemented yet"}
	s.res.Outcome = OutcomeFailedNoRecovery
	s.res.RecoveryHint = "rollback path not yet wired (Task 16)"
	return s.res, nil
}

// labserialPort is a local alias to avoid leaking the import in this signature.
// Replaced with labserial.Port directly in Task 16.
type labserialPort = interface {
	SetDTR(bool) error
	SetBaudRate(int) error
}

// runTest exits programming mode, switches the open port to TargetBaud,
// waits PostOpenSettle, drains, sends TestCommand, and reads exactly
// len(ExpectedResponse) bytes. Compares exact-match. Returns true on match,
// false on any failure (read error, mismatch, length mismatch).
func runTest(s *runState, c *stkClient, p labserialPort) bool {
	start := time.Now()
	if len(s.req.TestCommand) == 0 {
		s.res.Stages["test"] = StageResult{Status: "skipped"}
		return true
	}

	if err := c.LeaveProgMode(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "leave_progmode: "+err.Error(), time.Since(start))
		return false
	}
	if err := p.SetBaudRate(avr.TargetBaud); err != nil {
		s.recordStage("test", "failed", "set_baud: "+err.Error(), time.Since(start))
		return false
	}
	time.Sleep(s.req.PostOpenSettle)
	if drainer, ok := p.(interface {
		Drain(time.Duration) error
	}); ok {
		_ = drainer.Drain(50 * time.Millisecond)
	}

	rw, ok := p.(interface {
		Write([]byte) (int, error)
		Read([]byte) (int, error)
		SetReadTimeout(time.Duration) error
	})
	if !ok {
		s.recordStage("test", "failed", "port does not support write+read", time.Since(start))
		return false
	}
	if _, err := rw.Write(s.req.TestCommand); err != nil {
		s.recordStage("test", "failed", "write: "+err.Error(), time.Since(start))
		return false
	}

	expected := s.req.ExpectedResponse
	received := make([]byte, 0, len(expected))
	if err := rw.SetReadTimeout(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "set_read_timeout: "+err.Error(), time.Since(start))
		return false
	}
	deadline := time.Now().Add(s.req.Timeout)
	buf := make([]byte, len(expected))
	for len(received) < len(expected) {
		n, err := rw.Read(buf[:len(expected)-len(received)])
		if err != nil {
			s.res.TestResult = &TestResult{
				Sent: s.req.TestCommand, Expected: expected, Received: received, Match: false,
			}
			s.recordStage("test", "failed", "read: "+err.Error(), time.Since(start))
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
		return false
	}
	s.recordStage("test", "ok", "", time.Since(start))
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
