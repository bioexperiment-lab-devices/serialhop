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
