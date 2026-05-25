//go:build !windows

package streamer

import (
	"context"
	"testing"
)

func TestFakeEnumerator_List(t *testing.T) {
	e := NewEnumerator()
	got, err := e.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 fake camera, got %d", len(got))
	}
	if got[0].ID == "" || got[0].Label == "" {
		t.Fatalf("fake camera should have id+label, got %+v", got[0])
	}
}
