package serial

import (
	"errors"
	"fmt"
	"time"

	bugst "go.bug.st/serial"
	bugstenum "go.bug.st/serial/enumerator"
)

var ErrClosed = errors.New("serial port closed")

// Port is the abstraction the rest of the code uses.
// All read methods respect the most recent SetReadTimeout call.
type Port interface {
	Read(p []byte) (int, error) // returns (0, nil) on read-timeout, never blocks past it
	Write(p []byte) (int, error)
	SetReadTimeout(d time.Duration) error
	Drain(d time.Duration) error // discard all RX bytes available within d
	SetDTR(level bool) error     // toggle DTR line — used to reset Arduino into bootloader
	SetBaudRate(rate int) error  // change baud on the open handle (e.g., 115200 -> 9600 between bootloader and sketch)
	Close() error
	Name() string
}

// Opener creates new Ports and lists available port names.
type Opener interface {
	Open(name string) (Port, error)                   // 9600 / 8N1, no flow control
	OpenWithBaud(name string, baud int) (Port, error) // arbitrary baud, 8N1, no flow control
	List() ([]string, error)
	ListDetailed() ([]DetailedPort, error)
}

// DetailedPort carries USB descriptors from the OS — the arduino-cli board list analog.
type DetailedPort struct {
	Name         string
	IsUSB        bool
	VID, PID     string
	SerialNumber string
	Product      string
}

// realOpener is the production implementation backed by go.bug.st/serial.
type realOpener struct{}

func NewRealOpener() Opener { return realOpener{} }

func (realOpener) List() ([]string, error) {
	return bugst.GetPortsList()
}

func (realOpener) OpenWithBaud(name string, baud int) (Port, error) {
	mode := &bugst.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   bugst.NoParity,
		StopBits: bugst.OneStopBit,
	}
	p, err := bugst.Open(name, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s @ %d: %w", name, baud, err)
	}
	return &realPort{name: name, p: p}, nil
}

func (realOpener) ListDetailed() ([]DetailedPort, error) {
	raw, err := bugstenum.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	out := make([]DetailedPort, 0, len(raw))
	for _, r := range raw {
		out = append(out, DetailedPort{
			Name:         r.Name,
			IsUSB:        r.IsUSB,
			VID:          r.VID,
			PID:          r.PID,
			SerialNumber: r.SerialNumber,
			Product:      r.Product,
		})
	}
	return out, nil
}

func (realOpener) Open(name string) (Port, error) {
	mode := &bugst.Mode{
		BaudRate: 9600,
		DataBits: 8,
		Parity:   bugst.NoParity,
		StopBits: bugst.OneStopBit,
	}
	p, err := bugst.Open(name, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return &realPort{name: name, p: p}, nil
}

type realPort struct {
	name string
	p    bugst.Port
}

func (r *realPort) Read(p []byte) (int, error)  { return r.p.Read(p) }
func (r *realPort) Write(p []byte) (int, error) { return r.p.Write(p) }
func (r *realPort) Close() error                { return r.p.Close() }
func (r *realPort) Name() string                { return r.name }

func (r *realPort) SetReadTimeout(d time.Duration) error {
	return r.p.SetReadTimeout(d)
}

func (r *realPort) SetDTR(level bool) error { return r.p.SetDTR(level) }

func (r *realPort) SetBaudRate(rate int) error {
	return r.p.SetMode(&bugst.Mode{
		BaudRate: rate,
		DataBits: 8,
		Parity:   bugst.NoParity,
		StopBits: bugst.OneStopBit,
	})
}

func (r *realPort) Drain(d time.Duration) error {
	if err := r.p.SetReadTimeout(10 * time.Millisecond); err != nil {
		return err
	}
	deadline := time.Now().Add(d)
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := r.p.Read(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			// no data within the inner read-timeout — keep looping until d elapses
			continue
		}
	}
	return nil
}
