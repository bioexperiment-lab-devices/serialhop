package serial

import (
	"errors"
	"testing"
	"time"
)

func TestFakePort_ReadAfterFeed(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close()
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
	defer p.Close()
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
	defer p.Close()
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
	defer p.Close()
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
