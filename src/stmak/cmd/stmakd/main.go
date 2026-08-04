// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Command stmakd is the Go-based server process for LinuxCNC.
//
// For RIP builds, source rip-environment first to set up PATH, LD_LIBRARY_PATH,
// PYTHONPATH, etc.  For installed builds, all paths are compiled in.
//
// Usage:
//
//	stmakd [Options] [path/to/ini_file]
//
// Options:
//
//	-D              Daemonize: go to background and log to syslog
//	-p path         PID file path (default: /var/run/stmakd.pid, implies -D)
//	-d level        Set log level: 0=DEBUG, 1=INFO (default), 2=WARN, 3=ERROR
//	-r          Disable redirection of stdout/stderr (for tests)
//	-l          Use the last-used INI file
//	-k          Continue in the presence of errors in HAL files
//	-t "name"   Custom trajectory planning module (overrides [TRAJ]TPMOD)
//	-m "name"   Custom homing module (overrides [EMCMOT]HOMEMOD)
//	-H "dir"    Prepend dir to HALLIB_PATH (may be specified multiple times)
//	-h          Show help
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/stratuMAK/stratumak/src/stmak/internal/daemon"
	_ "github.com/stratuMAK/stratumak/src/stmak/internal/hallib"
	"github.com/stratuMAK/stratumak/src/stmak/internal/launcher"

	halcmd "github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
)

func init() {
	// Pin the main goroutine to a single OS thread for the lifetime of the
	// process.  Several C libraries loaded via cmod plugins (most notably
	// milltask → Boost.Python → libpython) store per-thread state during
	// their New() phase and later reference it during Start().  Go's
	// scheduler is free to migrate a goroutine between OS threads between
	// CGo calls; without the lock the Start() CGo call may land on a
	// different thread, leaving Python without a valid thread-state and
	// causing a SIGSEGV in PyUnicode_New.
	runtime.LockOSThread()
}

// multiFlag is a flag.Value that accumulates repeated string flags (e.g. -H).
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	result := ""
	for i, v := range *m {
		if i > 0 {
			result += ":"
		}
		result += v
	}
	return result
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func main() {
	// Initialize RT application environment as early as possible:
	// sets up RLIMIT_MEMLOCK/RLIMIT_RTPRIO, calls mlockall(MCL_CURRENT) to lock
	// all currently-mapped pages (libc, librtapi, vdso, initial Go runtime pages),
	// installs signal handlers, and grants I/O privileges.
	// Must precede any HAL or component initialization.
	halcmd.RtapiInitializeApp()

	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("stmakd", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `stmakd: Run LinuxCNC

Usage:
  stmakd [Options] [path/to/ini_file]

  path/to/ini_file  Path to the INI configuration file.
                    Pass '-' to use the last-used INI file (same as -l).

Options:
`)
		fs.PrintDefaults()
	}

	var (
		daemonFlag    = fs.Bool("D", false, "Daemonize: go to background and log to syslog")
		pidFileFlag   = fs.String("p", "", "PID file `path` (default: "+daemon.DefaultPidFile+", implies -D)")
		debugLevel    = fs.Int("d", 1, `Log level: 0=DEBUG, 1=INFO, 2=WARN, 3=ERROR`)
		noRedirect    = fs.Bool("r", false, "Disable redirection of stdout/stderr to log files (use for tests)")
		useLast       = fs.Bool("l", false, "Use the last-used INI file")
		continueOnErr = fs.Bool("k", false, "Continue in the presence of errors in HAL files")
		tpMod         = fs.String("t", "", `Custom trajectory planning module name (overrides [TRAJ]TPMOD)`)
		homeMod       = fs.String("m", "", `Custom homing module name (overrides [EMCMOT]HOMEMOD)`)
		halFileFlag   = fs.String("f", "", "One-shot mode: execute the given HAL `file` (halrun) then exit; no INI/task/motion")
		halServeFlag  = fs.Bool("serve", false, "With -f: stay resident after executing the HAL file and serve the REST/WebSocket API (for streaming/capture tests); block until SIGINT/SIGTERM")
		halLibDirs    multiFlag
	)
	fs.Var(&halLibDirs, "H", "Prepend `dir` to HALLIB_PATH (may be specified multiple times)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	// Validate -H directories.
	for _, d := range halLibDirs {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "stmakd: invalid directory specified with -H: %s\n", d)
			return 1
		}
	}

	// Resolve the INI file path.
	var iniFile string
	if fs.NArg() > 0 {
		arg := fs.Arg(0)
		if arg == "-" {
			*useLast = true
		} else {
			abs, err := filepath.Abs(arg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stmakd: resolving INI file path: %v\n", err)
				return 1
			}
			iniFile = abs
		}
	}

	// One-shot HAL-file (halrun) mode does not use an INI file.
	if *halFileFlag == "" {
		if *useLast && iniFile == "" {
			fmt.Fprintln(os.Stderr, "stmakd: -l / last-used INI file not yet implemented")
			return 1
		}

		if iniFile == "" {
			fmt.Fprintln(os.Stderr, "stmakd: no INI file specified")
			return 1
		}
	}

	// Configure logger with dynamic level.
	if err := halcmd.SetDebug(*debugLevel); err != nil {
		fmt.Fprintf(os.Stderr, "stmakd: %v\n", err)
		return 1
	}

	// Determine if we should daemonize.
	// -pidfile implies -daemon.
	daemonMode := *daemonFlag || *pidFileFlag != ""
	pidFile := *pidFileFlag
	if daemonMode && pidFile == "" {
		pidFile = daemon.DefaultPidFile
	}

	if daemonMode {
		if err := daemon.Daemonize(pidFile); err != nil {
			fmt.Fprintf(os.Stderr, "stmakd: %v\n", err)
			return 1
		}
		// We are now the daemon child (the parent exited inside Daemonize).
		// Drop the PID file on EVERY exit path, not just a clean Run(): an
		// error return used to leave it behind, and nothing checks liveness, so
		// the next start silently overwrote a file that already looked valid.
		defer daemon.RemovePidFile(pidFile)

		// Redirect stdio and switch to syslog.
		if err := daemon.RedirectStdio(); err != nil {
			fmt.Fprintf(os.Stderr, "stmakd: redirect stdio: %v\n", err)
			return 1
		}
	}

	var logger *slog.Logger
	if daemonMode {
		handler, err := daemon.NewSyslogHandler(&halcmd.LogLevel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stmakd: syslog: %v\n", err)
			return 1
		}
		logger = slog.New(handler)
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &halcmd.LogLevel}))
	}

	opts := launcher.Options{
		NoRedirect:      *noRedirect,
		UseLast:         *useLast,
		ContinueOnError: *continueOnErr,
		TpMod:           *tpMod,
		HomeMod:         *homeMod,
		HalLibDirs:      []string(halLibDirs),
		IniFile:         iniFile,
	}

	// Before anything is loaded: say so if this binary was built locally from
	// stmak sources older than the installed ones.
	launcher.WarnIfStale(logger)

	l := launcher.New(opts, logger)

	// One-shot HAL-file (halrun) mode: execute the file and exit.  With -serve,
	// stay resident and serve the API after executing the file.
	if *halFileFlag != "" {
		if err := l.RunHalFile(*halFileFlag, *halServeFlag); err != nil {
			fmt.Fprintf(os.Stderr, "stmakd: %v\n", err)
			return 1
		}
		return 0
	}

	if err := l.Run(); err != nil {
		// In daemon mode the logger may have already reported this via
		// syslog; print to stderr regardless so foreground callers see it.
		fmt.Fprintf(os.Stderr, "stmakd: %v\n", err)
		return 1
	}
	return 0
}
