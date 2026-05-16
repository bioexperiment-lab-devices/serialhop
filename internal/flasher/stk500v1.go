package flasher

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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
	stkEnterProgMode byte = 0x50
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
//
// Per the bogdan-firmware doc: the first sync byte after a DTR-pulse reset can
// be eaten while optiboot is starting up, and partial replies (one byte
// arriving before the other) leave residual bytes that misalign every
// subsequent attempt. So we drain the RX buffer before each attempt and
// accumulate up to two reply bytes within the per-attempt budget.
func (c *stkClient) Sync(totalBudget time.Duration) error {
	per := totalBudget / time.Duration(bootloaderSyncRetries)
	if per <= 0 {
		per = 100 * time.Millisecond
	}
	start := time.Now()
	for i := 0; i < bootloaderSyncRetries; i++ {
		_ = c.p.Drain(10 * time.Millisecond)
		if err := c.p.SetReadTimeout(per); err != nil {
			return fmt.Errorf("sync: set read timeout: %w", err)
		}
		if _, err := c.p.Write([]byte{stkGetSync, stkCrcEop}); err != nil {
			return fmt.Errorf("sync: write: %w", err)
		}
		out := make([]byte, 0, 2)
		buf := make([]byte, 2)
		deadline := time.Now().Add(per)
		for len(out) < 2 && time.Now().Before(deadline) {
			n, err := c.p.Read(buf[:2-len(out)])
			if err != nil {
				return fmt.Errorf("sync: read: %w", err)
			}
			out = append(out, buf[:n]...)
		}
		if len(out) == 2 && out[0] == stkInSync && out[1] == stkOK {
			slog.Info("stk500 response", "len", len(out))
			slog.Debug("stk500 response payload", "hex", hex.EncodeToString(out))
			return nil
		}
		if len(out) > 0 {
			slog.Warn("stk500 nack", "expected", stkInSync, "got", out[0])
		}
		time.Sleep(syncAttemptGap)
	}
	slog.Warn("stk500 timeout", "phase", "handshake", "after", time.Since(start))
	return errBootloaderUnresponsive
}

// LoadAddress sets the bootloader's word-address pointer for the next ProgPage / ReadPage.
// wordAddr is the byte address divided by 2 — that's the STK500v1 convention.
func (c *stkClient) LoadAddress(timeout time.Duration, wordAddr uint16) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("load_address: set read timeout: %w", err)
	}
	msg := []byte{stkLoadAddress, byte(wordAddr & 0xFF), byte(wordAddr >> 8), stkCrcEop}
	if _, err := c.p.Write(msg); err != nil {
		return fmt.Errorf("load_address: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "load_address")
}

// ProgPage writes the page (flash memory type) at the current word address.
// The bootloader advances the word address by len(page)/2 after a successful write.
func (c *stkClient) ProgPage(timeout time.Duration, page []byte) error {
	n := len(page)
	header := []byte{stkProgPage, byte((n >> 8) & 0xFF), byte(n & 0xFF), 'F'}
	msg := make([]byte, 0, len(header)+n+1)
	msg = append(msg, header...)
	msg = append(msg, page...)
	msg = append(msg, stkCrcEop)
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("prog_page: set read timeout: %w", err)
	}
	if _, err := c.p.Write(msg); err != nil {
		return fmt.Errorf("prog_page: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "prog_page")
}

// ReadPage reads n bytes from flash at the current word address.
// The bootloader advances the word address by n/2 after a successful read.
func (c *stkClient) ReadPage(timeout time.Duration, n int) ([]byte, error) {
	msg := []byte{stkReadPage, byte((n >> 8) & 0xFF), byte(n & 0xFF), 'F', stkCrcEop}
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return nil, fmt.Errorf("read_page: set read timeout: %w", err)
	}
	if _, err := c.p.Write(msg); err != nil {
		return nil, fmt.Errorf("read_page: write: %w", err)
	}
	out := make([]byte, 0, n+2)
	buf := make([]byte, 256)
	start := time.Now()
	deadline := start.Add(timeout)
	for len(out) < n+2 {
		got, err := c.p.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("read_page: read: %w", err)
		}
		out = append(out, buf[:got]...)
		if time.Now().After(deadline) && len(out) < n+2 {
			slog.Warn("stk500 timeout", "phase", "read_page", "after", time.Since(start))
			return nil, fmt.Errorf("read_page: timeout (got %d of %d bytes)", len(out), n+2)
		}
	}
	if out[0] != stkInSync {
		slog.Warn("stk500 nack", "expected", stkInSync, "got", out[0])
		return nil, fmt.Errorf("read_page: expected INSYNC, got 0x%02X", out[0])
	}
	if out[n+1] != stkOK {
		slog.Warn("stk500 nack", "expected", stkOK, "got", out[n+1])
		return nil, fmt.Errorf("read_page: expected OK, got 0x%02X", out[n+1])
	}
	slog.Info("stk500 response", "len", len(out))
	slog.Debug("stk500 response payload", "hex", hex.EncodeToString(out))
	return out[1 : n+1], nil
}

// expectInSyncOK reads two bytes and verifies they are INSYNC and OK.
func (c *stkClient) expectInSyncOK(timeout time.Duration, op string) error {
	buf := make([]byte, 2)
	out := make([]byte, 0, 2)
	start := time.Now()
	deadline := start.Add(timeout)
	for len(out) < 2 {
		n, err := c.p.Read(buf[:2-len(out)])
		if err != nil {
			return fmt.Errorf("%s: read: %w", op, err)
		}
		out = append(out, buf[:n]...)
		if time.Now().After(deadline) && len(out) < 2 {
			slog.Warn("stk500 timeout", "phase", op, "after", time.Since(start))
			return fmt.Errorf("%s: timeout waiting for INSYNC/OK (got %d bytes)", op, len(out))
		}
	}
	if out[0] != stkInSync {
		slog.Warn("stk500 nack", "expected", stkInSync, "got", out[0])
		return fmt.Errorf("%s: expected INSYNC, got 0x%02X", op, out[0])
	}
	if out[1] != stkOK {
		slog.Warn("stk500 nack", "expected", stkOK, "got", out[1])
		return fmt.Errorf("%s: expected OK, got 0x%02X", op, out[1])
	}
	slog.Info("stk500 response", "len", len(out))
	slog.Debug("stk500 response payload", "hex", hex.EncodeToString(out))
	return nil
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
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		n, err := c.p.Read(buf)
		if err != nil {
			return "", fmt.Errorf("sign_on: read: %w", err)
		}
		out = append(out, buf[:n]...)
		if !seenInSync {
			if len(out) == 0 {
				if time.Now().After(deadline) {
					slog.Warn("stk500 timeout", "phase", "sign_on", "after", time.Since(start))
					return "", errors.New("sign_on: timeout waiting for INSYNC")
				}
				continue
			}
			if out[0] != stkInSync {
				slog.Warn("stk500 nack", "expected", stkInSync, "got", out[0])
				return "", fmt.Errorf("sign_on: expected INSYNC, got 0x%02X", out[0])
			}
			out = out[1:]
			seenInSync = true
		}
		for i, b := range out {
			if b == stkOK {
				fullResp := append([]byte{stkInSync}, out[:i+1]...)
				slog.Info("stk500 response", "len", len(fullResp))
				slog.Debug("stk500 response payload", "hex", hex.EncodeToString(fullResp))
				return string(out[:i]), nil
			}
		}
		if time.Now().After(deadline) {
			slog.Warn("stk500 timeout", "phase", "sign_on", "after", time.Since(start))
			return "", errors.New("sign_on: timeout waiting for OK")
		}
	}
}

// EnterProgMode sends STK_ENTER_PROGMODE. Optiboot accepts it as a no-op that
// replies INSYNC+OK, but the STK500v1 spec (and bogdan-firmware's reference
// flow §11.3) requires it once after sync, before any LoadAddress / ProgPage /
// ReadPage. Skipping it works against optiboot 8.x but is non-spec.
func (c *stkClient) EnterProgMode(timeout time.Duration) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("enter_progmode: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkEnterProgMode, stkCrcEop}); err != nil {
		return fmt.Errorf("enter_progmode: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "enter_progmode")
}

// ChipErase sends STK_CHIP_ERASE. Optiboot 512 B does NOT implement chip erase
// — avrdude requires the `-D` flag (skip chip erase) for this bootloader, and
// per-page erase happens implicitly during ProgPage. The opcode is kept here
// because the STK client is reusable, but production flash flow does not call
// it. See bogdan-firmware/docs/firmware-backup-and-flash.md §8.
func (c *stkClient) ChipErase(timeout time.Duration) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("chip_erase: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkChipErase, stkCrcEop}); err != nil {
		return fmt.Errorf("chip_erase: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "chip_erase")
}

// LeaveProgMode tells optiboot to hand control to the user sketch.
// Optiboot does NOT reset the chip; the user code starts running and the
// UART hardware switches to whatever baud the sketch configures.
func (c *stkClient) LeaveProgMode(timeout time.Duration) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("leave_progmode: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkLeaveProgMode, stkCrcEop}); err != nil {
		return fmt.Errorf("leave_progmode: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "leave_progmode")
}
