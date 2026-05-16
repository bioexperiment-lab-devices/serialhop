package serial

import (
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestFakePort_ReadAfterFeed(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	p.Feed([]byte{10, 1, 2, 3})
	if err := p.SetReadTimeout(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	n, err := p.Read(buf)
	if err != nil || n != 1 || buf[0] != 10 {
		t.Fatalf("got n=%d buf=%v err=%v, want n=1 buf[0]=10", n, buf, err)
	}
}

func TestFakePort_ReadTimeout(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	if err := p.SetReadTimeout(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	start := time.Now()
	n, err := p.Read(buf)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected nil err on timeout, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected n=0 on timeout, got %d", n)
	}
	if elapsed < 15*time.Millisecond {
		t.Errorf("returned too fast: %v (want >=20ms)", elapsed)
	}
}

func TestFakePort_WriteCapture(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	in := []byte{1, 2, 3, 4, 0}
	n, err := p.Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	got := p.Written()
	if len(got) != len(in) {
		t.Fatalf("Written: got %v, want %v", got, in)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("Written[%d]: got %d, want %d", i, got[i], in[i])
		}
	}
}

func TestFakePort_AfterClose(t *testing.T) {
	p := NewFakePort("COMTEST")
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte{1}); !errors.Is(err, ErrClosed) {
		t.Errorf("Write after close: got %v, want ErrClosed", err)
	}
	if _, err := p.Read(make([]byte, 1)); !errors.Is(err, ErrClosed) {
		t.Errorf("Read after close: got %v, want ErrClosed", err)
	}
}

func TestFakePort_Drain(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	p.Feed([]byte{99, 99, 99})
	if err := p.Drain(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// After draining, Read should not see the drained bytes.
	if err := p.SetReadTimeout(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	n, _ := p.Read(make([]byte, 1))
	if n != 0 {
		t.Errorf("Drain did not consume buffered bytes")
	}
}

func TestFakeOpener_Open(t *testing.T) {
	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	p, err := o.Open("COM3")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if p.Name() != "COM3" {
		t.Errorf("Name: got %q, want COM3", p.Name())
	}
	if _, err := o.Open("COM99"); err == nil {
		t.Errorf("Open unknown port should error")
	}
}

func TestFakeOpener_List(t *testing.T) {
	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	o.Add(NewFakePort("COM7"))
	got, err := o.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("List len: got %d, want 2 (%v)", len(got), got)
	}
}

func TestFakePort_SetDTR_Records(t *testing.T) {
	p := NewFakePort("COM3")
	if err := p.SetDTR(false); err != nil {
		t.Fatalf("SetDTR(false): %v", err)
	}
	if err := p.SetDTR(true); err != nil {
		t.Fatalf("SetDTR(true): %v", err)
	}
	got := p.DTRSequence()
	want := []bool{false, true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DTRSequence: got %v, want %v", got, want)
	}
}

func TestFakePort_SetBaudRate_Records(t *testing.T) {
	p := NewFakePort("COM3")
	if err := p.SetBaudRate(115200); err != nil {
		t.Fatalf("SetBaudRate(115200): %v", err)
	}
	if err := p.SetBaudRate(9600); err != nil {
		t.Fatalf("SetBaudRate(9600): %v", err)
	}
	got := p.BaudSequence()
	want := []int{115200, 9600}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BaudSequence: got %v, want %v", got, want)
	}
}

func TestFakeOpener_OpenWithBaud(t *testing.T) {
	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	p, err := o.OpenWithBaud("COM3", 115200)
	if err != nil {
		t.Fatalf("OpenWithBaud: %v", err)
	}
	fp, ok := p.(*FakePort)
	if !ok {
		t.Fatalf("returned port is not *FakePort")
	}
	if got := fp.BaudSequence(); len(got) != 1 || got[0] != 115200 {
		t.Errorf("BaudSequence after OpenWithBaud: got %v, want [115200]", got)
	}
}

func TestFakeOpener_Open_LogsInfo(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	if _, err := o.Open("COM3"); err != nil {
		t.Fatalf("Open: %v", err)
	}

	rec.AssertRecord(t, slog.LevelInfo, "serial open", map[string]any{
		"serial_port": "COM3",
		"baud":        9600,
	})
}

func TestFakeOpener_Open_ErrorLogsError(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o := NewFakeOpener()
	_, err := o.Open("COM99")
	if err == nil {
		t.Fatal("expected error")
	}

	rec.AssertRecord(t, slog.LevelError, "serial open failed", map[string]any{
		"serial_port": "COM99",
	})
}

func TestFakeOpener_ListDetailed(t *testing.T) {
	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	o.SetDetail("COM3", DetailedPort{
		Name: "COM3", IsUSB: true, VID: "2341", PID: "0043",
		SerialNumber: "ABC123", Product: "Arduino Uno",
	})
	got, err := o.ListDetailed()
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ListDetailed): got %d, want 1", len(got))
	}
	if got[0].Product != "Arduino Uno" || got[0].VID != "2341" {
		t.Errorf("ListDetailed[0]: got %+v", got[0])
	}
}
