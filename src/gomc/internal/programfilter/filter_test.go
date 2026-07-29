// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package programfilter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func iniGetter(vals map[string]string) func(string, string) string {
	return func(section, key string) string { return vals[section+"/"+key] }
}

func TestLookupOnlyForConfiguredExtensions(t *testing.T) {
	get := iniGetter(map[string]string{"FILTER/png": "image-to-gcode"})

	f, err := Lookup(get, "/programs/shape.png")
	if err != nil || f == nil {
		t.Fatalf("Lookup(.png) = %v, %v; want a filter", f, err)
	}
	if f.Argv[0] != "image-to-gcode" {
		t.Errorf("Argv = %v", f.Argv)
	}
	// The overwhelmingly common case: plain G-code needs no filter, and must
	// not pay for one.
	if f, err := Lookup(get, "/programs/part.ngc"); f != nil || err != nil {
		t.Errorf("Lookup(.ngc) = %v, %v; want no filter", f, err)
	}
	if f, err := Lookup(get, "/programs/noext"); f != nil || err != nil {
		t.Errorf("Lookup(no extension) = %v, %v; want no filter", f, err)
	}
}

func TestLookupTimeout(t *testing.T) {
	f, err := Lookup(iniGetter(map[string]string{
		"FILTER/png": "conv", "FILTER/FILTER_TIMEOUT": "12.5"}), "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if f.Timeout != 12500*time.Millisecond {
		t.Errorf("Timeout = %v, want 12.5s", f.Timeout)
	}

	f, err = Lookup(iniGetter(map[string]string{"FILTER/png": "conv"}), "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if f.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v with no FILTER_TIMEOUT, want the default %v", f.Timeout, DefaultTimeout)
	}

	// A misconfigured timeout is reported, not silently ignored: silently
	// running unbounded is exactly what the setting exists to prevent.
	if _, err := Lookup(iniGetter(map[string]string{
		"FILTER/png": "conv", "FILTER/FILTER_TIMEOUT": "soon"}), "a.png"); err == nil {
		t.Error("FILTER_TIMEOUT=soon accepted; want an error")
	}
}

func TestSplitArgs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"image-to-gcode", []string{"image-to-gcode"}},
		{"python3 /path/conv.py --flag", []string{"python3", "/path/conv.py", "--flag"}},
		{`conv "/path with spaces/x.py"`, []string{"conv", "/path with spaces/x.py"}},
		{`conv 'single quoted'`, []string{"conv", "single quoted"}},
		{`conv a\ b`, []string{"conv", "a b"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
	} {
		got, err := splitArgs(tc.in)
		if err != nil {
			t.Errorf("splitArgs(%q): %v", tc.in, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("splitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := splitArgs(`conv "unbalanced`); err == nil {
		t.Error("unbalanced quote accepted")
	}
}

// writeFilter drops an executable shell script and returns its path.
func writeFilter(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunProducesOutputAndProgress(t *testing.T) {
	dir := t.TempDir()
	conv := writeFilter(t, dir, "conv.sh", `
echo "FILTER_PROGRESS=0" >&2
echo "G21"
echo "FILTER_PROGRESS=50" >&2
echo "G1 X1 F100"
echo "FILTER_PROGRESS=100" >&2
echo "M2"
`)
	src := filepath.Join(dir, "in.tst")
	if err := os.WriteFile(src, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.ngc")

	var pcts []int
	f := &Filter{Argv: []string{conv}, Timeout: 30 * time.Second}
	if err := f.Run(context.Background(), src, dst, func(p int) { pcts = append(pcts, p) }); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "G21\nG1 X1 F100\nM2\n" {
		t.Errorf("filtered output = %q", got)
	}
	if len(pcts) != 3 || pcts[0] != 0 || pcts[2] != 100 {
		t.Errorf("progress reports %v, want 0..100", pcts)
	}
}

// A failing filter must say WHY: its stderr is the only thing that tells an
// operator their file is unconvertible, and it has to reach the error channel.
func TestRunReportsStderrAndRemovesPartialOutput(t *testing.T) {
	dir := t.TempDir()
	conv := writeFilter(t, dir, "bad.sh", `
echo "G21"
echo "cannot read image: bad magic" >&2
exit 3
`)
	src := filepath.Join(dir, "in.tst")
	if err := os.WriteFile(src, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.ngc")

	err := (&Filter{Argv: []string{conv}, Timeout: 30 * time.Second}).
		Run(context.Background(), src, dst, nil)
	if err == nil {
		t.Fatal("a filter exiting 3 reported success")
	}
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("error type %T, want *programfilter.Error", err)
	}
	if fe.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", fe.ExitCode)
	}
	if !strings.Contains(fe.Stderr, "bad magic") {
		t.Errorf("Stderr = %q, want the filter's own diagnosis", fe.Stderr)
	}
	// The half-written G21 must not survive as something openable.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("partial output left behind at %s", dst)
	}
}

func TestRunTimesOut(t *testing.T) {
	dir := t.TempDir()
	conv := writeFilter(t, dir, "hang.sh", "sleep 30\n")
	src := filepath.Join(dir, "in.tst")
	if err := os.WriteFile(src, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.ngc")

	start := time.Now()
	err := (&Filter{Argv: []string{conv}, Timeout: 300 * time.Millisecond}).
		Run(context.Background(), src, dst, nil)
	if err == nil {
		t.Fatal("a filter that never finishes reported success")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v to give up; the timeout did not kill it", elapsed)
	}
	if !strings.Contains(err.Error(), "FILTER_TIMEOUT") {
		t.Errorf("error %q does not point at the setting that bounds it", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("output left behind after a timeout")
	}
}

// The classic implementation built a shell command line by interpolating the
// file name into `sh -c "%s '%s'"`, escaping only single quotes. Names are user
// data — they come from a file chooser — so the filter must receive one intact
// argument, with no shell to interpret it.
func TestRunPassesHostileFileNameAsOneArgument(t *testing.T) {
	dir := t.TempDir()
	// Reports what it was actually handed, so the test sees the argument
	// rather than trusting the mechanism.
	conv := writeFilter(t, dir, "echoarg.sh", `printf '%s' "$1" > "$0.arg"`+"\n")
	hostile := filepath.Join(dir, `x'; touch pwned; echo '.tst`)
	if err := os.WriteFile(hostile, []byte("source\n"), 0o644); err != nil {
		t.Skipf("filesystem will not hold that name: %v", err)
	}
	dst := filepath.Join(dir, "out.ngc")

	if err := (&Filter{Argv: []string{conv}, Timeout: 30 * time.Second}).
		Run(context.Background(), hostile, dst, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := os.ReadFile(conv + ".arg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != hostile {
		t.Errorf("filter received %q, want the file name intact: %q", got, hostile)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("the file name was interpreted by a shell — it ran a command")
	}
}

func TestRunCancellationStopsTheFilter(t *testing.T) {
	dir := t.TempDir()
	conv := writeFilter(t, dir, "hang.sh", "sleep 30\n")
	src := filepath.Join(dir, "in.tst")
	if err := os.WriteFile(src, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()

	start := time.Now()
	err := (&Filter{Argv: []string{conv}}).
		Run(ctx, src, filepath.Join(dir, "out.ngc"), nil)
	if err == nil {
		t.Fatal("cancelled filter reported success")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("cancellation took %v", elapsed)
	}
}
