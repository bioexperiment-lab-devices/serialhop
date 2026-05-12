package flasher

import (
	"errors"
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// STK500v1 wire opcodes (subset used by optiboot).
const (
	stkGetSync       byte = 0x30
	stkGetSignOn     byte = 0x31
	stkLoadAddress   byte = 0x55
	stkProgPage      byte = 0x64
	stkReadPage      byte = 0x74
	stkChipErase     byte = 0x52
	stkLeaveProgMode byte = 0x51

	stkCrcEop byte = 0x20
	stkInSync byte = 0x14
	stkOK     byte = 0x10
)

const (
	bootloaderSyncRetries = 5
	syncAttemptGap        = 200 * time.Millisecond
)

var errBootloaderUnresponsive = errors.New("bootloader unresponsive")

// stkClient wraps a serial.Port with the STK500v1 transactions optiboot supports.
type stkClient struct {
	p serial.Port
}

func newSTKClient(p serial.Port) *stkClient { return &stkClient{p: p} }

// Sync waits for the bootloader to reply to STK_GET_SYNC within the total
// budget. Retries up to bootloaderSyncRetries with a fixed gap between attempts.
func (c *stkClient) Sync(totalBudget time.Duration) error {
	per := totalBudget / time.Duration(bootloaderSyncRetries)
	if per <= 0 {
		per = 100 * time.Millisecond
	}
	for i := 0; i < bootloaderSyncRetries; i++ {
		if err := c.p.SetReadTimeout(per); err != nil {
			return fmt.Errorf("sync: set read timeout: %w", err)
		}
		if _, err := c.p.Write([]byte{stkGetSync, stkCrcEop}); err != nil {
			return fmt.Errorf("sync: write: %w", err)
		}
		buf := make([]byte, 2)
		n, err := c.p.Read(buf)
		if err != nil {
			return fmt.Errorf("sync: read: %w", err)
		}
		if n == 2 && buf[0] == stkInSync && buf[1] == stkOK {
			return nil
		}
		time.Sleep(syncAttemptGap)
	}
	return errBootloaderUnresponsive
}

// GetSignOn returns the bootloader's vendor string. Optiboot replies "AVR ISP";
// tolerate any vendor that returns a non-empty string between INSYNC and OK.
func (c *stkClient) GetSignOn(timeout time.Duration) (string, error) {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return "", fmt.Errorf("sign_on: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkGetSignOn, stkCrcEop}); err != nil {
		return "", fmt.Errorf("sign_on: write: %w", err)
	}
	buf := make([]byte, 64)
	out := make([]byte, 0, 16)
	seenInSync := false
	deadline := time.Now().Add(timeout)
	for {
		n, err := c.p.Read(buf)
		if err != nil {
			return "", fmt.Errorf("sign_on: read: %w", err)
		}
		out = append(out, buf[:n]...)
		if !seenInSync {
			if len(out) == 0 {
				if time.Now().After(deadline) {
					return "", errors.New("sign_on: timeout waiting for INSYNC")
				}
				continue
			}
			if out[0] != stkInSync {
				return "", fmt.Errorf("sign_on: expected INSYNC, got 0x%02X", out[0])
			}
			out = out[1:]
			seenInSync = true
		}
		for i, b := range out {
			if b == stkOK {
				return string(out[:i]), nil
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("sign_on: timeout waiting for OK")
		}
	}
}
