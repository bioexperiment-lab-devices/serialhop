// Package testing provides an in-memory STK500v1 responder that simulates
// optiboot. Used by the flasher's unit tests. It implements
// internal/serial.Port so it can be substituted for a real port.
package testing

import (
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// STK500v1 wire opcodes used by optiboot (subset).
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
	stkNoSync byte = 0x15
)

// FakeOptiboot is the in-memory bootloader. The zero value is unusable; use NewFakeOptiboot.
type FakeOptiboot struct {
	mu sync.Mutex

	flash       [avr.FlashSize]byte
	wordAddr    uint16
	rx          []byte
	tx          []byte
	readTimeout time.Duration
	rxSignal    chan struct{}
	txSignal    chan struct{}
	closed      bool

	// error-injection knobs
	failSync          int
	corruptNextRead   bool
	corruptOffset     int
	dropWriteBytes    int
	ackButDontPersist bool
	failChipErase     bool
	failNextProgPage  bool
	failNextReadPage  bool

	dtrSeq  []bool
	baudSeq []int
}

// NewFakeOptiboot returns a FakeOptiboot with all flash bytes 0xFF (the AVR
// erased state).
func NewFakeOptiboot() *FakeOptiboot {
	f := &FakeOptiboot{
		rxSignal:    make(chan struct{}, 1),
		txSignal:    make(chan struct{}, 1),
		readTimeout: 100 * time.Millisecond,
	}
	for i := range f.flash {
		f.flash[i] = 0xFF
	}
	return f
}

// FailSyncTimes makes the next n STK_GET_SYNC requests produce no reply.
func (f *FakeOptiboot) FailSyncTimes(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSync = n
}

// CorruptNextReadPageAt schedules a one-byte corruption of the next STK_READ_PAGE reply.
func (f *FakeOptiboot) CorruptNextReadPageAt(off int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.corruptNextRead = true
	f.corruptOffset = off
}

// AckButDontPersistNextProgPage makes the next STK_PROG_PAGE return OK but
// skip the in-memory write.
func (f *FakeOptiboot) AckButDontPersistNextProgPage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackButDontPersist = true
}

// FailNextChipErase makes the next STK_CHIP_ERASE respond NOSYNC.
func (f *FakeOptiboot) FailNextChipErase() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failChipErase = true
}

// FailNextProgPage makes the next STK_PROG_PAGE respond NOSYNC.
func (f *FakeOptiboot) FailNextProgPage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNextProgPage = true
}

// FailNextReadPage makes the next STK_READ_PAGE respond NOSYNC.
func (f *FakeOptiboot) FailNextReadPage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNextReadPage = true
}

// FlashImage returns a copy of the current flash contents.
func (f *FakeOptiboot) FlashImage() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, len(f.flash))
	copy(out, f.flash[:])
	return out
}

// PreloadFlash writes data starting at byte address 0.
func (f *FakeOptiboot) PreloadFlash(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy(f.flash[:], data)
}

// --- serial.Port surface --------------------------------------------------

func (f *FakeOptiboot) Name() string { return "fake-optiboot" }

func (f *FakeOptiboot) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *FakeOptiboot) SetReadTimeout(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return labserial.ErrClosed
	}
	f.readTimeout = d
	return nil
}

func (f *FakeOptiboot) SetDTR(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return labserial.ErrClosed
	}
	f.dtrSeq = append(f.dtrSeq, level)
	return nil
}

func (f *FakeOptiboot) SetBaudRate(rate int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return labserial.ErrClosed
	}
	f.baudSeq = append(f.baudSeq, rate)
	return nil
}

func (f *FakeOptiboot) Drain(d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return labserial.ErrClosed
		}
		f.tx = nil
		f.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

func (f *FakeOptiboot) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, labserial.ErrClosed
	}
	in := p
	if f.dropWriteBytes > 0 {
		drop := f.dropWriteBytes
		if drop > len(in) {
			drop = len(in)
		}
		in = in[drop:]
		f.dropWriteBytes -= drop
	}
	f.rx = append(f.rx, in...)
	f.mu.Unlock()
	f.processRX()
	return len(p), nil
}

func (f *FakeOptiboot) Read(p []byte) (int, error) {
	deadline := time.Now().Add(f.currentTimeout())
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, labserial.ErrClosed
		}
		if len(f.tx) > 0 {
			n := copy(p, f.tx)
			f.tx = f.tx[n:]
			f.mu.Unlock()
			return n, nil
		}
		f.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil
		}
		select {
		case <-f.txSignal:
		case <-time.After(remaining):
		}
	}
}

func (f *FakeOptiboot) currentTimeout() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readTimeout
}

// processRX consumes whole STK500v1 commands from f.rx and appends replies to f.tx.
func (f *FakeOptiboot) processRX() {
	for {
		f.mu.Lock()
		if len(f.rx) == 0 {
			f.mu.Unlock()
			return
		}
		eop := indexByte(f.rx, stkCrcEop)
		if eop < 0 {
			f.mu.Unlock()
			return
		}
		cmd := append([]byte(nil), f.rx[:eop+1]...)
		f.rx = f.rx[eop+1:]

		reply := f.dispatch(cmd)
		if len(reply) > 0 {
			f.tx = append(f.tx, reply...)
			select {
			case f.txSignal <- struct{}{}:
			default:
			}
		}
		f.mu.Unlock()
	}
}

func (f *FakeOptiboot) dispatch(cmd []byte) []byte {
	if len(cmd) < 2 || cmd[len(cmd)-1] != stkCrcEop {
		return []byte{stkNoSync}
	}
	op := cmd[0]
	body := cmd[1 : len(cmd)-1]

	switch op {
	case stkGetSync:
		if f.failSync > 0 {
			f.failSync--
			return nil
		}
		return []byte{stkInSync, stkOK}

	case stkGetSignOn:
		return append([]byte{stkInSync}, append([]byte("AVR ISP"), stkOK)...)

	case stkLoadAddress:
		if len(body) != 2 {
			return []byte{stkNoSync}
		}
		f.wordAddr = uint16(body[0]) | uint16(body[1])<<8
		return []byte{stkInSync, stkOK}

	case stkProgPage:
		if f.failNextProgPage {
			f.failNextProgPage = false
			return []byte{stkNoSync}
		}
		if len(body) < 3 {
			return []byte{stkNoSync}
		}
		n := int(body[0])<<8 | int(body[1])
		if len(body) < 3+n {
			return []byte{stkNoSync}
		}
		data := body[3 : 3+n]
		byteAddr := int(f.wordAddr) * 2
		if !f.ackButDontPersist {
			copy(f.flash[byteAddr:], data)
		}
		f.ackButDontPersist = false
		f.wordAddr += uint16(n / 2)
		return []byte{stkInSync, stkOK}

	case stkReadPage:
		if f.failNextReadPage {
			f.failNextReadPage = false
			return []byte{stkNoSync}
		}
		if len(body) < 3 {
			return []byte{stkNoSync}
		}
		n := int(body[0])<<8 | int(body[1])
		byteAddr := int(f.wordAddr) * 2
		if byteAddr+n > len(f.flash) {
			return []byte{stkNoSync}
		}
		page := append([]byte(nil), f.flash[byteAddr:byteAddr+n]...)
		if f.corruptNextRead {
			if f.corruptOffset >= 0 && f.corruptOffset < len(page) {
				page[f.corruptOffset] ^= 0xFF
			}
			f.corruptNextRead = false
		}
		f.wordAddr += uint16(n / 2)
		reply := append([]byte{stkInSync}, page...)
		return append(reply, stkOK)

	case stkChipErase:
		if f.failChipErase {
			f.failChipErase = false
			return []byte{stkNoSync}
		}
		for i := range f.flash {
			f.flash[i] = 0xFF
		}
		return []byte{stkInSync, stkOK}

	case stkLeaveProgMode:
		return []byte{stkInSync, stkOK}
	}
	return []byte{stkNoSync}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
