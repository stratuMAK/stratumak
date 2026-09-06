// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package task implements the milltask gomod — the CNC task controller
// that coordinates motion, I/O, and the G-code interpreter.
//
// All state lives in the Task struct (no globals), making the module
// inherently multi-instance capable.
package task

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcerror"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
)

// TaskState represents the machine state (estop, on, etc.)
type TaskState int32

const (
	StateEstop      TaskState = 1
	StateEstopReset TaskState = 2
	StateOff        TaskState = 3
	StateOn         TaskState = 4
)

func (s TaskState) String() string {
	switch s {
	case StateEstop:
		return "ESTOP"
	case StateEstopReset:
		return "ESTOP_RESET"
	case StateOff:
		return "OFF"
	case StateOn:
		return "ON"
	default:
		return fmt.Sprintf("TaskState(%d)", int(s))
	}
}

// TaskMode represents the current operating mode.
type TaskMode int32

const (
	ModeManual TaskMode = 1
	ModeMDI    TaskMode = 3
	ModeAuto   TaskMode = 2
)

func (m TaskMode) String() string {
	switch m {
	case ModeManual:
		return "MANUAL"
	case ModeMDI:
		return "MDI"
	case ModeAuto:
		return "AUTO"
	default:
		return fmt.Sprintf("TaskMode(%d)", int(m))
	}
}

// InterpState represents the interpreter execution state.
type InterpState int32

const (
	InterpIdle    InterpState = 1
	InterpReading InterpState = 2
	InterpPaused  InterpState = 3
	InterpWaiting InterpState = 4
)

// ExecState represents the task execution state.
// Values must match the emcstat GMI enum (emcstat.gmi ExecState).
type ExecState int32

const (
	ExecError                     ExecState = 1
	ExecDone                      ExecState = 2
	ExecWaitingForMotion          ExecState = 3
	ExecWaitingForMotionQueue     ExecState = 4
	ExecWaitingForIO              ExecState = 5
	ExecWaitingForMotionAndIO     ExecState = 7
	ExecWaitingForDelay           ExecState = 8
	ExecWaitingForSystemCmd       ExecState = 9
	ExecWaitingForSpindleOriented ExecState = 10
)

// jogTimeout is how long a continuous jog stays active without being refreshed.
// Clients must re-send the jog command within this interval to keep it alive.
const jogTimeout = 2 * time.Second

// activeJog tracks a single active continuous jog for the watchdog.
type activeJog struct {
	active   bool
	isTeleop int32
	fromHAL  bool // HAL-pin-driven jogs are self-managing, skip watchdog
	lastSeen time.Time
}

// MotionController is the interface to motmod (motctl GMI API).
// Methods match the motctl.gmi function names.
type MotionController interface {
	// Motion queue
	SetLine(pos Pose, vel, iniMaxvel, acc float64, motionType int32, id int32, feedMmPerMin float64, indexerJnum int32) error
	SetCircle(pos Pose, center, normal Cartesian, turn int32, vel, iniMaxvel, acc float64, motionType int32, id int32, feedMmPerMin float64) error
	Probe(pos Pose, vel, iniMaxvel, acc float64, motionType int32, probeType uint8, id int32, feedMmPerMin float64) error
	ClearProbeFlags() error
	RigidTap(pos Pose, vel, iniMaxvel, acc float64, scale float64, id int32, feedMmPerMin float64) error

	// Motion control
	Abort() error
	Pause() error
	Resume() error
	Step(id int32) error
	Reverse() error
	Forward() error
	SetFree() error
	SetCoord() error
	SetTeleop() error
	Enable() error
	Disable() error

	// Jogging
	JogCont(num int32, vel float64, isTeleop int32) error
	JogIncr(num int32, vel, incr float64, isTeleop int32) error
	JogAbs(num int32, vel, pos float64, isTeleop int32) error
	JogAbort(num int32, isTeleop int32) error

	// Spindle
	SpindleOn(spindle int32, speed float64, css_factor float64, css_max float64, wait int32) error
	SpindleOff(spindle int32) error
	SpindleOrient(spindle int32, orientation float64, mode int32) error
	SpindleIncrease(spindle int32) error
	SpindleDecrease(spindle int32) error
	SpindleBrakeEngage(spindle int32) error
	SpindleBrakeRelease(spindle int32) error
	SetSpindleScale(spindle int32, scale float64) error

	// Overrides
	SetFeedScale(scale float64) error
	SetRapidScale(scale float64) error
	SetMaxFeedOverride(max float64) error
	FeedScaleEnable(enable int32) error
	SpindleScaleEnable(spindle int32, enable int32) error
	AdaptiveFeedEnable(enable int32) error
	FeedHoldEnable(enable int32) error

	// Limits and homing
	OverrideLimits(joint int32) error
	JointHome(joint int32) error
	JointUnhome(joint int32) error

	// Parameters
	SetVel(vel float64) error
	SetVelLimit(vel float64) error
	SetAcc(acc float64) error
	SetTermCond(cond int32, tolerance float64) error
	SetOffset(offset Pose) error
	SetDebug(level int32) error

	// I/O
	SetDout(index, value int32) error
	SetDoutSynched(index, startValue, endValue int32) error
	SetAout(index int32, value float64) error
	SetAoutSynched(index int32, startValue, endValue float64) error

	// Spindle sync
	SetSpindlesync(sync float64, motionType int32) error
}

// IOController is the interface to iocontrol (emcio GMI API).
type IOController interface {
	CoolantFloodOn() error
	CoolantFloodOff() error
	CoolantMistOn() error
	CoolantMistOff() error
	LubeOn() error
	LubeOff() error
	ToolPrepare(tool int32) error
	ToolLoad() error
	ToolUnload() error
	ToolStartChange() error
	ToolSetNumber(tool int32) error
	ToolSetOffset(pocket, toolno int32, x, y, z, a, b, c, u, v, w, diameter, frontangle, backangle float64, orientation int32) error
	ToolLoadTable(file string) error
	EstopOn() error
	EstopOff() error
	IoAbort(reason int32) error
	SetDebug(debug int32) error
	GetCmdStatus() (int32, error) // 1=DONE, 2=EXEC, 3=ERROR
	GetToolInSpindle() (int32, error)
	GetPocketPrepped() (int32, error)
	// GetToolStatus returns tool-in-spindle, pocket-prepped and
	// tool-from-pocket from ONE io status read — BuildStat runs at the
	// status publish rate, and the per-field getters each cost a full io
	// GetStatus round-trip. (tool_from_pocket has no per-field getter for
	// that reason: this is its only reader.)
	GetToolStatus() (toolInSpindle, pocketPrepped, toolFromPocket int32, err error)
}

// IO CmdStatus values.
const (
	IOStatusDone  int32 = 1
	IOStatusExec  int32 = 2
	IOStatusError int32 = 3
)

// MotionStatusReader provides read access to motion state (motstat GMI API).
type MotionStatusReader interface {
	GetStatus() (motstat.MotionStatus, error)
	GetPosCmd() (motstat.Pose, error)
	GetPosFb() (motstat.Pose, error)
	GetInpos() (int32, error)
	GetExecId() (int32, error)
	GetQueueDepth() (int32, error)
	GetCommandNumEcho() (int32, error)
	GetCommandStatus() (int32, error)
	// Narrow single-element accessors for the M66 poll loop — avoid copying the
	// full MotionStatus (joints, spindles, 64-wide DIO/AIO arrays, poses) just to
	// read one bit/value ~100×/s while waiting (E3).
	GetSynchDi(index int32) (int32, error)
	GetAnalogInput(index int32) (float64, error)
}

// ErrorPublisher publishes operator error/text/display messages to UI clients.
type ErrorPublisher interface {
	OperatorError(text string)
	OperatorText(text string)
	OperatorDisplay(text string)
}

// Pose represents a 9-axis position.
// Type alias for the generated motctl.Pose.
type Pose = motctl.Pose

// Cartesian represents a 3D vector.
// Type alias for the generated motctl.Cartesian.
type Cartesian = motctl.Cartesian

// Task is the central controller state. One instance per machine.
//
// Locking model (three locks, strict order cmdMu > seqLifeMu > mu):
//
//   - cmdMu serializes mutating commands for their FULL duration, including
//     I/O and waits. All emccmd entry points are blocking calls; cmdMu is what
//     makes their guard->act sequences atomic against each other (the Go
//     equivalent of the C milltask's single NML command queue). Worker
//     goroutines (sequencer, runProgram producer, mcode worker) never take
//     cmdMu. Abort-class operations (Abort, external/UI estop, pause, resume,
//     jog-stop) fire their signals WITHOUT cmdMu first — signals are what
//     unblock a command stuck in EnqueueCmd backpressure, so they must never
//     queue behind one — and only the state cleanup takes cmdMu.
//   - seqLifeMu serializes sequencer generation changes (StartSequencer /
//     StopSequencer), so concurrent restart requests (e.g. a monitor fault
//     teardown racing a user abort) cannot spawn two sequencer loops.
//   - mu is the short-lived state lock. It is never held across blocking I/O
//     or channel waits; several internal helpers release and re-acquire it
//     around external calls (safe because cmdMu holds command-level atomicity).
//   - msgMu (leaf, below mu) guards only the operator message list, so
//     operatorError is callable from any locking context, including under mu.
//
// Guard evaluation is two-phase: every mutating command runs its full guard
// set as a preflight BEFORE acquiring cmdMu (immediate rejection instead of
// queueing behind a long command) and again inside the serialized body (the
// authoritative check — state may change while waiting for the lock).
//
// Interpreter ownership: the C++ interpreter is not thread-safe. It may be
// touched only (a) under cmdMu with the interpreter idle (checked via
// programBusy), or (b) by the runProgram producer goroutine, whose lifetime
// teardown paths synchronize on runDone before touching the interpreter.
// BuildStat never calls the interpreter; it reads caches published by
// updateActiveCodes.
type Task struct {
	mu sync.Mutex

	// cmdMu serializes mutating command entry points end-to-end (see the
	// locking model above). Always acquired before mu, never while holding mu.
	cmdMu sync.Mutex

	// seqLifeMu serializes StartSequencer/StopSequencer generation changes.
	seqLifeMu sync.Mutex

	// Current state
	state       TaskState
	mode        TaskMode
	interpState InterpState
	execState   ExecState

	// Transactional mode restore: when ensureMode switches mode for a
	// command (e.g. MDI from manual), the previous mode is saved here.
	// After the command completes (MDI queue drained), mode is restored.
	modeBeforeTx TaskMode
	modeTx       bool // true if ensureMode performed a transient switch

	// Configuration
	numJoints       int
	numSpindles     int
	axisMask        int32
	linearUnits     float64
	angularUnits    float64
	maxVelocity     float64
	maxAcceleration float64
	jointMaxVel     [16]float64           // per-joint max velocity for jog clamping
	jointLinear     [16]bool              // per-joint linearity ([JOINT_n]TYPE); LINEAR joints scale machine->mm, ANGULAR don't
	jointHoming     [16]jointHomingParams // INI-fixed homing params, cached so a HAL home/offset/seq change re-pushes them unchanged
	axisMaxVel      [9]float64            // per-axis max velocity for jog clamping + canon blend
	axisMaxAcc      [9]float64            // per-axis max acceleration for canon vel/acc blend
	startupCode     string
	debug           int32 // EMC_SET_DEBUG level, echoed to stat.debug
	// [EMCIO]RANDOM_TOOLCHANGER: flips the pocket semantics of the tool
	// canon getters (spindle tool lives at pocket 0 vs the non-random
	// "empty spindle = idx -1" convention).
	randomToolchanger bool
	// [EMCIO]TOOL_CHANGE_POSITION: absolute machine coordinates to move to
	// before a tool change (2.9 CHANGE_TOOL canon). Internal mm/degrees;
	// toolChangePosLen is 0 (unset), 3, 6 or 9 coords given.
	toolChangePos    [9]float64
	toolChangePosLen int

	// Flags
	optionalStop  bool
	blockDelete   bool
	floodOn       bool
	mistOn        bool
	lubeOn        bool
	noForceHoming bool // [TRAJ]NO_FORCE_HOMING — skip homing check before MDI/AUTO
	stepping      bool // single-step mode: auto-pause after each interpreter line
	interpActive  bool // true while runProgram goroutine is executing
	// runDone is closed by runProgram when it exits, so teardown paths can wait
	// for the producer to stop touching the interpreter before Close/Reset.
	runDone  chan struct{}
	dwellEnd time.Time // wall-clock end of the current G4 dwell (for delayLeft)

	// Jog selection (shared across clients)
	jogAxis      int32   // selected jog axis (0=X .. 8=W, -1=none)
	jogIncrement float64 // current jog increment (0 = continuous)
	jogSpeed     float64 // linear jog speed (units/sec, from UI slider)
	ajogSpeed    float64 // angular jog speed (deg/sec, from UI slider)

	// Jog watchdog: active continuous jogs must be refreshed within jogTimeout.
	activeJogs [maxJoints]activeJog // indexed by axis_or_joint number

	// Line tracking (for stat reporting)
	readLine    int32 // line the interpreter has read up to
	currentLine int32 // line currently being executed by sequencer

	// lastMotionID is the serial id of the last motion command the sequencer
	// dispatched to the motion controller. The abort-time modal restore
	// (abortLocked) needs the tag of the segment "the machine was on", and
	// motion's executing id is 0 whenever the TP queue has momentarily run
	// dry (tpHandleEmptyQueue on feed starvation — exactly where a user abort
	// tends to land, since exact-stop corners are where starved motion sits).
	// With an empty queue everything dispatched has executed, so the last
	// dispatched segment IS the last executed one; its motionMap entry
	// survives pruning (BuildStat only prunes ids below the executing id).
	// 2.9 had no such gap: the state tag traveled inside the motion status
	// and persisted across an empty queue. Guarded by mu.
	lastMotionID int32

	// Motion segment side table: maps serial segment id → {file, lineno}
	// Written by canon at enqueue time; read by BuildStat for halui.program-line.
	motionMap map[int32]motionInfo

	// Interpreter active codes (updated after each execute). These are the
	// ONLY view of interpreter state stat consumers may use — BuildStat must
	// not call into the (non-thread-safe) interpreter while the producer
	// goroutine may be executing it.
	activeGcodes   []int32
	activeMcodes   []int32
	activeSettings []float64
	callLevel      int32 // cached interp.CallLevel(), updated with the codes

	// canonSnap is a value snapshot of *canon.state, republished by
	// updateActiveCodes after every interpreter execute. Canon mutates its
	// state lock-free on the producer goroutine; stat/halui readers must use
	// this snapshot, never t.canon.state.
	canonSnap CanonState

	// Dependencies (injected, mockable for tests)
	motion MotionController
	io     IOController
	ioStat IOStatusReader // optional; used to verify estop state from HAL
	status MotionStatusReader
	interp Interpreter
	errors ErrorPublisher
	logger *slog.Logger

	// Canon state (interpreter callback context)
	canon *Canon

	// Program state
	// Reads an INI value (namespaced to this instance); nil when the task runs
	// without an INI. Used for the [FILTER] section at program-open time.
	iniGet func(section, key string) string

	programFile string
	programOpen bool
	// What the operator opened, when a [FILTER] converter turned it into the
	// G-code programFile names. Equal to programFile when no filter applied.
	sourceFile string
	// A filter conversion in flight. The previously loaded program stays open
	// and executable until the new one is ready, so a filter that fails or
	// times out leaves the controller exactly as it was.
	filtering      bool
	filterProgress int32
	filterCancel   context.CancelFunc
	// This instance's private filtered-output directory
	// (pathres.FilteredInstanceDir). Set by the module at start; empty only
	// in tasks built without one (tests), which fall back to a default.
	filteredDir string
	// Tracks the filter goroutine so shutdown can wait for it: destroying
	// the interpreter or removing filteredDir under a still-running
	// conversion must be impossible by construction, not by lock ordering
	// luck.
	filterWG sync.WaitGroup
	// Bumped when a conversion starts and whenever an in-flight one is
	// superseded: by abort, at shutdown, and by a newer program open. The
	// filter goroutine captures it and refuses to publish a result that a
	// later command has superseded.
	filterGen int64
	// programRes resolves G-code paths against the program directories
	// (PROGRAM_PREFIX + SUBROUTINE_PATH + share).  Built in loadConfig.
	programRes *pathres.Resolver
	previewSeq int32 // increments on changes that invalidate preview
	// bootID identifies THIS run of the task (start timestamp, unix ns).
	// Reported in stat so a client can tell "the task restarted under me"
	// from "nothing changed": every other counter we publish — previewSeq
	// above, heartbeat — restarts at zero with the task and can land back on
	// a value the client already saw, so none of them can carry that signal.
	// Immutable after NewTask, hence no lock.
	bootID int64

	// Sequencer
	interpQueue chan QueuedCmd
	seqDone     chan struct{} // closed when sequencer goroutine exits
	seqAbort    chan struct{} // close to abort sequencer
	// seqInflight is true while the sequencer is processing a dequeued command
	// (from dequeue until its wait/post-wait completes). waitSequencerDrain
	// needs it: an empty queue with ExecDone is also observable in the instant
	// between dequeue and the command's first setExecState. Atomic (not t.mu-
	// guarded) — the writers maintain no cross-field invariant with it, so it
	// need not add two contended t.mu round-trips per dequeued command.
	seqInflight atomic.Bool

	// autoInhibit mirrors the halui auto-inhibit pin, sampled once per monitor
	// tick. Read by the AUTO guards, which run on command goroutines, so it is
	// atomic rather than under t.mu: a stale-by-one-tick value is fine (the
	// interlock it reflects is a physical condition, not a command race) and
	// taking t.mu here would invert the lock order the guards already hold.
	autoInhibit atomic.Bool
	mdiInhibit  atomic.Bool

	// motionDispatched is true once a motion segment has been sent since the
	// last completed drain. waitMotionDone applies its servo-settle skip only
	// when this is set — an empty barrier (back-to-back S/M-code drains with no
	// motion between) then clears in one immediate check instead of paying the
	// ≥50 ms settle floor. Cleared when a drain observes the queue empty.
	motionDispatched atomic.Bool

	// MDI queue — commands queued while interpreter is busy
	mdiQueue     []string
	maxMDIQueued int

	// mdiGen is bumped by executeMDI for every MDI command issued. mdiDoneCmd
	// carries the generation captured at issue; finishMDI runs it forward only
	// if the generation still matches, so a finishMDI left over from a
	// superseded MDI (Abort + new MDI) cannot synch/commit against the newer
	// command's state. Guarded by t.mu; only mutated under cmdMu (executeMDI).
	mdiGen uint64

	// seqFaulted is set by seqFaultExit when the sequencer dies on a hard fault
	// and cleared by StartSequencer/StopSequencer. While set, the interpreter
	// still carries the aborted run's state (stale toolchange/probe/input
	// flags): recoverSeqFault (spawned by seqFaultExit) and the MDI/AUTO front
	// doors run interp on_abort + a sequencer restart before anything else may
	// use the interpreter. Guarded by t.mu.
	seqFaulted bool

	// Sequencer-level pause/step control
	seqPauseCh  chan struct{} // closed to request sequencer pause
	seqResumeCh chan struct{} // closed to wake sequencer from pause

	// M-code handler (M100-M199)
	mcode *mcodeHandler

	// Cached motion status (fallback if read ever fails)
	lastMotionStatus motstat.MotionStatus
	hasMotionStatus  bool

	// Current message list (independent of emcerror /errors drain queue).
	// Guarded by msgMu, NOT mu — a leaf lock, so operatorError is callable
	// from any locking context (guard rejects emit messages under mu).
	msgMu         sync.Mutex
	messageList   []TaskMessage
	nextMessageID uint64

	// Status display fields (echoed to stat, mirrors C++ EMC_TASK_STAT).
	taskCommand  string // last/executing MDI command string ("" when idle)
	inputTimeout int32  // M66 wait: 0=none/cleared, 1=timed out, 2=waiting
	heartbeat    int32  // monotonic liveness counter, bumped each BuildStat

	// Result of the most recently completed M100-M199 handler. The interpreter
	// reads it back into #5399 — see setUserDefinedResult.
	userDefinedResult float64

	// One-entry decode memo for pinMotionState: consecutive naive-CAM flushed
	// segments usually share the same modal tag, so cache the last decode and
	// skip the cgo ActiveModesFromTag round-trip (once per G1 line otherwise).
	// Producer-goroutine-owned like the canon state — no lock.
	pinDecodeTag []byte
	pinDecodeGc  []int32
	pinDecodeMc  []int32
	pinDecodeSt  []float64
}

// motionInfo is the state tag milltask keeps for each motion segment, keyed by
// the serial id that motion echoes back. Motion itself is interpreter-agnostic
// (it only carries the id); milltask maps id → tag to report the source line
// and the active G/M codes of the segment actually executing (not readahead).
type motionInfo struct {
	File     string
	LineNo   int32
	Gcodes   []int32   // active G-codes when the segment was queued (nil = untagged)
	Mcodes   []int32   // active M-codes
	Settings []float64 // active settings (feed, speed, …)
	Tag      []byte    // packed interp state_tag_t for abort-time restore_from_tag
	// TagPinned marks a Tag set explicitly at emission time (naive-CAM merged
	// segments flush during a LATER line's execute, so tagMotionRange must not
	// overwrite their tag with that line's state — see allocSerialPinned).
	TagPinned bool
}

// NewTask creates a new Task with dependencies injected.
func NewTask(motion MotionController, io IOController, status MotionStatusReader, logger *slog.Logger) *Task {
	t := &Task{
		state:          StateEstop,
		mode:           ModeManual,
		interpState:    InterpIdle,
		execState:      ExecDone,
		motion:         motion,
		io:             io,
		status:         status,
		logger:         logger,
		activeSettings: make([]float64, 5), // ACTIVE_SETTINGS
		activeGcodes:   make([]int32, 17),  // ACTIVE_G_CODES
		activeMcodes:   make([]int32, 10),  // ACTIVE_M_CODES
		maxMDIQueued:   10,
		// Optional stop and block delete default ON at startup, matching classic
		// linuxcnc (axis.py defaults both prefs to True; the NML task stat reports
		// them True before any UI toggle).
		optionalStop: true,
		blockDelete:  true,
		mcode:        newMcodeHandler(),
		motionMap:    make(map[int32]motionInfo),
		bootID:       time.Now().UnixNano(),
	}
	t.canon = NewCanon(t)
	t.canonSnap = *t.canon.state
	return t
}

// registerMotion records the G-code location for a motion segment serial id.
// Called by Canon when a motion segment is enqueued.
func (t *Task) registerMotion(id int32, file string, lineno int32) {
	t.mu.Lock()
	t.motionMap[id] = motionInfo{File: file, LineNo: lineno}
	t.mu.Unlock()
}

// lookupMotionLine returns the G-code line number for a motion segment id.
// Returns 0 if not found.
func (t *Task) lookupMotionLine(id int32) int32 {
	t.mu.Lock()
	info, ok := t.motionMap[id]
	t.mu.Unlock()
	if !ok {
		return 0
	}
	return info.LineNo
}

// motionInfoAndPrune returns the state tag for motion segment id and, in the
// SAME lock, prunes entries older than id. BuildStat calls this once per status
// cycle instead of the separate lookupMotionLine (×2) + lookupMotionInfo +
// pruneMotionMap acquisitions it used to make (four t.mu round-trips → one).
func (t *Task) motionInfoAndPrune(id int32) (motionInfo, bool) {
	t.mu.Lock()
	info, ok := t.motionMap[id]
	for k := range t.motionMap {
		if k < id {
			delete(t.motionMap, k)
		}
	}
	t.mu.Unlock()
	return info, ok
}

// tagMotionRange attaches the active-code state tag to every motion segment
// whose serial id is in [startID, endID) — the segments queued while the
// interpreter executed one source line. Codes are captured after that line's
// execute (their modal state), completing the id → state_tag mapping.
func (t *Task) tagMotionRange(startID, endID int32, gcodes, mcodes []int32, settings []float64) {
	if endID <= startID {
		return
	}
	// The packed interp state tag emitted while this line executed (canon
	// state is producer-owned; tagMotionRange runs on the same goroutine).
	tag := t.canon.state.currentTag
	t.mu.Lock()
	for id := startID; id < endID; id++ {
		if info, ok := t.motionMap[id]; ok {
			if !info.TagPinned {
				info.Tag = tag
				info.Gcodes, info.Mcodes, info.Settings = gcodes, mcodes, settings
			} else if info.Gcodes == nil {
				// Pinned tag but no decoded codes (no decoding interp):
				// bracket codes are better than none.
				info.Gcodes, info.Mcodes, info.Settings = gcodes, mcodes, settings
			}
			t.motionMap[id] = info
		}
	}
	t.mu.Unlock()
}

// pinMotionState stamps a motion segment's state tag — and the active
// G-/M-codes and settings decoded FROM that tag — at emission time, marking
// the entry pinned so tagMotionRange won't overwrite it. A naive-CAM merged
// segment flushes while a LATER source line is executing (its id falls inside
// that later line's bracket), but must report and restore the modal state of
// the line that produced its LAST chained point: 2.9 stores the tag per
// chained point and derives status codes from the executing segment's tag
// (Interp::active_modes). The g64 abort test's readahead `G64 P1 Q2` line is
// exactly the case this guards: its SetMotionControlMode flushes the chain,
// and without pinning the merged move would report the never-executed P1/Q2.
func (t *Task) pinMotionState(id int32, tag []byte) {
	if tag == nil {
		return
	}
	var gc, mc []int32
	var st []float64
	if t.interp != nil {
		// Chained segments flush in bursts under one modal state — decode a
		// given tag once and reuse it (failed decodes are memoized too: the
		// result is a pure function of the tag bytes). pinMotionState only
		// runs on the producer goroutine, so the memo needs no lock.
		if t.pinDecodeTag != nil && bytes.Equal(tag, t.pinDecodeTag) {
			gc, mc, st = t.pinDecodeGc, t.pinDecodeMc, t.pinDecodeSt
		} else {
			gc, mc, st, _ = t.interp.ActiveModesFromTag(tag)
			t.pinDecodeTag = append([]byte(nil), tag...)
			t.pinDecodeGc, t.pinDecodeMc, t.pinDecodeSt = gc, mc, st
		}
	}
	t.mu.Lock()
	if info, ok := t.motionMap[id]; ok {
		info.Tag = tag
		info.TagPinned = true
		if gc != nil {
			info.Gcodes, info.Mcodes, info.Settings = gc, mc, st
		}
		t.motionMap[id] = info
	}
	t.mu.Unlock()
}

// SetInterpreter sets the interpreter dependency. Must be called before
// running G-code programs. The interpreter should already have its canon
// callbacks wired via SetCanonCallbacks.
func (t *Task) SetInterpreter(interp Interpreter) {
	t.interp = interp
}

// SetErrorPublisher sets the error publisher for operator messages.
func (t *Task) SetErrorPublisher(ep ErrorPublisher) {
	t.errors = ep
}

// SetIOStatusReader sets the IO status reader for estop state verification.
func (t *Task) SetIOStatusReader(r IOStatusReader) {
	t.ioStat = r
}

// operatorError sends an operator error message to connected UIs.
func (t *Task) operatorError(text string) {
	t.appendMessage(emcerror.ErrorKind_OPERATOR_ERROR, text)
	if t.errors != nil {
		t.errors.OperatorError(text)
	}
	t.logger.Warn("operator error", "msg", text)
}

// updateActiveCodes fetches the interpreter's active G/M codes and settings
// and stores them in the task state for stat reporting. It also republishes
// the canon state snapshot and call level — this is the single point where
// interpreter/canon state becomes visible to stat consumers, so it must be
// called by whoever owns the interpreter after anything that may have changed
// that state (execute, reset).
func (t *Task) updateActiveCodes(interp Interpreter) (gc, mc []int32, st []float64) {
	gc = interp.ActiveGCodes()
	mc = interp.ActiveMCodes()
	st = interp.ActiveSettings()
	cl := int32(interp.CallLevel())
	t.mu.Lock()
	t.activeGcodes = gc
	t.activeMcodes = mc
	t.activeSettings = st
	t.callLevel = cl
	t.canonSnap = *t.canon.state
	t.mu.Unlock()
	return gc, mc, st
}

// setAutoInhibit records the halui auto-inhibit pin state.
func (t *Task) setAutoInhibit(v bool) { t.autoInhibit.Store(v) }

// autoInhibited reports whether AUTO is currently forbidden by the interlock.
func (t *Task) autoInhibited() bool { return t.autoInhibit.Load() }

// programRunning reports whether an AUTO program is mid-run, paused included:
// a paused program resumes into the same cut, so an interlock has to stop it
// too. Takes t.mu and releases it before the caller acts, so the caller can go
// on to take cmdMu without inverting the lock order.
func (t *Task) programRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.interpState == InterpReading ||
		t.interpState == InterpWaiting ||
		t.interpState == InterpPaused
}

// setMDIInhibit records the halui mdi-inhibit pin state.
func (t *Task) setMDIInhibit(v bool) { t.mdiInhibit.Store(v) }

// mdiInhibited reports whether MDI is currently forbidden by the interlock.
func (t *Task) mdiInhibited() bool { return t.mdiInhibit.Load() }
