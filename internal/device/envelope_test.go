package device

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOKResponseShape(t *testing.T) {
	b, err := json.Marshal(OK("c9f3", map[string]any{"uptime_ms": 81}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":"c9f3"`, `"status":"ok"`, `"uptime_ms":81`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"error"`) {
		t.Errorf("ok response must omit error: %s", s)
	}
}

func TestErrorResponseShape(t *testing.T) {
	e := ErrInvalidParams("volume_ml", -1, "volume_ml must be positive")
	b, err := json.Marshal(Err("c9f3", e))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"status":"error"`, `"code":"invalid_params"`,
		`"message":"volume_ml must be positive"`,
		`"param":"volume_ml"`, `"value":-1`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"result"`) {
		t.Errorf("error response must omit result: %s", s)
	}
}

func TestCmdErrorIsError(t *testing.T) {
	var err error = ErrHardware("device not responding")
	if got := err.Error(); got != "hardware_error: device not responding" {
		t.Errorf("Error() = %q", got)
	}
}

func TestRequestDecode(t *testing.T) {
	var r Request
	if err := json.Unmarshal([]byte(`{"id":"a","cmd":"dispense","params":{"volume_ml":10}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != "a" || r.Cmd != "dispense" || string(r.Params) != `{"volume_ml":10}` {
		t.Errorf("bad decode: %+v", r)
	}
}
