package pump

import (
	"errors"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

var errWatchAbandoned = errors.New("pump: completion watch abandoned")

// startWatch begins the opcode-18 completion wait (TRANSLATION §4 dispense
// step 9): a watcher goroutine blocks on the 4-byte elapsed-µs reply while
// the loop holds the reader — only write-only frames (stop/pause/resume) may
// touch the port meanwhile. The port handle is captured HERE, on the session
// goroutine, and closed over — never fetched inside the watcher (decision 1:
// reattach swaps the port; the old handle unblocks with ErrClosed).
// Loop-side, a watchdog bounds the wait to estimate×1.5 + 5 s of active
// (unpaused) time.
func (d *Driver) startWatch(gen int, estimate time.Duration) {
	port := d.s.Conn()
	h := &watchHandle{stop: make(chan struct{}), done: make(chan struct{})}
	d.watch = h
	d.s.HoldReader()
	d.s.Go(func() {
		reply, err := readCompletion(port, h.stop)
		close(h.done) // before the Post: stop() may be blocking on done
		d.s.Post(func() { d.watchEvent(h, gen, reply, err) })
	})
	d.armWatchdog(gen, time.Duration(1.5*float64(estimate))+5*time.Second)
}

// readCompletion accumulates exactly 4 reply bytes on the watcher goroutine,
// polling in WatchPoll slices so an abandon signal is noticed promptly.
// Returns serial.ErrClosed when a reattach/shutdown closes the port.
func readCompletion(port serial.Port, stop <-chan struct{}) ([]byte, error) {
	if err := port.SetReadTimeout(WatchPoll); err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 4)
	for {
		select {
		case <-stop:
			return nil, errWatchAbandoned
		default:
		}
		chunk := make([]byte, 4-len(buf))
		n, err := port.Read(chunk)
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk[:n]...)
		if len(buf) == 4 {
			return buf, nil
		}
	}
}

// watchEvent handles the watcher's report on the loop; after the watch-identity
// guard, its first act is releasing the reader (decision 3: release happens on
// the loop, via the watcher's Post). The release is guarded by watch identity:
// a stale event from a watcher already torn down by stop/Detach must not release
// a SUCCESSOR watcher's hold — abandonWatch released that stale watcher's hold
// itself. Stale events and jobs already failed by an unreachable transition
// (decision 2) are no-ops.
func (d *Driver) watchEvent(h *watchHandle, gen int, reply []byte, err error) {
	if d.watch != h {
		return // consumed by stop/Detach; abandonWatch/shutdown owned the release
	}
	d.s.ReleaseReader()
	d.watch = nil
	if gen != d.jobGen || d.job == nil || d.s.Jobs().Active() == nil {
		return // job already failed/cancelled elsewhere — tolerate
	}
	switch {
	case h.timedOut:
		// No completion within the budget: panel interference or a stall —
		// the run outcome is unknown (TRANSLATION §4 pause gap, mitigation a).
		d.s.Jobs().Fail(device.ErrHardware(
			"completion reply never arrived (panel interference or stall?)"))
		d.clearJob()
		// Panel disarm + liveness check still run; a failure here flips the
		// session unreachable via the standard Transact path.
		_, _ = d.s.Transact(serialFrame, 4, replyTimeout)
	case err != nil:
		// Port died mid-wait (reattach/shutdown closed it). The unreachable
		// machinery owns recovery; if the job somehow still shows active
		// (e.g. the write that failed was not job-fatal), fail it here.
		d.s.Jobs().Fail(device.ErrHardware("completion wait aborted: " + err.Error()))
		d.clearJob()
	default:
		us := uint32(reply[0])<<24 | uint32(reply[1])<<16 | uint32(reply[2])<<8 | uint32(reply[3])
		d.finishJob(gen, time.Duration(us)*time.Microsecond)
	}
}

// armWatchdog bounds the completion wait in ACTIVE time: fired early because
// a pause froze the job clock → re-arm for the remainder (one timer
// outstanding at a time — no unbounded Posts, decision 7).
func (d *Driver) armWatchdog(gen int, budget time.Duration) {
	d.s.After(budget, func() { d.watchdogFire(gen, budget) })
}

func (d *Driver) watchdogFire(gen int, budget time.Duration) {
	if gen != d.jobGen || d.job == nil || d.watch == nil {
		return // job finished or was replaced; nothing to time out
	}
	active := d.s.Jobs().Active()
	if active == nil {
		return
	}
	if elapsed := elapsedOf(active); elapsed < budget {
		d.s.After(budget-elapsed, func() { d.watchdogFire(gen, budget) })
		return
	}
	d.watch.timedOut = true
	close(d.watch.stop) // the watcher exits and posts the timeout event
}

// abandonWatch synchronously tears down a pending watch (used by stop: the
// firmware only replies to opcode 18 if the run finishes on its own, so
// after a stop frame the reply will never come). Blocks the loop up to
// ~WatchPoll — bounded, and done is always closed BEFORE the watcher's
// final Post, so this cannot deadlock. Clearing d.watch first makes the
// watcher's queued Post a full no-op in watchEvent, so the release below
// is the abandoned watcher's only release — a successor watcher's hold is
// never touched.
func (d *Driver) abandonWatch() {
	if d.watch == nil {
		return
	}
	h := d.watch
	d.watch = nil
	close(h.stop)
	<-h.done
	d.s.ReleaseReader()
}
