// Package slogtest provides a slog.Handler that records every log call
// for assertion in tests. Records are returned in the order they were
// emitted; attribute equality is value-based via fmt.Sprint.
//
// Typical use:
//
//	rec := slogtest.NewRecorder()
//	prev := slog.Default()
//	slog.SetDefault(slog.New(rec))
//	t.Cleanup(func() { slog.SetDefault(prev) })
//	... exercise code under test ...
//	rec.AssertRecord(t, slog.LevelWarn, "flasher retry", map[string]any{"retry": 1})
package slogtest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

// Record is a captured slog event with its attributes flattened to a
// plain map. Group attributes are flattened with dotted keys
// ("panel.session_id"). Nested groups are supported.
type Record struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// Recorder is a slog.Handler that appends each record to an internal slice.
type Recorder struct {
	mu   sync.Mutex
	recs []Record
	pre  []slog.Attr
	grp  string
}

func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (r *Recorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]any, rec.NumAttrs()+len(r.pre))
	prefix := r.grp
	for _, a := range r.pre {
		flatten(attrs, prefix, a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		flatten(attrs, prefix, a)
		return true
	})
	r.mu.Lock()
	r.recs = append(r.recs, Record{
		Level:   rec.Level,
		Message: rec.Message,
		Attrs:   attrs,
	})
	r.mu.Unlock()
	return nil
}

func (r *Recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	r.mu.Lock()
	pre := append([]slog.Attr{}, r.pre...)
	recs := r.recs
	grp := r.grp
	r.mu.Unlock()
	pre = append(pre, attrs...)
	return &Recorder{recs: recs, pre: pre, grp: grp}
}

func (r *Recorder) WithGroup(name string) slog.Handler {
	r.mu.Lock()
	pre := append([]slog.Attr{}, r.pre...)
	recs := r.recs
	grp := r.grp
	r.mu.Unlock()
	if grp == "" {
		grp = name
	} else {
		grp = grp + "." + name
	}
	return &Recorder{recs: recs, pre: pre, grp: grp}
}

func flatten(out map[string]any, prefix string, a slog.Attr) {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + a.Key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, sub := range a.Value.Group() {
			flatten(out, key, sub)
		}
		return
	}
	out[key] = a.Value.Any()
}

// Records returns a snapshot of captured records (safe to retain).
func (r *Recorder) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.recs))
	copy(out, r.recs)
	return out
}

// Find returns the first record matching level, message, and the given
// attr subset (each key's value must compare equal under fmt.Sprint).
// Returns nil if none match.
func (r *Recorder) Find(level slog.Level, message string, want map[string]any) *Record {
	for i := range r.Records() {
		rec := r.Records()[i]
		if rec.Level != level || rec.Message != message {
			continue
		}
		ok := true
		for k, v := range want {
			if got, present := rec.Attrs[k]; !present || fmt.Sprint(got) != fmt.Sprint(v) {
				ok = false
				break
			}
		}
		if ok {
			return &rec
		}
	}
	return nil
}

// AssertRecord fails the test if no record matches.
func (r *Recorder) AssertRecord(t *testing.T, level slog.Level, message string, want map[string]any) {
	t.Helper()
	if r.Find(level, message, want) == nil {
		t.Fatalf("no record matching level=%v message=%q attrs=%v; got: %v",
			level, message, want, r.Records())
	}
}
