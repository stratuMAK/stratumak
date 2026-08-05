// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package daemon

import (
	"context"
	"log/slog"
	"testing"
)

// fakeSyslog records the severity each message was routed to.
type fakeSyslog struct {
	err, warn, info, debug []string
}

func (f *fakeSyslog) Err(m string) error     { f.err = append(f.err, m); return nil }
func (f *fakeSyslog) Warning(m string) error { f.warn = append(f.warn, m); return nil }
func (f *fakeSyslog) Info(m string) error    { f.info = append(f.info, m); return nil }
func (f *fakeSyslog) Debug(m string) error   { f.debug = append(f.debug, m); return nil }

func newTestHandler(level slog.Leveler) (*SyslogHandler, *fakeSyslog) {
	w := &fakeSyslog{}
	return &SyslogHandler{writer: w, level: level}, w
}

func TestSyslogHandler_Enabled(t *testing.T) {
	h, _ := newTestHandler(slog.LevelWarn)
	for _, tc := range []struct {
		level slog.Level
		want  bool
	}{
		{slog.LevelDebug, false},
		{slog.LevelInfo, false},
		{slog.LevelWarn, true},
		{slog.LevelError, true},
	} {
		if got := h.Enabled(context.Background(), tc.level); got != tc.want {
			t.Errorf("Enabled(%v) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

// TestSyslogHandler_LevelIsLive checks the pointer-Leveler wiring main.go relies
// on: the handler is built with &halcmd.LogLevel, so a later level change must
// take effect without rebuilding the logger.
func TestSyslogHandler_LevelIsLive(t *testing.T) {
	var lvl slog.LevelVar
	lvl.Set(slog.LevelError)
	h, _ := newTestHandler(&lvl)

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info enabled at level ERROR")
	}
	lvl.Set(slog.LevelDebug)
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info still disabled after lowering the level to DEBUG")
	}
}

func TestSyslogHandler_SeverityRouting(t *testing.T) {
	h, w := newTestHandler(slog.LevelDebug)
	log := slog.New(h)

	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error("e")

	for _, tc := range []struct {
		name string
		got  []string
		want string
	}{
		{"debug", w.debug, "d"},
		{"info", w.info, "i"},
		{"warning", w.warn, "w"},
		{"err", w.err, "e"},
	} {
		if len(tc.got) != 1 || tc.got[0] != tc.want {
			t.Errorf("%s messages = %q, want [%q]", tc.name, tc.got, tc.want)
		}
	}
}

func TestSyslogHandler_AttrsAppended(t *testing.T) {
	h, w := newTestHandler(slog.LevelDebug)
	slog.New(h).Info("started", "port", 5080, "mode", "rt")

	want := "started port=5080 mode=rt"
	if len(w.info) != 1 || w.info[0] != want {
		t.Errorf("message = %q, want [%q]", w.info, want)
	}
}

// TestSyslogHandler_WithAttrsIsolation is the aliasing regression: WithAttrs
// used to `append(h.attrs, attrs...)`, so two handlers derived from the same
// parent — the ordinary slog.With pattern — shared the parent's spare capacity
// and the second silently overwrote the first one's attrs.
func TestSyslogHandler_WithAttrsIsolation(t *testing.T) {
	h, w := newTestHandler(slog.LevelDebug)
	// Three chained With calls: the third append grows the backing array to
	// cap 4 with len 3, so the two branches below both write index 3 — the
	// exact shape the old in-place append corrupted.
	base := slog.New(h).With("a", 1).With("b", 2).With("c", 3)

	a := base.With("who", "alpha")
	b := base.With("who", "beta")

	a.Info("hello")
	b.Info("hello")

	if len(w.info) != 2 {
		t.Fatalf("got %d messages, want 2: %q", len(w.info), w.info)
	}
	wantA := "hello a=1 b=2 c=3 who=alpha"
	wantB := "hello a=1 b=2 c=3 who=beta"
	if w.info[0] != wantA || w.info[1] != wantB {
		t.Errorf("messages = %q, want [%q %q]", w.info, wantA, wantB)
	}
}

// TestSyslogHandler_HandlerAttrsPrecedeRecordAttrs pins the stdlib ordering:
// attrs bound via With come before the ones passed at the call site.
func TestSyslogHandler_HandlerAttrsPrecedeRecordAttrs(t *testing.T) {
	h, w := newTestHandler(slog.LevelDebug)
	slog.New(h).With("bound", 1).Info("msg", "inline", 2)

	want := "msg bound=1 inline=2"
	if len(w.info) != 1 || w.info[0] != want {
		t.Errorf("message = %q, want [%q]", w.info, want)
	}
}

// TestSyslogHandler_GroupQualifiesKeys covers WithGroup, whose `groups` field
// was recorded and then never used — attrs from different groups collided under
// bare keys.
func TestSyslogHandler_GroupQualifiesKeys(t *testing.T) {
	h, w := newTestHandler(slog.LevelDebug)
	log := slog.New(h).WithGroup("hal").WithGroup("thread")

	log.Info("cycle", "period", 1000000)

	want := "cycle hal.thread.period=1000000"
	if len(w.info) != 1 || w.info[0] != want {
		t.Errorf("message = %q, want [%q]", w.info, want)
	}

	// The group must not leak back into the parent handler.
	slog.New(h).Info("plain", "period", 1)
	if w.info[1] != "plain period=1" {
		t.Errorf("parent message = %q, want %q", w.info[1], "plain period=1")
	}
}

func TestSyslogHandler_EmptyWithIsIdentity(t *testing.T) {
	h, _ := newTestHandler(slog.LevelDebug)
	if got := h.WithAttrs(nil); got != slog.Handler(h) {
		t.Error("WithAttrs(nil) allocated a new handler")
	}
	if got := h.WithGroup(""); got != slog.Handler(h) {
		t.Error(`WithGroup("") allocated a new handler`)
	}
}
