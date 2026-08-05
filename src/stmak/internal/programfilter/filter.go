// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package programfilter runs the [FILTER] converter that turns a source file
// (an image, a DXF, a script) into the G-code the interpreter executes.
//
// Classic LinuxCNC ran this in the GUI: each UI filtered for itself, into its
// own temp directory, and a UI that did not implement filtering simply could
// not open those programs. The controller owns the loaded program now, so the
// filter runs here — once, when the program is opened — and every client sees
// the result through the same status and file-read APIs.
//
// Running a program named in the INI is the one place the controller executes
// user-supplied code, and stmakd carries file capabilities (src/Makefile's
// setuid target grants cap_sys_admin, cap_dac_override and more). What protects
// the filter child:
//
//   - It inherits none of those capabilities. Linux recomputes a process's
//     capabilities from the executed file, and nothing here sets ambient caps,
//     so an ordinary filter binary starts with none.
//   - No shell. The INI value is split into argv here and the source file is
//     passed as its own argument, so a file name containing quotes cannot run
//     commands — which the classic `sh -c "%s '%s'"` allowed.
//   - Its own process group, killed as a group on timeout.
//   - The real uid restored if the server ever runs with a different effective
//     one. Under the capability model uid == euid, so this is normally a no-op.
//   - No inherited descriptors — with a caveat. The Go runtime opens
//     everything O_CLOEXEC, but exec only closes what is marked: a descriptor
//     opened by linked C code WITHOUT O_CLOEXEC survives into the child. The
//     in-tree cgo code follows the rule (the EtherCAT master's raw/XDP
//     sockets, its tun and device handles — handles that would otherwise let
//     a filter script inject frames onto the fieldbus). New C code that
//     opens a long-lived descriptor must do the same, or it is handed to
//     every filter that runs.
//
// Not covered: PR_SET_NO_NEW_PRIVS on the child, which Go's os/exec cannot set
// without a helper process. A filter that execs a setuid binary can therefore
// still gain that binary's privileges — the same exposure the operator has from
// a shell, and no more, since the filter runs as the operator either way.
package programfilter

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout bounds a filter that never finishes. Generous on purpose:
// image-to-gcode on a large image legitimately takes minutes, and a filter
// killed halfway is indistinguishable to the operator from a broken one.
// [FILTER]FILTER_TIMEOUT overrides it (seconds; 0 disables the limit).
const DefaultTimeout = 5 * time.Minute

// progressLine is the classic filter progress protocol: a line of exactly
// this form on stderr is progress, everything else is error text.
var progressLine = regexp.MustCompile(`^FILTER_PROGRESS=(\d+)$`)

// Filter is the converter configured for one file extension.
type Filter struct {
	// Argv is the INI value split into a program and its arguments. The
	// source file is appended as one more argument at run time, never
	// interpolated into a string — a shell would otherwise let a file name
	// containing quotes run commands.
	Argv    []string
	Timeout time.Duration
}

// Error carries a filter's own diagnosis. Its stderr is what tells an
// operator why their file would not convert, so it travels to the error
// channel rather than being flattened into an exit code.
type Error struct {
	Prog     string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("filter %s failed", e.Prog)
	if e.ExitCode != 0 {
		msg = fmt.Sprintf("%s (exit %d)", msg, e.ExitCode)
	}
	if e.Err != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.Err)
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg = msg + ": " + s
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Lookup returns the filter configured for name's extension, or nil when the
// file needs none — the overwhelmingly common case, an ordinary .ngc.
//
// get reads an INI value; pass a namespaced getter to honour per-instance
// sections. The [FILTER] section keys off the extension without its dot,
// exactly as the classic UIs read it, so existing configs keep working.
func Lookup(get func(section, key string) string, name string) (*Filter, error) {
	if get == nil {
		return nil, nil
	}
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if ext == "" {
		return nil, nil
	}
	spec := strings.TrimSpace(get("FILTER", ext))
	if spec == "" {
		return nil, nil
	}
	argv, err := splitArgs(spec)
	if err != nil {
		return nil, fmt.Errorf("[FILTER]%s: %w", ext, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("[FILTER]%s: empty filter command", ext)
	}
	f := &Filter{Argv: argv, Timeout: DefaultTimeout}
	if v := strings.TrimSpace(get("FILTER", "FILTER_TIMEOUT")); v != "" {
		secs, err := strconv.ParseFloat(v, 64)
		if err != nil || secs < 0 {
			return nil, fmt.Errorf("[FILTER]FILTER_TIMEOUT=%q: want a non-negative number of seconds", v)
		}
		f.Timeout = time.Duration(secs * float64(time.Second))
	}
	return f, nil
}

// Run converts src into dst, calling onProgress with percentages the filter
// reports. It blocks until the filter exits, ctx is cancelled, or the timeout
// expires; the caller runs it off the command path so the controller stays
// responsive (a real conversion takes seconds to minutes).
//
// dst is created fresh and removed again if the filter fails, so a failed
// conversion never leaves a half-written program behind for something else to
// open.
func (f *Filter) Run(ctx context.Context, src, dst string, onProgress func(percent int)) error {
	// Lookup never builds an empty Argv, but Filter is an exported struct: a
	// hand-built one must fail as an error, not panic the controller.
	if len(f.Argv) == 0 {
		return &Error{Prog: "(filter)", Err: fmt.Errorf("empty filter command")}
	}
	prog := f.Argv[0]

	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}

	out, err := os.Create(dst)
	if err != nil {
		return &Error{Prog: prog, Err: fmt.Errorf("create %s: %w", dst, err)}
	}
	// Every failure path closes the output and removes it, so a failed
	// conversion leaves no half-written program behind. Both are cleanup ON TOP
	// of the error being returned — their own errors would say nothing the
	// returned one does not, which is why they are dropped here and nowhere
	// else in this function.
	fail := func(e *Error) error {
		_ = out.Close()
		_ = os.Remove(dst)
		return e
	}

	args := append(append([]string{}, f.Argv[1:]...), src)
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Stdout = out
	cmd.Stdin = nil
	// Classic sets this so filters know to emit FILTER_PROGRESS.
	cmd.Env = append(os.Environ(), "AXIS_PROGRESS_BAR=1")
	cmd.SysProcAttr = childAttr()
	// The timeout has to kill the process GROUP: a filter that spawns helpers
	// would otherwise leave them running with the pipe open.
	cmd.Cancel = func() error { return killGroup(cmd) }

	// The stderr pipe is owned here, not by exec (StderrPipe would let Wait
	// close the read end underneath a still-draining reader): the child gets
	// the write end, the parent's copy is closed right after Start, and EOF
	// arrives when the last writer exits.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return fail(&Error{Prog: prog, Err: err})
	}
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stderrR.Close()
		_ = stderrW.Close()
		return fail(&Error{Prog: prog, Err: err})
	}
	_ = stderrW.Close()

	diagCh := make(chan string, 1)
	go func() { diagCh <- readProgress(stderrR, onProgress) }()
	waitErr := cmd.Wait()
	// Bound the drain after the filter itself is gone. killGroup covers
	// helpers that stayed in the process group, but one that called setsid()
	// survives it with the write end open — without this bound the read
	// above would never see EOF, the conversion would never finish, and
	// `filtering` would wedge true for the life of the process.
	var diag string
	select {
	case diag = <-diagCh:
	case <-time.After(10 * time.Second):
		_ = stderrR.Close() // unblocks the reader
		diag = <-diagCh
	}
	_ = stderrR.Close()

	if waitErr != nil || ctx.Err() != nil {
		e := &Error{Prog: prog, Stderr: diag, Err: waitErr}
		if ee, ok := waitErr.(*exec.ExitError); ok {
			e.ExitCode = ee.ExitCode()
			e.Err = nil // the exit code IS the diagnosis; don't double-report
		}
		if ctx.Err() == context.DeadlineExceeded {
			e.Err = fmt.Errorf("timed out after %s ([FILTER]FILTER_TIMEOUT)", f.Timeout)
		} else if ctx.Err() != nil {
			e.Err = ctx.Err()
		}
		return fail(e)
	}

	// The filter wrote through this file, so its Close is part of the
	// conversion: a flush that fails here (a full disk, a dead network mount)
	// leaves dst truncated, and reporting success would hand the interpreter a
	// silently shortened program.
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return &Error{Prog: prog, Err: fmt.Errorf("write %s: %w", dst, err)}
	}
	return nil
}

// readProgress splits the filter's stderr into progress reports and
// everything else, returning the everything-else as the diagnosis.
func readProgress(r io.Reader, onProgress func(int)) string {
	var diag strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := progressLine.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			if onProgress != nil {
				if pct, err := strconv.Atoi(m[1]); err == nil {
					if pct < 0 {
						pct = 0
					}
					if pct > 100 {
						pct = 100
					}
					onProgress(pct)
				}
			}
			continue
		}
		// Bound what a chatty filter can accumulate; the first screenful is
		// what identifies the problem anyway.
		if diag.Len() < 8192 {
			diag.WriteString(line)
			diag.WriteByte('\n')
		}
	}
	return diag.String()
}

// childAttr builds the process attributes every filter runs under.
func childAttr() *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{
		// Own process group, so a timeout can kill the filter and anything it
		// spawned rather than orphaning helpers with the pipe still open.
		Setpgid: true,
	}
	// The server holds file capabilities rather than a setuid bit, so its real
	// and effective ids normally match and this is a no-op. It matters only if
	// someone runs it setuid after all — in which case the filter must not
	// inherit that identity.
	if uid, euid := os.Getuid(), os.Geteuid(); uid != euid {
		gid := os.Getgid()
		attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	return attr
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid addresses the whole group (Setpgid above made the child a
	// group leader).
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// splitArgs splits an INI filter spec into argv the way a shell would, minus
// the shell: quotes group, backslash escapes the next character. Everything
// else — pipelines, redirection, variable expansion — is deliberately not
// supported, because supporting it means handing the file name to a shell;
// an UNQUOTED shell metacharacter is rejected outright, because classic ran
// these specs through `sh -c` and passing the characters on as literal
// arguments would turn an old config's pipeline into a baffling runtime
// failure instead of a clear config error.
func splitArgs(spec string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote byte
	started := false

	// Byte-wise on purpose, byte-written on purpose: every character with
	// syntactic meaning here is ASCII, and multi-byte UTF-8 sequences must
	// pass through untouched — converting a lone byte to a rune would
	// re-encode it and corrupt every non-ASCII path.
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		switch {
		case c == '\\' && quote != '\'':
			i++
			if i >= len(spec) {
				return nil, fmt.Errorf("trailing backslash")
			}
			cur.WriteByte(spec[i])
			started = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			started = true
		case c == ' ' || c == '\t':
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		case strings.IndexByte("|&;<>$`()", c) >= 0:
			return nil, fmt.Errorf("shell syntax (%c) is not supported in a filter command; wrap the pipeline in a script and name that", c)
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced %c quote", quote)
	}
	if started {
		args = append(args, cur.String())
	}
	return args, nil
}
