package device

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type nopDriver struct{}

func (nopDriver) Attach(context.Context, []byte) (Info, error) { return Info{}, nil }
func (nopDriver) Execute(context.Context, string, json.RawMessage) (any, *CmdError) {
	return nil, nil
}
func (nopDriver) Tick(time.Time) {}
func (nopDriver) Detach()        {}

func TestRegisterAndLookup(t *testing.T) {
	Register(201, "testdev", func(*Session) Driver { return nopDriver{} })
	name, factory, ok := LookupDriver(201)
	if !ok || name != "testdev" || factory == nil {
		t.Fatalf("lookup: %q %v %v", name, factory, ok)
	}
	if d := factory(nil); d == nil {
		t.Fatal("factory returned nil driver")
	}
	if _, _, ok := LookupDriver(202); ok {
		t.Fatal("unregistered code must not resolve")
	}
}
