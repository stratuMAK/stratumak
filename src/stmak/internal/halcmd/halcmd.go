// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halcmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unsafe"

	hal "github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// LogLevel is the dynamic log level variable that controls which messages are
// emitted by the slog handler.  It is set at startup by stmakd and can be
// changed at runtime via SetDebug / "halcmd debug <level>".
// Levels follow stmak_log_level_t: 0=DEBUG, 1=INFO, 2=WARN, 3=ERROR.
var LogLevel slog.LevelVar

// SetLock sets the HAL lock level, restricting which commands are permitted.
// This is the low-level counterpart of Lock()/Unlock() and exists so that the
// halparse executor can set lock levels directly via an integer bitmask.
func SetLock(level int) error {
	return halSetLock(level, "lock")
}

// GetLock returns the current HAL lock bitmask (0-255). The bitmask is a HAL-
// internal detail (see HAL_LOCK_* in hal.h); it is exposed here for the halparse
// executor's classic `status` rendering, not over REST.
func GetLock() int {
	return halGetLock()
}

// LockStatusString renders the current HAL lock state in the classic halcmd
// `status` format (upstream print_lock_status in halcmd_commands.cc), byte for
// byte, so `halrun -f file | grep lock` matches classic output.
func LockStatusString() string {
	lock := halGetLock()
	var b strings.Builder
	b.WriteString("HAL locking status:\n")
	fmt.Fprintf(&b, "  current lock value %d (%02x)\n", lock, lock)
	if lock == 0 { // HAL_LOCK_NONE
		b.WriteString("  HAL_LOCK_NONE - nothing is locked\n")
	}
	if lock&1 != 0 { // HAL_LOCK_LOAD
		b.WriteString("  HAL_LOCK_LOAD    - loading of new components is locked\n")
	}
	if lock&2 != 0 { // HAL_LOCK_CONFIG
		b.WriteString("  HAL_LOCK_CONFIG  - link and addf is locked\n")
	}
	if lock&4 != 0 { // HAL_LOCK_PARAMS
		b.WriteString("  HAL_LOCK_PARAMS  - setting params is locked\n")
	}
	if lock&8 != 0 { // HAL_LOCK_RUN
		b.WriteString("  HAL_LOCK_RUN     - running/stopping HAL is locked\n")
	}
	return b.String()
}

// StartThreads starts all HAL realtime threads.
// This is the point at which realtime functions start being called.
// Equivalent to "halcmd start".
func StartThreads() error {
	return halStartThreads()
}

// StopThreads stops all HAL realtime threads.
// This should be called before any component that is part of a system exits.
// Equivalent to "halcmd stop".
func StopThreads() error {
	return halStopThreads()
}

// CreateThreadCPU creates a single HAL realtime thread with CPU affinity.
//
// When cpu=-1, the next available isolated CPU is automatically assigned from
// the pool initialized by InitCPUPool().  If the pool is exhausted the thread
// runs without affinity (a warning is logged in POSIX RT mode).
//
// When cpu>=0, the thread is pinned to that CPU. Any online CPU is accepted —
// pinning is the caller's explicit choice — but a non-isolated one logs a
// warning. Only an offline/out-of-range CPU is an error.
//
// HAL assigns thread priorities by creation order — each new thread one step
// below the previous one — so threads should be created fastest-first to get
// rate monotonic scheduling. That is a convention, not a rule: creating a
// faster thread later succeeds and produces a warning (see ThreadOrderWarning).
func CreateThreadCPU(name string, periodNs int64, usesFP int, cpu int) error {
	if w := ThreadOrderWarning(name, periodNs); w != "" {
		if logger := poolLogger(); logger != nil {
			logger.Warn(w)
		}
	}
	// A core popped for a thread that HAL then refuses must go back into the
	// pool — otherwise every failed newthread permanently burns an isolated
	// core. Only this call's own acquisition is undone: restoring a whole-pool
	// snapshot would also resurrect cores handed to concurrent newthread calls
	// between the snapshot and the failure.
	lease, err := acquireCPU(name, cpu)
	if err != nil {
		return err
	}
	if err := halCreateThreadCPU(name, periodNs, usesFP, lease.cpu); err != nil {
		releaseCPU(lease)
		return err
	}
	return nil
}

// ThreadOrderWarning reports whether creating a thread of the given period now
// would break rate monotonic scheduling, and returns the message to show if so
// (empty when the order is fine).
//
// HAL gives each newly created thread the next lower priority, so the thread
// about to be created will be the lowest-priority one. That inverts rate
// monotonic scheduling as soon as ANY existing thread has a LONGER period: the
// slower thread would preempt the faster one. Hence a scan of all threads
// rather than a comparison against the newest — once creation order is free and
// threads can be deleted, the most recently created thread is not necessarily
// the slowest one. The list is a handful of entries on a non-realtime path, so
// scanning beats caching a "slowest" value that delthread would invalidate.
//
// This lives in Go, not in hal_lib, because hal_lib's diagnostics go to the RT
// log: a warning emitted there would never reach the operator who typed the
// command.
func ThreadOrderWarning(name string, periodNs int64) string {
	threads, err := halShowThreads("")
	if err != nil {
		return ""
	}
	var slower []string
	for _, t := range threads {
		if t.Period > periodNs {
			slower = append(slower, fmt.Sprintf("%s (%d ns)", t.Name, t.Period))
		}
	}
	if len(slower) == 0 {
		return ""
	}
	return fmt.Sprintf("thread %s (%d ns) is created after the slower thread(s) %s, "+
		"so it gets a LOWER priority than they do and they will preempt it — "+
		"create faster threads first for rate monotonic scheduling",
		name, periodNs, strings.Join(slower, ", "))
}

// ThreadDelete deletes a HAL realtime thread by name.
// The thread must have been stopped (via StopThreads) before deletion.
func ThreadDelete(name string) error {
	return halThreadDelete(name)
}

// ListComponents returns the names of all currently loaded HAL components.
// Equivalent to "halcmd list comp".
func ListComponents() ([]string, error) {
	return halListComponents()
}

// UnloadAll unloads all HAL components except the specified one.
// Pass the caller's own component ID to avoid unloading itself.
// Equivalent to "halcmd unload all".
func UnloadAll(exceptCompID int) error {
	return halUnloadAll(exceptCompID)
}

// DelFunctsByComp removes all functions owned by comp_id from all threads.
// Returns the number of functions removed.
func DelFunctsByComp(compID int) (int, error) {
	return halDelFunctsByComp(compID)
}

// WaitCycleAdvance waits until every realtime thread has completed a full
// cycle that started after the call was entered, each thread measured against
// its own counter.  After it returns, no thread is still executing a function
// removed before the call, and values written before the call have been
// processed by every thread's functions (put on the wire, for a transport).
// Returns an error on timeout (two periods of the slowest thread, 100ms floor).
func WaitCycleAdvance() error {
	return halWaitCycleAdvance()
}

// FindCompID returns the comp_id for a named HAL component, or 0 if not found.
func FindCompID(name string) int {
	return halFindCompID(name)
}

// LockDLHandle locks the PT_LOAD segments of a single dlopen handle
// into memory, preventing page faults during RT execution.
func LockDLHandle(handle unsafe.Pointer) {
	halLockDLHandle(handle)
}

// UnlockDLHandle unlocks the PT_LOAD segments of a single dlopen handle.
func UnlockDLHandle(handle unsafe.Pointer) {
	halUnlockDLHandle(handle)
}

// NewInst creates a new instance of a HAL component type.
// Equivalent to "halcmd newinst <type> <name> [arg]".
func NewInst(compType, name, arg string) error {
	return halNewInst(compType, name, arg)
}

// RtapiInitializeApp initializes the RTAPI application environment.
// It sets up RT rlimits, calls mlockall(MCL_CURRENT) to lock currently-mapped
// pages (libc, librtapi, vdso, and initial Go runtime pages), installs signal
// handlers, and grants I/O privileges.  The function is idempotent: subsequent
// calls return immediately.
//
// This must be called as early as possible — before any HAL or component
// initialization — so that the locked page set is minimal and all RT privileges
// are in place before any RT-sensitive code runs.
func RtapiInitializeApp() {
	halRtapiInitializeApp()
}

// SetLogRing sets the stmak_log ring for the RTAPI message handler.
// Must be called before RtapiAppInit().
func SetLogRing(ring unsafe.Pointer) {
	halSetLogRing(ring)
}

// ClearMsgHandler disconnects the RTAPI message handler so that
// subsequent rtapi_print_msg calls are silently discarded.  Call
// before destroying the log ring.
func ClearMsgHandler() {
	halClearMsgHandler()
}

// RtapiAppInit initializes the in-process RTAPI/HAL environment.
// Sets up HAL shared memory.
// Must be called before hal_init() / hal.NewComponent().
func RtapiAppInit() error {
	return halRtapiAppInit()
}

// RtapiAppCleanup shuts down the in-process RTAPI/HAL environment.
// Tears down HAL threads and releases shared memory.
// Must be called after all components are unloaded.
func RtapiAppCleanup() {
	halRtapiAppCleanup()
}

// ===== Signal commands =====

// NewSig creates a new HAL signal with the given name and type.
// Equivalent to "halcmd newsig <name> <type>".
func NewSig(name string, halType hal.PinType) error {
	return halNewSig(name, halType)
}

// DelSig deletes a HAL signal by name.
// Equivalent to "halcmd delsig <name>".
func DelSig(name string) error {
	return halDelSig(name)
}

// Retain sets the HAL_SIGFLAG_RETAIN flag on a signal.
// The signal must not have any output writers.
// Equivalent to "halcmd retain <name>".
func Retain(name string) error {
	return halRetain(name)
}

// Unretain clears the HAL_SIGFLAG_RETAIN flag on a signal.
// Equivalent to "halcmd unretain <name>".
func Unretain(name string) error {
	return halUnretain(name)
}

// SetS sets the value of a HAL signal by name.
// The value is provided as a string and parsed according to the signal's type.
// Equivalent to "halcmd sets <name> <value>".
func SetS(name string, value string) error {
	return halSetS(name, value)
}

// GetS returns the current value of a HAL signal as a string.
// Equivalent to "halcmd gets <name>".
func GetS(name string) (string, error) {
	return halGetS(name)
}

// SType returns the data type of a HAL signal.
// Equivalent to "halcmd stype <name>".
func SType(name string) (hal.PinType, error) {
	return halSType(name)
}

// ===== Pin/param value commands =====

// SetP sets the value of a HAL pin or parameter by name.
// The value is provided as a string and parsed according to the pin/param type.
// Equivalent to "halcmd setp <name> <value>".
func SetP(name string, value string) error {
	return halSetP(name, value)
}

// GetP returns the current value of a HAL pin or parameter as a string.
// Equivalent to "halcmd getp <name>".
func GetP(name string) (string, error) {
	return halGetP(name)
}

// PType returns the data type of a HAL pin or parameter.
// Equivalent to "halcmd ptype <name>".
func PType(name string) (hal.PinType, error) {
	return halPType(name)
}

// ===== Link/net commands =====

// removeArrows filters out HAL arrow tokens (=>, <=, <=>) from a pin list.
// Arrow tokens are valid in .hal files for readability but have no functional meaning.
func removeArrows(pins []string) []string {
	result := make([]string, 0, len(pins))
	for _, p := range pins {
		if p != "=>" && p != "<=" && p != "<=>" {
			result = append(result, p)
		}
	}
	return result
}

// Net connects one or more pins to a signal, creating the signal if needed.
// Arrow tokens (=>, <=, <=>) are automatically stripped.
// If no pins remain after filtering, Net returns nil (matching halcmd behaviour
// which allows "net signame" with no pins to check/display a signal's status).
// Equivalent to "halcmd net <signame> [pins...]".
func Net(signame string, pins ...string) error {
	filtered := removeArrows(pins)
	if len(filtered) == 0 {
		// No pins specified — no-op, matching halcmd which allows this.
		return nil
	}
	return halNet(signame, filtered)
}

// LinkPS links a pin to a signal.
// Equivalent to "halcmd linkps <pin> <sig>".
func LinkPS(pin, sig string) error {
	return halLinkPS(pin, sig, "linkps")
}

// LinkSP links a signal to a pin (argument order reversed from LinkPS).
// Equivalent to "halcmd linksp <sig> <pin>".
func LinkSP(sig, pin string) error {
	return halLinkPS(pin, sig, "linksp")
}

// UnlinkP unlinks a pin from its signal.
// Equivalent to "halcmd unlinkp <pin>".
func UnlinkP(pin string) error {
	return halUnlinkP(pin)
}

// ===== Thread function commands =====

// AddF adds a function to a HAL thread.
// pos is the position in the thread's execution order (-1 appends at end).
// Equivalent to "halcmd addf <funct> <thread> [pos]".
func AddF(funct, thread string, pos int) error {
	return halAddF(funct, thread, pos)
}

// DelF removes a function from a HAL thread.
// Equivalent to "halcmd delf <funct> <thread>".
func DelF(funct, thread string) error {
	return halDelF(funct, thread)
}

// ===== Alias commands =====

// Alias creates an alternate name for a HAL pin or parameter.
// kind is "pin" or "param", name is the real name, alias is the alternate name.
// Equivalent to "halcmd alias <kind> <name> <alias>".
func Alias(kind, name, alias string) error {
	return halAlias(kind, name, alias)
}

// UnAlias removes an alias from a HAL pin or parameter.
// kind is "pin" or "param", name is the real or aliased name.
// Equivalent to "halcmd unalias <kind> <name>".
func UnAlias(kind, name string) error {
	return halUnAlias(kind, name)
}

// ===== Lock/unlock =====

// parseLockLevel maps a string level name to the HAL lock bitmask.
// Valid levels: "none", "load", "config", "params", "run", "tune", "all".
func parseLockLevel(level string) (int, error) {
	switch strings.ToLower(level) {
	case "none":
		return 0, nil // HAL_LOCK_NONE
	case "load":
		return 1, nil // HAL_LOCK_LOAD
	case "config":
		return 2, nil // HAL_LOCK_CONFIG
	case "params":
		return 4, nil // HAL_LOCK_PARAMS
	case "run":
		return 8, nil // HAL_LOCK_RUN
	case "tune":
		return 3, nil // HAL_LOCK_TUNE (LOAD | CONFIG)
	case "all":
		return 255, nil // HAL_LOCK_ALL
	default:
		return 0, fmt.Errorf("unknown lock level %q: valid levels are none, load, config, params, run, tune, all", level)
	}
}

// Lock sets the HAL lock level to restrict which commands are permitted.
// Equivalent to "halcmd lock <level>".
func Lock(level string) error {
	lvl, err := parseLockLevel(level)
	if err != nil {
		return err
	}
	return halSetLock(lvl, "lock")
}

// Unlock sets the HAL lock level to allow previously restricted commands.
// The level argument names the bits to clear from the CURRENT lock: "unlock all"
// removes all lock bits (→ LockNone=0), "unlock tune" clears just the tune bits
// from whatever is currently locked. This mirrors upstream halcmd semantics
// (do_unlock_cmd: hal_get_lock() & ~lvl).
// Equivalent to "halcmd unlock <level>".
func Unlock(level string) error {
	lvl, err := parseLockLevel(level)
	if err != nil {
		return err
	}
	return halSetLock(halGetLock()&^lvl, "unlock")
}

// ===== Query commands =====

// List returns the names of HAL objects of the given type that match any of
// the provided glob patterns. If no patterns are given, all objects are listed.
// halType is one of: "pin", "sig", "param", "funct", "thread", "comp".
// Equivalent to "halcmd list <type> [patterns...]".
func List(halType string, patterns ...string) ([]string, error) {
	if len(patterns) == 0 {
		patterns = []string{""}
	}

	var listFn func(string) ([]string, error)
	switch strings.ToLower(halType) {
	case "pin":
		listFn = halListPins
	case "sig":
		listFn = halListSigs
	case "param":
		listFn = halListParams
	case "funct":
		listFn = halListFuncts
	case "thread":
		listFn = halListThreads
	case "retain":
		listFn = halListRetainSigs
	case "comp":
		listFn = func(pat string) ([]string, error) {
			all, err := halListComponents()
			if err != nil {
				return nil, err
			}
			if pat == "" {
				return all, nil
			}
			// Filter with libc fnmatch (halFnmatch), the same matcher the C
			// shims use for pin/sig/param/funct/thread — so `list comp` shares
			// the glob dialect of every other list type instead of diverging on
			// Go's path.Match semantics.
			var filtered []string
			for _, name := range all {
				if halFnmatch(pat, name) {
					filtered = append(filtered, name)
				}
			}
			return filtered, nil
		}
	default:
		return nil, fmt.Errorf("list: unknown type %q: valid types are pin, sig, param, funct, thread, comp, retain", halType)
	}

	// Collect results for all patterns, deduplicating by name.
	seen := make(map[string]struct{})
	var all []string
	for _, pat := range patterns {
		names, err := listFn(pat)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				all = append(all, name)
			}
		}
	}
	return all, nil
}

// ===== Structured info types =====
//
// These are internal, CGO-backed domain types populated by reading HAL shared
// memory directly. They are NOT wire types: the REST provider
// (internal/halrest) converts them field-by-field into the generated
// halcmdapi.* types, which are the single source of truth for JSON marshaling
// (and carry the ",string" 64-bit tags). Deliberately no json tags here — so
// these can never be mistaken for the wire representation and silently drift
// from it (the cmd/ethercat hand-written-client trap).

// CompInfo holds the attributes of a HAL component.
type CompInfo struct {
	Name string
	ID   int
	Type string
}

// PinInfo holds all attributes of a HAL pin.
type PinInfo struct {
	Name      string
	Type      string
	Direction string
	Value     string
	Signal    string
	Owner     string
	HasWriter bool
}

// ParamInfo holds all attributes of a HAL parameter.
type ParamInfo struct {
	Name      string
	Type      string
	Direction string
	Value     string
	Owner     string
}

// SigInfo holds all attributes of a HAL signal.
type SigInfo struct {
	Name  string
	Type  string
	Value string
}

// FunctInfo holds all attributes of a HAL realtime function.
type FunctInfo struct {
	Name    string
	Owner   string
	Users   int32
	FP      bool
	MaxTime int64
}

// ThreadInfo holds all attributes of a HAL thread.
type ThreadInfo struct {
	Name    string
	Period  int64
	FP      bool
	Functs  []string
	Running bool
}

// ShowResult aggregates the results of a Show() call.
type ShowResult struct {
	Comps   []CompInfo
	Pins    []PinInfo
	Params  []ParamInfo
	Signals []SigInfo
	Functs  []FunctInfo
	Threads []ThreadInfo
}

// StatusInfo holds HAL shared-memory status information.
type StatusInfo struct {
	ShmemFree int
	LockLevel string
}

// ===== Show / Save / Status / SetDebug =====

// Show returns structured information about HAL objects of the given type
// that match any of the provided glob patterns.
// halType can be "all", "comp", "pin", "param", "sig", "funct", or "thread".
// If no patterns are given, all objects of that type are returned.
// Equivalent to "halcmd show <type> [patterns...]".
func Show(halType string, patterns ...string) (*ShowResult, error) {
	if len(patterns) == 0 {
		patterns = []string{""}
	}

	result := &ShowResult{}

	showComps := func(pat string) error {
		items, err := halShowComps(pat)
		if err != nil {
			return err
		}
		result.Comps = append(result.Comps, items...)
		return nil
	}
	showPins := func(pat string) error {
		items, err := halShowPins(pat)
		if err != nil {
			return err
		}
		result.Pins = append(result.Pins, items...)
		return nil
	}
	showParams := func(pat string) error {
		items, err := halShowParams(pat)
		if err != nil {
			return err
		}
		result.Params = append(result.Params, items...)
		return nil
	}
	showSigs := func(pat string) error {
		items, err := halShowSigs(pat)
		if err != nil {
			return err
		}
		result.Signals = append(result.Signals, items...)
		return nil
	}
	showFuncts := func(pat string) error {
		items, err := halShowFuncts(pat)
		if err != nil {
			return err
		}
		result.Functs = append(result.Functs, items...)
		return nil
	}
	showThreads := func(pat string) error {
		items, err := halShowThreads(pat)
		if err != nil {
			return err
		}
		result.Threads = append(result.Threads, items...)
		return nil
	}

	var showFns []func(string) error
	switch strings.ToLower(halType) {
	case "all", "":
		showFns = []func(string) error{showComps, showPins, showParams, showSigs, showFuncts, showThreads}
	case "comp":
		showFns = []func(string) error{showComps}
	case "pin":
		showFns = []func(string) error{showPins}
	case "param":
		showFns = []func(string) error{showParams}
	case "sig", "signal":
		showFns = []func(string) error{showSigs}
	case "funct", "function":
		showFns = []func(string) error{showFuncts}
	case "thread":
		showFns = []func(string) error{showThreads}
	default:
		return nil, fmt.Errorf("show: unknown type %q: valid types are all, comp, pin, param, sig, funct, thread", halType)
	}

	for _, fn := range showFns {
		for _, pat := range patterns {
			if err := fn(pat); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// Save serializes the current HAL state as halcmd commands.
// halType selects what to save: "all", "allu", "comp", "alias", "sig",
// "signal", "sigu", "link", "linka", "net", "neta", "netl", "netla",
// "netal", "param", "parameter", or "thread".
// If filename is non-empty the output is written to that file; otherwise
// the lines are returned as a slice.
// Equivalent to "halcmd save [type] [filename]".
func Save(halType string, filename string) ([]string, error) {
	if halType == "" {
		halType = "all"
	}
	lines, err := halSave(halType)
	if err != nil {
		return nil, err
	}
	if filename != "" {
		f, err := os.Create(filename)
		if err != nil {
			return nil, fmt.Errorf("save: cannot open %q: %w", filename, err)
		}
		for _, line := range lines {
			if _, err := fmt.Fprintln(f, line); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("save: write error: %w", err)
			}
		}
		// Check the write-file Close so a deferred/flush error is not lost.
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("save: closing %q: %w", filename, err)
		}
		return nil, nil
	}
	return lines, nil
}

// Status returns a summary of HAL shared-memory usage and lock state.
// Equivalent to "halcmd status".
func Status() (*StatusInfo, error) {
	return halStatus()
}

// SetDebug sets the log output verbosity level.
// Levels follow stmak_log_level_t: 0=DEBUG, 1=INFO, 2=WARN, 3=ERROR.
// Equivalent to "halcmd debug <level>".
func SetDebug(level int) error {
	switch level {
	case 0:
		LogLevel.Set(slog.LevelDebug)
	case 1:
		LogLevel.Set(slog.LevelInfo)
	case 2:
		LogLevel.Set(slog.LevelWarn)
	case 3:
		LogLevel.Set(slog.LevelError)
	default:
		return fmt.Errorf("invalid debug level %d (valid: 0=DEBUG, 1=INFO, 2=WARN, 3=ERROR)", level)
	}
	return nil
}

// GetDebug returns the current log output verbosity level, in the same
// 0=DEBUG..3=ERROR encoding SetDebug takes. A UI offering the level as a
// control needs this: the level is process-global and any halcmd client can
// change it, so a control that could only write would drift out of step with
// the server it claims to show.
func GetDebug() int {
	switch l := LogLevel.Level(); {
	case l <= slog.LevelDebug:
		return 0
	case l <= slog.LevelInfo:
		return 1
	case l <= slog.LevelWarn:
		return 2
	default:
		return 3
	}
}
