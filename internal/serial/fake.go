package serial

import (
	"sync"
	"time"
)

// FakePort is a thread-safe in-memory implementation of Port for tests.
//
// - Feed appends bytes to the read queue; the next Read calls drain them in order.
// - Written returns a snapshot of every byte the consumer has written.
// - Read blocks up to the SetReadTimeout duration; returns (0, nil) on timeout.
type FakePort struct {
	name        string
	mu          sync.Mutex
	rx          []byte // bytes available to be read
	tx          []byte // bytes written by consumer
	readTimeout time.Duration
	closed      bool
	rxSignal    chan struct{} // signaled whenever rx grows
	dtrSeq      []bool
	baudSeq     []int
}

func NewFakePort(name string) *FakePort {
	return &FakePort{name: name, rxSignal: make(chan struct{}, 1), readTimeout: time.Second}
}

func (f *FakePort) Name() string { return f.name }

func (f *FakePort) Feed(b []byte) {
	f.mu.Lock()
	f.rx = append(f.rx, b...)
	f.mu.Unlock()
	select {
	case f.rxSignal <- struct{}{}:
	default:
	}
}

func (f *FakePort) Written() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, len(f.tx))
	copy(out, f.tx)
	return out
}

func (f *FakePort) SetReadTimeout(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.readTimeout = d
	return nil
}

func (f *FakePort) Read(p []byte) (int, error) {
	deadline := time.Now().Add(f.currentTimeout())
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, ErrClosed
		}
		if len(f.rx) > 0 {
			n := copy(p, f.rx)
			f.rx = f.rx[n:]
			f.mu.Unlock()
			return n, nil
		}
		f.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil
		}
		select {
		case <-f.rxSignal:
		case <-time.After(remaining):
		}
	}
}

func (f *FakePort) currentTimeout() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readTimeout
}

func (f *FakePort) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrClosed
	}
	f.tx = append(f.tx, p...)
	return len(p), nil
}

func (f *FakePort) Drain(d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return ErrClosed
		}
		f.rx = nil
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (f *FakePort) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *FakePort) SetDTR(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.dtrSeq = append(f.dtrSeq, level)
	return nil
}

func (f *FakePort) SetBaudRate(rate int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.baudSeq = append(f.baudSeq, rate)
	return nil
}

func (f *FakePort) DTRSequence() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bool, len(f.dtrSeq))
	copy(out, f.dtrSeq)
	return out
}

func (f *FakePort) BaudSequence() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.baudSeq))
	copy(out, f.baudSeq)
	return out
}

// FakeOpener is an in-memory Opener for tests.
type FakeOpener struct {
	mu      sync.Mutex
	ports   map[string]*FakePort
	details map[string]DetailedPort
}

func NewFakeOpener() *FakeOpener {
	return &FakeOpener{
		ports:   map[string]*FakePort{},
		details: map[string]DetailedPort{},
	}
}

func (o *FakeOpener) Add(p *FakePort) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ports[p.Name()] = p
}

func (o *FakeOpener) Remove(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.ports, name)
}

func (o *FakeOpener) List() ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.ports))
	for n := range o.ports {
		out = append(out, n)
	}
	return out, nil
}

// Open returns the registered FakePort by name. It does NOT replace the
// FakePort instance, so the test can Feed/Written it directly.
// Note: FakePort survives Close(); to simulate a re-open after I/O failure,
// tests should manually reset its closed flag via Reopen().
func (o *FakeOpener) Open(name string) (Port, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.ports[name]
	if !ok {
		return nil, errUnknownPort{name}
	}
	p.mu.Lock()
	p.closed = false
	p.mu.Unlock()
	return p, nil
}

// OpenWithBaud opens the registered FakePort and records the initial baud rate.
func (o *FakeOpener) OpenWithBaud(name string, baud int) (Port, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.ports[name]
	if !ok {
		return nil, errUnknownPort{name}
	}
	p.mu.Lock()
	p.closed = false
	p.baudSeq = append(p.baudSeq, baud)
	p.mu.Unlock()
	return p, nil
}

// SetDetail registers USB descriptor information for a named port.
func (o *FakeOpener) SetDetail(name string, d DetailedPort) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.details[name] = d
}

// ListDetailed returns detailed port info for all registered ports. Ports that
// have had detail records set via SetDetail carry USB descriptor fields;
// ports added only via Add are returned with a minimal record (name only,
// IsUSB=false). This mirrors the behaviour of the real OS enumerator which
// always lists every port regardless of whether USB descriptors are available.
func (o *FakeOpener) ListDetailed() ([]DetailedPort, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]DetailedPort, 0, len(o.ports))
	for name := range o.ports {
		if d, ok := o.details[name]; ok {
			out = append(out, d)
		} else {
			out = append(out, DetailedPort{Name: name})
		}
	}
	return out, nil
}

type errUnknownPort struct{ name string }

func (e errUnknownPort) Error() string { return "unknown port: " + e.name }
