package densitometer_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
)

// TestFullSession walks the JSON_PROTOCOL §8 typical session:
// identify → set_thermostat → status → measure_blank → measure → monitoring →
// get_readings → stop.
func TestFullSession(t *testing.T) {
	f := newFixture(t)

	if f.exec("identify", "").Status != "ok" {
		t.Fatal("identify")
	}

	feedThermSet(f.port, 37)
	if f.exec("set_thermostat", `{"enabled":true,"target_c":37}`).Status != "ok" {
		t.Fatal("set_thermostat")
	}

	f.port.Feed([]byte{5, 5, 36, 98}) // status temperature
	f.port.Feed([]byte{5, 5, 37, 0})  // status thermostat (in sync)
	if st := f.resultMap(f.exec("status", "")); st["state"] != "idle" {
		t.Fatalf("status before measuring: %v", st)
	}

	bid := startJob(t, f, "measure_blank", "")
	feedSweepCompletion(f, 100, 37, 0)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank", func() bool { return jobResult(t, f, bid)["state"] == "succeeded" })

	mid := startJob(t, f, "measure", "")
	feedSweepCompletion(f, 50, 37, 0)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "measure", func() bool { return jobResult(t, f, mid)["state"] == "succeeded" })

	if f.exec("start_monitoring", `{"interval_s":60}`).Status != "ok" {
		t.Fatal("start_monitoring")
	}
	rd := f.resultMap(f.exec("get_readings", `{"since_seq":0}`))
	if len(rd["readings"].([]any)) != 1 {
		t.Fatalf("expected the one-off measure reading: %v", rd)
	}

	stop := f.resultMap(f.exec("stop", ""))
	if stop["state"] != "idle" {
		t.Fatalf("stop: %v", stop)
	}
}

func TestStatusMidSweepServesCachedTemperature(t *testing.T) {
	f := newFixture(t)
	// prime the cache with an idle status read
	f.port.Feed([]byte{5, 5, 30, 0}) // temperature 30.00
	f.port.Feed([]byte{5, 5, 0, 0})  // thermostat 0
	if f.resultMap(f.exec("status", ""))["temperature_c"].(float64) != 30.0 {
		t.Fatal("prime cache")
	}
	// start a sweep → busy window
	startJob(t, f, "measure_blank", "")
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "measuring" {
		t.Fatalf("state during sweep: %v", m)
	}
	if m["temperature_c"].(float64) != 30.0 {
		t.Fatalf("mid-sweep status must serve cached temperature, got %v", m["temperature_c"])
	}
}

func TestStopDuringMonitoring(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	if f.exec("start_monitoring", `{"interval_s":30}`).Status != "ok" {
		t.Fatal("start_monitoring")
	}
	if f.resultMap(f.exec("stop", ""))["state"] != "idle" {
		t.Fatal("stop must return idle")
	}
	// a subsequent Tick must not start a measure (monitoring disabled)
	before := len(f.frames())
	f.clock.Advance(device.HeartbeatInterval)
	time.Sleep(20 * time.Millisecond)
	for _, fr := range f.frames()[before:] {
		if frameEq(fr, 78, 4, 0, 0, 0) {
			t.Fatal("stop did not disable monitoring — a measure fired")
		}
	}
}
