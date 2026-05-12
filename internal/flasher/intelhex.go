// Package flasher implements remote firmware flashing for AVR / optiboot
// targets (Arduino Uno R3 and pin-compatible clones) using the STK500v1
// protocol over a serial port.
package flasher

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
)

const eraseFill = 0xFF // AVR flash erased state — used to pad gaps between records.

// ParseIntelHex parses an Intel HEX document and returns a flat byte image
// starting at address 0x0000. Gaps between records are padded with 0xFF
// (AVR's erased-flash state). Only record types 00 (data) and 01 (EOF) are
// supported; 02–05 are rejected explicitly. The returned image is trimmed
// to the highest address referenced by a data record.
//
// Tolerates a leading UTF-8 BOM, trailing whitespace, and \r\n line endings.
func ParseIntelHex(input []byte) ([]byte, error) {
	// Strip an optional UTF-8 BOM.
	input = bytes.TrimPrefix(input, []byte{0xEF, 0xBB, 0xBF})

	var img []byte
	sawEOF := false
	for lineNum, raw := range bytes.Split(input, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		if line[0] != ':' {
			return nil, fmt.Errorf("intelhex: line %d: missing ':' prefix", lineNum+1)
		}
		body := line[1:]
		if len(body)%2 != 0 {
			return nil, fmt.Errorf("intelhex: line %d: odd-length record", lineNum+1)
		}
		buf, err := hex.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("intelhex: line %d: %w", lineNum+1, err)
		}
		if len(buf) < 5 {
			return nil, fmt.Errorf("intelhex: line %d: record too short (%d bytes)", lineNum+1, len(buf))
		}
		length := int(buf[0])
		if 4+length+1 > len(buf) {
			return nil, fmt.Errorf("intelhex: line %d: record length %d overflows buffer (%d bytes)", lineNum+1, length, len(buf))
		}
		addr := int(buf[1])<<8 | int(buf[2])
		rtype := buf[3]
		data := buf[4 : 4+length]
		var sum byte
		for _, b := range buf[:len(buf)-1] {
			sum += b
		}
		expected := byte(-int8(sum))
		got := buf[len(buf)-1]
		if got != expected {
			return nil, fmt.Errorf("intelhex: line %d: bad checksum (got %02X, want %02X)", lineNum+1, got, expected)
		}

		switch rtype {
		case 0x00:
			end := addr + length
			if end > len(img) {
				grown := make([]byte, end)
				for i := range grown {
					grown[i] = eraseFill
				}
				copy(grown, img)
				img = grown
			}
			copy(img[addr:], data)
		case 0x01:
			sawEOF = true
		default:
			return nil, fmt.Errorf("intelhex: line %d: unsupported record type 0x%02X", lineNum+1, rtype)
		}

		if sawEOF {
			break
		}
	}
	if !sawEOF {
		return nil, fmt.Errorf("intelhex: missing EOF record (type 01)")
	}
	return img, nil
}

// RenderIntelHex serializes a flat byte image (starting at address 0x0000)
// into Intel HEX text. Records are at most 16 data bytes each, terminated
// with the EOF record (type 01). The output is parseable by ParseIntelHex
// and by any STK500v1-compliant tool (avrdude, arduino-cli, ...).
func RenderIntelHex(img []byte) string {
	const recordSize = 16
	var sb strings.Builder
	sb.Grow(len(img) * 3)

	for off := 0; off < len(img); off += recordSize {
		n := recordSize
		if off+n > len(img) {
			n = len(img) - off
		}
		hdr := []byte{byte(n), byte(off >> 8), byte(off & 0xFF), 0x00}
		var sum byte
		for _, b := range hdr {
			sum += b
		}
		for _, b := range img[off : off+n] {
			sum += b
		}
		cks := byte(-int8(sum))

		sb.WriteByte(':')
		writeHex(&sb, hdr)
		writeHex(&sb, img[off:off+n])
		writeHex(&sb, []byte{cks})
		sb.WriteByte('\n')
	}
	sb.WriteString(":00000001FF\n")
	return sb.String()
}

func writeHex(sb *strings.Builder, b []byte) {
	const digits = "0123456789ABCDEF"
	for _, v := range b {
		sb.WriteByte(digits[v>>4])
		sb.WriteByte(digits[v&0x0F])
	}
}
