// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package daemon

import (
	"context"
	"log/slog"
	"log/syslog"
	"strings"
)

// syslogWriter is the subset of *syslog.Writer SyslogHandler uses. Named so the
// severity routing can be tested without a syslog daemon.
type syslogWriter interface {
	Err(m string) error
	Warning(m string) error
	Info(m string) error
	Debug(m string) error
}

// SyslogHandler is an slog.Handler that writes to syslog.
type SyslogHandler struct {
	writer syslogWriter
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
}

// NewSyslogHandler creates a new slog.Handler that logs to syslog with the
// given facility and tag.
func NewSyslogHandler(level slog.Leveler) (*SyslogHandler, error) {
	w, err := syslog.New(syslog.LOG_DAEMON, "gomc-server")
	if err != nil {
		return nil, err
	}
	return &SyslogHandler{writer: w, level: level}, nil
}

func (h *SyslogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *SyslogHandler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	// Handler attrs first (they were bound earlier, via slog.With), then the
	// record's own — the order every stdlib handler emits.
	for _, a := range h.attrs {
		writeAttr(&sb, h.groups, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&sb, h.groups, a)
		return true
	})
	msg := sb.String()

	switch {
	case r.Level >= slog.LevelError:
		return h.writer.Err(msg)
	case r.Level >= slog.LevelWarn:
		return h.writer.Warning(msg)
	case r.Level >= slog.LevelInfo:
		return h.writer.Info(msg)
	default:
		return h.writer.Debug(msg)
	}
}

// writeAttr appends " group.key=value" for one attr. Open groups qualify the
// key, matching slog's dotted-path convention — without this the groups slice
// was recorded by WithGroup and then never used, so attrs from different groups
// collided under bare keys.
func writeAttr(sb *strings.Builder, groups []string, a slog.Attr) {
	sb.WriteByte(' ')
	for _, g := range groups {
		sb.WriteString(g)
		sb.WriteByte('.')
	}
	sb.WriteString(a.Key)
	sb.WriteByte('=')
	sb.WriteString(a.Value.String())
}

func (h *SyslogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	// Copy rather than append in place: append(h.attrs, ...) reuses h.attrs'
	// spare capacity, so two handlers derived from the SAME parent (the normal
	// slog.With pattern) would share the backing array and the second would
	// overwrite the first one's attrs.
	return &SyslogHandler{
		writer: h.writer,
		level:  h.level,
		attrs:  concatAttrs(h.attrs, attrs),
		groups: h.groups,
	}
}

func (h *SyslogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := make([]string, len(h.groups), len(h.groups)+1)
	copy(groups, h.groups)
	return &SyslogHandler{
		writer: h.writer,
		level:  h.level,
		attrs:  h.attrs,
		groups: append(groups, name),
	}
}

func concatAttrs(a, b []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}
