package flasher

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseIntelHex_TwoDataRecordsAndEOF(t *testing.T) {
	input := strings.Join([]string{
		":0400000001020304F2",
		":0400040005060708DE",
		":00000001FF",
	}, "\n")
	got, err := ParseIntelHex([]byte(input))
	if err != nil {
		t.Fatalf("ParseIntelHex: %v", err)
	}
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	if !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

func TestParseIntelHex_GapsPadWithFF(t *testing.T) {
	input := strings.Join([]string{
		":01000000AA55",
		":0100040055A6",
		":00000001FF",
	}, "\n")
	got, err := ParseIntelHex([]byte(input))
	if err != nil {
		t.Fatalf("ParseIntelHex: %v", err)
	}
	want := []byte{0xAA, 0xFF, 0xFF, 0xFF, 0x55}
	if !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

func TestParseIntelHex_BadChecksum(t *testing.T) {
	input := ":0400000001020304F3\n:00000001FF"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected checksum error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error %q must mention checksum", err)
	}
}

func TestParseIntelHex_BadHexDigit(t *testing.T) {
	input := ":0400000001020Z04F2\n:00000001FF"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected hex-digit error, got nil")
	}
}

func TestParseIntelHex_MissingEOFRecord(t *testing.T) {
	input := ":0400000001020304F2"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected missing-EOF error, got nil")
	}
}

func TestParseIntelHex_RejectsUnsupportedRecordType(t *testing.T) {
	input := ":02000004FFFFFC\n:00000001FF"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected unsupported-record-type error, got nil")
	}
	if !strings.Contains(err.Error(), "record type") {
		t.Errorf("error %q must mention record type", err)
	}
}

func TestParseIntelHex_TolerantOfWhitespaceAndBOM(t *testing.T) {
	input := "\xef\xbb\xbf  :0400000001020304F2  \r\n :00000001FF \r\n"
	got, err := ParseIntelHex([]byte(input))
	if err != nil {
		t.Fatalf("ParseIntelHex: %v", err)
	}
	if !bytes.Equal(got, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("got % X", got)
	}
}

func TestRenderIntelHex_SingleSmallImage(t *testing.T) {
	img := []byte{0x01, 0x02, 0x03, 0x04}
	out := RenderIntelHex(img)
	if !strings.Contains(out, ":04000000") {
		t.Errorf("output missing data-record prefix: %q", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), ":00000001FF") {
		t.Errorf("output missing EOF record: %q", out)
	}
}

func TestIntelHex_RoundTrip(t *testing.T) {
	img := make([]byte, 256)
	for i := range img {
		img[i] = byte(i)
	}
	rendered := RenderIntelHex(img)
	parsed, err := ParseIntelHex([]byte(rendered))
	if err != nil {
		t.Fatalf("ParseIntelHex(rendered): %v", err)
	}
	if !bytes.Equal(img, parsed) {
		t.Errorf("round-trip mismatch")
	}
}

func FuzzParseIntelHex(f *testing.F) {
	f.Add([]byte(":0400000001020304F2\n:00000001FF"))
	f.Add([]byte(":00000001FF"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = ParseIntelHex(in)
	})
}
