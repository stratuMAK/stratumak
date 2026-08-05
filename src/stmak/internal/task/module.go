// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"runtime/cgo"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emccmd"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcerror"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcio"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcstat"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/tooltable"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

func init() {
	stmak.RegisterModule("milltask", factory)
}

// Compile-time interface checks.
var _ MotionConfig = (*motctl.MotctlClient)(nil)

func factory(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	logger = logger.With("module", name)
	// milltask is INI-driven throughout (loadConfig reads TRAJ/KINS/JOINT_n/…),
	// so an INI-less launcher (halrun mode, ini == nil) cannot run it.  Reject
	// it here with a clear error instead of nil-dereferencing on the next line.
	if ini == nil {
		return nil, fmt.Errorf("milltask %q: requires an INI file (loaded without one)", name)
	}
	// Use a namespaced view of the INI so this instance reads [name:SECTION]
	// with fallback to [SECTION].  For the default "milltask" instance name
	// where no namespaced sections exist, all lookups fall through to global.
	nsIni := ini.WithNamespace(name)
	m := &milltaskModule{ini: nsIni, logger: logger, name: name}

	// Parse module parameters (given on the "load milltask <name> k=v ..." line):
	//   halui=<prefix>              export halui HAL pins under <prefix>
	//   motion_instance=<name>      motmod instance to drive (default "motmod")
	//   iocontrol_instance=<name>   io instance to drive (default "iocontrol")
	//   tooltable_instance=<name>   tooltable instance (default "tooltable")
	//   persist_instance=<name>     persist instance (default "persistence")
	//   preview_instance=<name>     ngcpreview instance serving this task
	//   mtc_instance=<name>         manualtoolchange instance for this task
	//   pyvcp_instance=<name>       pyvcp panel instance for this task
	//   error_filter=<regexp>       forward a C-module ERROR message to this
	//                               instance's operator list only when its
	//                               emitting component name matches <regexp>;
	//                               unset = forward everything (see below).
	//
	// preview/mtc/pyvcp note: milltask never calls these three itself. It is
	// told their names so a UI client can ask ONE endpoint (GET /info) what
	// this machine is made of, instead of deriving names by convention
	// ("<task>-preview") or hardcoding them ("tooltable") — the guess that made
	// a multi-instance config answer every tool table read with a 404. Naming
	// them here also turns a typo into a config-load failure (Start resolves
	// each one) rather than a client-side 404 discovered much later.
	//
	// error_filter note: the ERROR-forwarding hook fires for EVERY milltask
	// instance, so in a multi-instance config set a per-instance filter (e.g.
	// error_filter=^pnp\. on pnp.task) to stop one instance's "joint N following
	// error" from also surfacing on the others. The HAL tokenizer does NO
	// backslash-escape processing, so the regexp is taken literally — write
	// \. \d \w as-is, no doubling. But INI/ENV substitution runs on the line
	// BEFORE tokenizing, so avoid regex metacharacters that collide with it:
	// a [...] class immediately followed by a word char parses as an
	// [SECTION]KEY INI ref (use (?:a|b) instead), and $NAME/${NAME} parses as
	// an env ref (a trailing $ anchor is fine). Anchored prefixes like ^pnp\.
	// dodge all of this.
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		switch k {
		case "halui":
			m.haluiPrefix = v
		case "motion_instance":
			m.motInstance = v
		case "iocontrol_instance":
			m.ioInstance = v
		case "tooltable_instance":
			m.ttInstance = v
		case "persist_instance":
			m.persistInstance = v
		case "preview_instance":
			m.previewInstance = v
		case "mtc_instance":
			m.mtcInstance = v
		case "pyvcp_instance":
			m.pyvcpInstance = v
		case "error_filter":
			re, err := regexp.Compile(v)
			if err != nil {
				return nil, fmt.Errorf("milltask: invalid error_filter regexp %q: %w", v, err)
			}
			m.errorFilter = re
		}
	}

	// Allow the motion module instance to be selected from the INI so a config
	// can insert a motion interceptor (e.g. motion-logger) in front of motmod
	// without editing the shared HAL that loads milltask. The load arg
	// (motion_instance=) still takes precedence.
	if m.motInstance == "" {
		if v := m.ini.Get("EMCMOT", "MOTION_INSTANCE"); v != "" {
			m.motInstance = v
		}
	}

	// Register C-compatible callback structs so C modules (halui) can
	// call emccmd/emcstat via the standard api_get mechanism.
	reg := apiserver.DefaultRegistry()
	if reg == nil {
		return nil, fmt.Errorf("milltask: no API registry available")
	}
	if err := emccmd.RegisterEmccmdAPI(reg, name, m); err != nil {
		return nil, fmt.Errorf("milltask: emccmd register: %w", err)
	}
	if err := emcstat.RegisterEmcstatAPI(reg, name, m); err != nil {
		return nil, fmt.Errorf("milltask: emcstat register: %w", err)
	}
	m.apiCleanup = func() {
		if ptr, err := reg.GetAPIUntracked("emccmd", name, 0); err == nil {
			emccmd.FreeEmccmdCallbacks(ptr)
		}
		if ptr, err := reg.GetAPIUntracked("emcstat", name, 0); err == nil {
			emcstat.FreeEmcstatCallbacks(ptr)
		}
	}

	// Create halui HAL component if halui=<prefix> module parameter was given.
	// Done in factory so pins exist before HAL wiring commands execute.
	if m.haluiPrefix != "" {
		numJoints := getIntOr(ini, "KINS", "JOINTS", 3)
		numSpindles := getIntOr(ini, "TRAJ", "SPINDLES", 1)
		coord := ini.Get("TRAJ", "COORDINATES")
		axisMask := parseAxisMask(coord)
		mdiCmds := ini.GetAll("HALUI", "MDI_COMMAND")
		hu, err := newHalUI(m.haluiPrefix, numJoints, numSpindles, axisMask, mdiCmds)
		if err != nil {
			return nil, fmt.Errorf("milltask: %w", err)
		}
		m.halui = hu
		logger.Info("halui pins exported", "prefix", m.haluiPrefix)
	}

	// Register WebSocket watches and commands (direct Go path, no C thunk).
	m.registerWatches(name)

	return m, nil
}

// milltaskModule wraps Task to satisfy the stmak.Module lifecycle.
type milltaskModule struct {
	ini               *inifile.IniFile
	name              string
	task              *Task
	logger            *slog.Logger
	inihal            *iniHal
	mc                MotionConfig
	apiCleanup        func()
	poslog            posLogger
	interp            *CInterp
	canonTable        *canonCallbackTable
	mon               *monitor
	stopped           atomic.Bool                // read by ready() from arbitrary goroutines
	haluiPrefix       string                     // if set, export halui pins with this component name
	halui             *halUI                     // halui HAL component (created in factory)
	motInstance       string                     // motion module instance name (default "motmod")
	ioInstance        string                     // io controller instance name (default "iocontrol")
	errorFilter       *regexp.Regexp             // if set, only C-module errors whose component matches are forwarded to operator messages
	ttInstance        string                     // tooltable instance name (default "tooltable")
	persistInstance   string                     // persist instance name (default "persistence")
	previewInstance   string                     // ngcpreview instance, "" = none (reported via GetInfo, never called)
	mtcInstance       string                     // manualtoolchange instance, "" = none (reported via GetInfo, never called)
	pyvcpInstance     string                     // pyvcp panel instance, "" = none (reported via GetInfo, never called)
	iniAccessorHandle cgo.Handle                 // CGo handle for the INI accessor (must be freed)
	ttClient          *tooltable.TooltableClient // tooltable GMI client
	paramIO           *interpParamIOPersist      // persist-backed parameter I/O (default)
	fileParamIO       *interpParamIOFile         // classic .var-file parameter I/O (opt-in)
}

// forwardsErrorFrom reports whether an ERROR-level message from the named C
// module component should be forwarded to this instance's operator message
// list. With no error_filter configured every component is forwarded (legacy
// behaviour); otherwise the component name must match the filter regexp.
func (m *milltaskModule) forwardsErrorFrom(component string) bool {
	return m.errorFilter == nil || m.errorFilter.MatchString(component)
}

func (m *milltaskModule) Start() error {
	reg := apiserver.DefaultRegistry()
	if reg == nil {
		return fmt.Errorf("milltask: no API registry available")
	}

	// Determine motion module instance name from INI (default "motmod").
	motInstance := m.motInstance
	if motInstance == "" {
		motInstance = "motmod"
	}

	// Determine IO controller instance name.
	ioInstance := m.ioInstance
	if ioInstance == "" {
		ioInstance = "iocontrol"
	}

	// Look up registered GMI callbacks.
	motctlCbs, err := reg.GetAPIFor(m.name, "motctl", motInstance, 1)
	if err != nil {
		return fmt.Errorf("milltask: motctl API lookup (%s): %w", motInstance, err)
	}
	motstatCbs, err := reg.GetAPIFor(m.name, "motstat", motInstance, 1)
	if err != nil {
		return fmt.Errorf("milltask: motstat API lookup (%s): %w", motInstance, err)
	}
	emcioCbs, err := reg.GetAPIFor(m.name, "emcio", ioInstance, 1)
	if err != nil {
		return fmt.Errorf("milltask: emcio API lookup (%s): %w", ioInstance, err)
	}

	// Look up tooltable API.
	ttInstance := m.ttInstance
	if ttInstance == "" {
		ttInstance = "tooltable"
	}
	ttCbs, err := reg.GetAPIFor(m.name, "tooltable", ttInstance, 3)
	if err != nil {
		return fmt.Errorf("milltask: tooltable API lookup (%s): %w", ttInstance, err)
	}
	// Keep the RESOLVED name: GetInfo hands clients the instance that actually
	// serves them, which for an unset tooltable_instance= is the default, not "".
	m.ttInstance = ttInstance
	m.ttClient = tooltable.NewTooltableClient(unsafe.Pointer(ttCbs))
	// Publish the client for the canon tool getters NOW: the interpreter init
	// and RS274NGC_STARTUP_CODE below already resolve tools through it (2.9
	// loads the tool table before interp init and startup code, emctaskmain.cc
	// initMain: emcIoInit -> emcTaskPlanInit). Leaving this until
	// registerTools() made startup-code tool lookups (e.g. "G43 H1") fail with
	// "tool not found".
	pkgTTClient = m.ttClient

	// Resolve the peers milltask reports but never calls (GetInfo). This is a
	// config-sanity gate, not a dependency: a name given here must name a
	// module of the RIGHT KIND, checked by api:instance rather than instance
	// alone so that preview_instance=pnp.tt fails instead of quietly matching
	// the tool table. Unset means the feature is absent, which is legal and
	// tells the client not to probe for it.
	//
	// Safe to do in Start: the launcher completes every module's New() — where
	// APIs register — before it starts any of them, so a peer loaded on a LATER
	// line of the HAL file is already registered by now.
	if err := m.checkReportedPeers(reg); err != nil {
		return err
	}

	// Wrap C callback pointers in typed Go clients.
	mc := motctl.NewMotctlClient(unsafe.Pointer(motctlCbs))
	ms := motstat.NewMotstatClient(unsafe.Pointer(motstatCbs))
	ioClient := emcio.NewEmcioClient(unsafe.Pointer(emcioCbs))
	io := &ioAdapter{EmcioClient: ioClient}

	t := NewTask(mc, io, ms, m.logger)
	t.SetIOStatusReader(io)
	// Each instance filters into its own directory: on a multi-instance
	// server, a shared one would let one task's open (or shutdown) destroy
	// the program another task has loaded.
	t.filteredDir = pathres.FilteredInstanceDir(m.name)

	// Validate kinematics/joint/axis INI consistency before loading config.
	if err := m.checkConfig(); err != nil {
		return fmt.Errorf("milltask: %w", err)
	}

	// Load configuration from INI and send to motion controller.
	if err := loadConfig(m.ini, t, mc); err != nil {
		return fmt.Errorf("milltask: %w", err)
	}

	// What an idx means — carousel pocket or synthetic slot — belongs to the
	// tool store, which is told once on its load line. Reading
	// [EMCIO]RANDOM_TOOLCHANGER here as well is how a task and its own store
	// came to disagree on a multi-instance server: the store resolved the
	// global section while the task resolved its namespaced one. GetInfo is
	// answerable before the store's Start, so module start order does not
	// matter here.
	ttInfo, err := m.ttClient.GetInfo()
	if err != nil {
		return fmt.Errorf("milltask: tooltable get_info (%s): %w", ttInstance, err)
	}
	t.randomToolchanger = ttInfo.RandomToolchanger
	// Migration catch: a hand-written HAL file that still loads a bare
	// `load tooltable` under an INI that sets [EMCIO]RANDOM_TOOLCHANGER used
	// to get random semantics from the store's own INI read. The store no
	// longer reads the INI — silence here would flip every idx from carousel
	// pocket to synthetic slot with nothing logged, so say it loudly.
	if v := strings.TrimSpace(m.ini.Get("EMCIO", "RANDOM_TOOLCHANGER")); v != "" {
		iniRandom := v != "0"
		if iniRandom != ttInfo.RandomToolchanger {
			m.logger.Warn("milltask: [EMCIO]RANDOM_TOOLCHANGER is set in the INI but the tool store was loaded without a matching random_toolchanger= parameter; the store's load line wins",
				"ini", v, "store", ttInfo.RandomToolchanger, "tooltable", ttInstance)
		}
	}

	// Create inihal HAL component for runtime INI parameter override.
	ih, err := newIniHal(m.name+".inihal", t.numJoints)
	if err != nil {
		return fmt.Errorf("milltask: %w", err)
	}
	ih.initPins(t)

	m.task = t
	m.inihal = ih
	m.mc = mc

	// Register the mcode_handler GMI provider so cmods can register handlers for
	// user M-codes M100-M199 (IDL: "Provider: milltask.so"). Done here (Start)
	// because the mcodeHandler registry is created with the Task; cmods look it
	// up in their own Start(), which runs after milltask's.
	if reg := apiserver.DefaultRegistry(); reg != nil {
		mcodeCleanup, err := registerMcodeHandlerProvider(reg, m.name, m.task.mcode)
		if err != nil {
			return fmt.Errorf("milltask: mcode_handler register: %w", err)
		}
		prevCleanup := m.apiCleanup
		m.apiCleanup = func() {
			if prevCleanup != nil {
				prevCleanup()
			}
			mcodeCleanup()
		}
	}

	// Wire the error publisher so operator messages reach UI clients.
	// EnsureDrainStarted creates the ring+drain if the C milltask didn't.
	if drain := emcerror.EnsureDrainStarted(m.name); drain != nil {
		t.SetErrorPublisher(&drainErrorPublisher{drain: drain})
	}

	// Forward ERROR-level log messages from C modules (motmod, per-joint
	// homemod instances, io, etc.) to the operator message list.
	//
	// stmak.NotifyLogError fans out to EVERY registered hook globally, so in a
	// multi-instance config (e.g. coat.task + pnp.task) an unfiltered hook makes
	// each task report every OTHER instance's errors — "pnp.mot: joint N
	// following error" would also surface on coat.task. Rather than guess which
	// components belong to this task, routing is left to the operator via the
	// optional "error_filter=<regexp>" module parameter, matched against the
	// emitting component name (e.g. error_filter=^pnp\. on pnp.task). Unset =
	// forward everything (legacy behaviour).
	// Register the hook and chain its unregister into apiCleanup so Destroy
	// removes it. The registry is a process-global with no owner; the log ring's
	// final flush runs after Go modules are destroyed, so a hook left registered
	// would forward a late error into this freed task (operatorError on a stopped
	// task). Unregistering at Destroy closes that window.
	unregisterLogHook := stmak.OnLogError(func(component, msg string) {
		if !m.forwardsErrorFrom(component) {
			return
		}
		t.operatorError(msg)
	})
	prevCleanupLog := m.apiCleanup
	m.apiCleanup = func() {
		unregisterLogHook()
		if prevCleanupLog != nil {
			prevCleanupLog()
		}
	}

	// Create and configure the G-code interpreter.
	if err := m.initInterpreter(); err != nil {
		return fmt.Errorf("milltask: %w", err)
	}

	// Register the interp_ext GMI provider now that the interpreter exists, so
	// cmods (stdglue, custom O-word / remap handlers) can look it up via
	// interp_ext_api_get in their own Start(), which runs after milltask's.
	if reg := apiserver.DefaultRegistry(); reg != nil {
		extCleanup, err := registerInterpExtProvider(reg, m.name, m.interp.RawInterp())
		if err != nil {
			return fmt.Errorf("milltask: interp_ext register: %w", err)
		}
		prevCleanup := m.apiCleanup
		m.apiCleanup = func() {
			if prevCleanup != nil {
				prevCleanup()
			}
			extCleanup()
		}
	}

	// Start the sequencer goroutine (executes queued motion commands).
	t.StartSequencer()

	// Push the default blending mode to the TP, mirroring the C canon's
	// INIT_CANON tail at task init (SET_TERM_COND(BLEND, 0.0254mm)). The
	// boot-time interpreter init ran before the sequencer existed, so its
	// InitCanon could not emit this itself.
	t.pushDefaultTermCond()

	// Run [RS274NGC]RS274NGC_STARTUP_CODE once, now that the interpreter's canon
	// callbacks have a running sequencer to feed. Mirrors C emcTaskPlanInit
	// (emctask.cc): execute the string on the interp, draining any
	// EXECUTE_FINISH continuations. Runs independent of machine state, exactly
	// as classic task does at init.
	t.runStartupCode()

	// Start the monitor: a safety loop (estop, errors, soft limits, jog
	// watchdog, inihal) plus a separate halui pin-dispatch loop, so a
	// blocking halui command can never stall the safety checks.
	m.mon = newMonitor(t, mc, ih, io)
	m.mon.halui = m.halui
	m.mon.start()

	// Register tools API (needs INI for tool table path).
	m.registerTools()

	// Load default program if configured.
	m.loadDefaultProgram()

	m.logger.Info("milltask started")
	return nil
}

func (m *milltaskModule) Stop() {
	m.stopped.Store(true)
	// Fire abort signals first so any command blocked in a wait or in
	// EnqueueCmd backpressure unwinds — the monitor and sequencer shutdowns
	// below wait for goroutines that might otherwise never exit.
	if m.task != nil {
		m.task.signalAbort()
	}
	if m.mon != nil {
		m.mon.stop()
	}
	m.poslog.stopLogger()
	if m.task != nil {
		m.task.StopSequencer()
		if m.task.mcode != nil {
			m.task.mcode.Stop()
		}
	}
	if m.task != nil {
		// Stop any filter still converting and wait for its goroutine: after
		// this point the interpreter gets destroyed and the output directory
		// removed, and a conversion still holding either would be a
		// use-after-free, not a race to win.
		m.task.cancelFiltering()
		m.task.filterWG.Wait()
		// Drop only THIS instance's output directory — another milltask in
		// the same process may still be serving its own filtered program.
		// Best effort: the process is going away either way, and a directory
		// that outlives it is a stale temp dir, not a fault to report.
		_ = os.RemoveAll(m.task.filteredDirOrDefault())
		// The shared parent goes with the last instance out: Remove refuses
		// a non-empty directory, which is exactly the point.
		_ = os.Remove(pathres.FilteredDir())
	}
	m.logger.Info("milltask stopping")
}

func (m *milltaskModule) Destroy() {
	if m.interp != nil {
		m.interp.Destroy()
		m.interp = nil
	}
	if m.paramIO != nil {
		m.paramIO.destroy()
		m.paramIO = nil
	}
	if m.fileParamIO != nil {
		m.fileParamIO.destroy()
		m.fileParamIO = nil
	}
	if m.iniAccessorHandle != 0 {
		FreeIniAccessor(m.iniAccessorHandle)
		m.iniAccessorHandle = 0
	}
	if m.canonTable != nil {
		m.canonTable.release()
		m.canonTable = nil
	}
	if m.inihal != nil {
		m.inihal.exit()
	}
	if m.mon != nil && m.mon.halui != nil {
		m.mon.halui.exit()
	}
	if m.apiCleanup != nil {
		m.apiCleanup()
	}
	m.logger.Info("milltask destroyed")
}

// initInterpreter creates and configures the G-code interpreter.
func (m *milltaskModule) initInterpreter() error {
	interp, err := NewCInterp()
	if err != nil {
		return fmt.Errorf("creating interpreter: %w", err)
	}

	// Set up canon callbacks so interpreter actions flow to the sequencer.
	ct := newCanonCallbackTable(m.task.canon)
	interp.SetCanonCallbacks(ct.ptr())

	// Load INI configuration into interpreter via accessor callbacks.
	// This replaces interp.IniLoad(filename) — the accessor provides
	// namespace-resolved values from the Go INI parser, enabling
	// multi-instance support without the interpreter opening files.
	accHandle, err := interp.IniLoadAccessor(m.ini)
	if err != nil {
		interp.Destroy()
		ct.release()
		return fmt.Errorf("interpreter ini_load_accessor: %w", err)
	}
	m.iniAccessorHandle = accHandle

	// Ensure canon knows the parameter file name so the interpreter can
	// load it during Init(). IniLoad should set this via the callback,
	// but we also set it explicitly as a safety net.
	if pf := m.ini.Get("RS274NGC", "PARAMETER_FILE"); pf != "" {
		m.task.canon.SetParameterFileName(pf)
	}

	// Mark interpreter as running in task mode (not preview mode).
	// This enables save_parameters to actually write the var file.
	interp.SetTaskMode(1)

	// In task mode, M99 in the main program loops the program back to the
	// start (an endless program), rather than ending like a standalone
	// interpreter run. Classic task does this in emctask.cc (set_loop_on_main_m99).
	interp.SetLoopOnMainM99(true)

	// Set up parameter I/O. Default is the persistence backend. A config may
	// opt into the classic .var-file backend with
	// [RS274NGC]PARAMETER_FILE_MODE=file, which reads the PARAMETER_FILE at
	// Init() (and writes it on save) exactly like classic linuxcnc — so G5x/G92
	// offsets and other parameters are restored at startup with no commands.
	destroyParamIO := func() {
		if m.paramIO != nil {
			m.paramIO.destroy()
			m.paramIO = nil
		}
		if m.fileParamIO != nil {
			m.fileParamIO.destroy()
			m.fileParamIO = nil
		}
	}
	if strings.EqualFold(strings.TrimSpace(m.ini.Get("RS274NGC", "PARAMETER_FILE_MODE")), "file") {
		pf := m.ini.Get("RS274NGC", "PARAMETER_FILE")
		if pf == "" {
			interp.Destroy()
			ct.release()
			return fmt.Errorf("PARAMETER_FILE_MODE=file requires [RS274NGC]PARAMETER_FILE")
		}
		m.fileParamIO = newInterpParamIOFile(pf)
		m.fileParamIO.install(interp)
	} else {
		persistInstance := m.persistInstance
		if persistInstance == "" {
			persistInstance = "persistence"
		}
		reg := apiserver.DefaultRegistry()
		persistCbs, err := reg.GetAPIFor(m.name, "persist", persistInstance, 2)
		if err != nil {
			interp.Destroy()
			ct.release()
			return fmt.Errorf("interpreter: persist API lookup (%s): %w", persistInstance, err)
		}
		m.paramIO = newInterpParamIOPersist(persistCbs)
		m.paramIO.install(interp)
	}

	// Initialize interpreter state.
	if err := interp.Init(); err != nil {
		interp.Destroy()
		ct.release()
		destroyParamIO()
		return fmt.Errorf("interpreter init: %w", err)
	}

	m.interp = interp
	m.canonTable = ct
	m.task.SetInterpreter(interp)

	// Register M-code trampoline for all M100-M199 slots so the interpreter
	// calls back into Go when user-defined M-codes are encountered.
	interp.RegisterAllMcodeSlots()

	// Synch interpreter state with motion and populate active G/M codes.
	// The C milltask calls emcTaskPlanSynch() at startup which does interp.synch(),
	// then the stat update calls active_g_codes/m_codes/settings.
	if err := interp.Synch(); err != nil {
		m.logger.Warn("interpreter initial synch failed (no motion?)", "err", err)
	}
	m.task.updateActiveCodes(interp)

	m.logger.Info("interpreter initialized")
	return nil
}

func getIntOr(ini *inifile.IniFile, section, key string, def int) int {
	s := ini.Get(section, key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// ioAdapter wraps the generated EmcioClient to satisfy IOController interface.
type ioAdapter struct {
	*emcio.EmcioClient
}

// GetCmdStatus returns the IO command status (1=DONE, 2=EXEC, 3=ERROR).
func (a *ioAdapter) GetCmdStatus() (int32, error) {
	st, err := a.EmcioClient.GetStatus()
	if err != nil {
		return 0, err
	}
	return int32(st.Status), nil
}

// GetIOFullStatus returns the full IO status for the monitor.
func (a *ioAdapter) GetIOFullStatus() (IOFullStatus, error) {
	st, err := a.EmcioClient.GetStatus()
	if err != nil {
		return IOFullStatus{}, err
	}
	return IOFullStatus{
		Estop:  st.Estop,
		Status: int32(st.Status),
		Reason: st.Reason,
	}, nil
}

// GetToolInSpindle returns the current tool number loaded in the spindle.
func (a *ioAdapter) GetToolInSpindle() (int32, error) {
	st, err := a.EmcioClient.GetStatus()
	if err != nil {
		return 0, err
	}
	return st.Tool.ToolInSpindle, nil
}

// GetPocketPrepped returns the pocket number prepared for next tool change.
func (a *ioAdapter) GetPocketPrepped() (int32, error) {
	st, err := a.EmcioClient.GetStatus()
	if err != nil {
		return 0, err
	}
	return st.Tool.PocketPrepped, nil
}

// GetToolStatus returns all three tool fields from a single status read.
func (a *ioAdapter) GetToolStatus() (int32, int32, int32, error) {
	st, err := a.EmcioClient.GetStatus()
	if err != nil {
		return 0, 0, 0, err
	}
	return st.Tool.ToolInSpindle, st.Tool.PocketPrepped, st.Tool.ToolFromPocket, nil
}

// drainErrorPublisher implements ErrorPublisher by writing to the emcerror drain.
// The message list append is handled by the caller (operatorError, etc.).
type drainErrorPublisher struct {
	drain *emcerror.PublishErrorDrain
}

func (p *drainErrorPublisher) OperatorError(text string) {
	p.drain.PublishError(emcerror.ErrorKind_OPERATOR_ERROR, text)
}

func (p *drainErrorPublisher) OperatorText(text string) {
	p.drain.PublishError(emcerror.ErrorKind_OPERATOR_TEXT, text)
}

func (p *drainErrorPublisher) OperatorDisplay(text string) {
	p.drain.PublishError(emcerror.ErrorKind_OPERATOR_DISPLAY, text)
}

// loadDefaultProgram opens the program specified by [DISPLAY]OPEN_FILE
// at task startup so every UI client sees the same initial file — including
// the clients that outlive a server restart, which adopt whatever the task has
// open and never push a program of their own.
//
// Resolution is left entirely to ProgramOpen (pathres, rooted at PROGRAM_PREFIX
// and friends). An os.Stat pre-check used to run first, and because it saw the
// raw INI string it measured the value against the server's *working
// directory*: a name that ProgramOpen resolves fine ("part.ngc" under
// PROGRAM_PREFIX) was rejected before it ever got there, and an identical
// config loaded or skipped the program depending on where the server happened
// to be started from.
func (m *milltaskModule) loadDefaultProgram() {
	file := m.ini.Get("DISPLAY", "OPEN_FILE")
	if file == "" {
		return
	}
	if err := m.task.ProgramOpen(file); err != nil {
		// ProgramOpen has already logged the resolver detail and put a short
		// reason on the operator channel; this names the source of the request,
		// which the operator message deliberately does not.
		m.logger.Warn("[DISPLAY]OPEN_FILE not loaded", "file", file, "error", err)
		return
	}
	m.logger.Info("loaded default program", "file", file)
}

// checkConfig validates kinematics/joint/axis INI consistency.
// This is the consumer-side validation for settings that milltask reads
// from [KINS], [TRAJ], [JOINT_*], and [AXIS_*] sections.
func (m *milltaskModule) checkConfig() error {
	result, err := runConfigCheck(m.ini)
	if err != nil {
		return fmt.Errorf("config check: %w", err)
	}
	if w := result.formatWarnings(); w != "" {
		m.logger.Warn(w)
	}
	if result.hasErrors() {
		return fmt.Errorf("config check failed:\n%s", result.formatErrors())
	}
	return nil
}
