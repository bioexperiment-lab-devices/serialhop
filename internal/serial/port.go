package serial

import (
	"errors"
	"fmt"
	"time"

	bugst "go.bug.st/serial"
)

var ErrClosed = errors.New("serial port closed")

// Port is the abstraction the rest of the code uses.
// All read methods respect the most recent SetReadTimeout call.
type Port interface {
	Read(p []byte) (int, error) // returns (0, nil) on read-timeout, never blocks past it
	Write(p []byte) (int, error)
	SetReadTimeout(d time.Duration) error
	Drain(d time.Duration) error // discard all RX bytes available within d
	Close() error
	Name() string
}

// Opener creates new Ports and lists available port names.
type Opener interface {
	Open(name string) (Port, error) // 9600 / 8N1, no flow control
	List() ([]string, error)
}

// realOpener is the production implementation backed by go.bug.st/serial.
type realOpener struct{}

func NewRealOpener() Opener { return realOpener{} }

func (realOpener) List() ([]string, error) {
	return bugst.GetPortsList()
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
