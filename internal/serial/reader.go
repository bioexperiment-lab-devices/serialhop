package serial

import "time"

// ReadFrame reads bytes from p with the following termination rules:
//
//   - Up to initialTimeout is spent waiting for the FIRST byte. If no byte
//     arrives, returns (nil, nil) — empty slice, no error.
//   - Once at least one byte has arrived, each subsequent byte gets up to
//     interByteTimeout before the function returns whatever was collected.
//   - If max > 0 and that many bytes have been collected, returns immediately.
//   - Always returns an io error from the underlying Port if Read fails.
//
// The caller must NOT rely on initialTimeout still being set after the call;
// ReadFrame mutates the port's read timeout via SetReadTimeout.
func ReadFrame(p Port, initialTimeout, interByteTimeout time.Duration, max int) ([]byte, error) {
	if err := p.SetReadTimeout(initialTimeout); err != nil {
		return nil, err
	}
	one := make([]byte, 1)
	n, err := p.Read(one)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil // initial timeout, no bytes
	}
	out := []byte{one[0]}
	if max > 0 && len(out) >= max {
		return out, nil
	}
	if err := p.SetReadTimeout(interByteTimeout); err != nil {
		return out, err
	}
	for max <= 0 || len(out) < max {
		n, err := p.Read(one)
		if err != nil {
			return out, err
		}
		if n == 0 {
			return out, nil // inter-byte silence — done
		}
		out = append(out, one[0])
	}
	return out, nil
}
