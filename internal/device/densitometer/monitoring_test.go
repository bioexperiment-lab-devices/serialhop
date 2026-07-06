package densitometer_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
)

// runBlank completes a blank so measures are allowed.
func runBlank(t *testing.T, f *fixture) {
	t.Helper()
	bid := startJob(t, f, "measure_blank", "")
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank", func() bool { return jobResult(t, f, bid)["state"] == "succeeded" })
}

func TestStartMonitoringRequiresBlank(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("start_monitoring", `{"interval_s":30}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("start_monitoring without blank: %+v", resp)
	}
}

func TestStartMonitoringRejectsShortInterval(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	resp := f.exec("start_monitoring", `{"interval_s":5}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("interval < 10: %+v", resp)
	}
}

func TestMeasureRejectedWhileMonitoring(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	if resp := f.exec("start_monitoring", `{"interval_s":30}`); resp.Status != "ok" {
		t.Fatalf("start_monitoring: %+v", resp)
	}
	resp := f.exec("measure", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("measure while monitoring must be busy: %+v", resp)
	}
}

func TestGetReadingsSinceSeqAndDropped(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	// two one-off measures → seq 1, 2
	for _, slope := range []int{50, 60} {
		mid := startJob(t, f, "measure", "")
		feedSweepCompletion(f, slope, 27, 45)
		f.clock.Advance(densitometer.SweepWait)
		waitFor(t, "measure", func() bool { return jobResult(t, f, mid)["state"] == "succeeded" })
	}
	m := f.resultMap(f.exec("get_readings", `{"since_seq":0,"limit":100}`))
	if len(m["readings"].([]any)) != 2 || m["dropped"].(float64) != 0 {
		t.Fatalf("get_readings since 0: %v", m)
	}
	m = f.resultMap(f.exec("get_readings", `{"since_seq":1}`))
	rs := m["readings"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["seq"].(float64) != 2 {
		t.Fatalf("get_readings since 1: %v", m)
	}
}

func TestMonitoringSchedulerRunsMeasure(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	if resp := f.exec("start_monitoring", `{"interval_s":10}`); resp.Status != "ok" {
		t.Fatalf("start_monitoring: %+v", resp)
	}
	// Pre-feed the scheduled measure's completion, then fire a Tick and the
	// sweep completion.
	feedSweepCompletion(f, 50, 27, 45)
	f.clock.Advance(device.HeartbeatInterval) // Tick starts the monitor measure
	waitFor(t, "monitor measure started", func() bool {
		// the 78 4 trigger appears once the scheduler fires
		for _, fr := range f.frames() {
			if frameEq(fr, 78, 4, 0, 0, 0) {
				return true
			}
		}
		return false
	})
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "monitor reading buffered", func() bool {
		m := f.resultMap(f.exec("get_readings", `{"since_seq":0}`))
		return len(m["readings"].([]any)) >= 1
	})
}

func TestTickCanaryDetectsReboot(t *testing.T) {
	dir := t.TempDir()
	st := device.NewStore(dir, "densitometer-25-006")
	if err := st.Save(map[string]any{
		"schema_version": 1, "tube_correction": 1.0,
		"thermostat": map[string]any{"enabled": true, "target_c": 37.0},
	}); err != nil {
		t.Fatal(err)
	}
	shrinkTimeouts(t)
	clock := device.NewFakeClock(timeUnix1000())
	port := newPort("COM8")
	opener := newOpener(port)
	// Attach: serial, wavelength, force-tube, thermostat readback 37 (in sync).
	port.Feed([]byte{5, 7, 25, 6})
	port.Feed([]byte{1, 2, 6, 0})
	port.Feed([]byte{5, 5, 37, 0})
	f := startFixture(t, clock, port, opener, dir)
	// Idle canary poll fires ~CanaryInterval later: feed a rebooted readback
	// (10.00) then the re-push verify (37.00).
	port.Feed([]byte{5, 5, 10, 0}) // canary read → rebooted
	port.Feed([]byte{5, 5, 37, 0}) // re-push verify
	f.clock.Advance(densitometer.CanaryInterval + device.HeartbeatInterval)
	waitFor(t, "canary re-push", func() bool {
		for _, fr := range f.frames() {
			if frameEq(fr, 75, 2, 37, 0, 0) {
				return true
			}
		}
		return false
	})
}
