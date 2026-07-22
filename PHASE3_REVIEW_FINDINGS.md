# Phase 3 Review Findings — supervision & startup tail

Tier-2 adversarial review of the four remaining Phase-3 modules (launcher/daemon were
done under Tier-1 hotspot #4):

- `pkg/inifile` — INI parser (**highest risk**: every customer INI must load identically)
- `internal/pkgreg` — packages.conf registry + imports_generated.go codegen (build-time)
- `cmd/gomc-server` — process entry point + flag parsing (thin wrapper over launcher)
- `internal/config` — compile-time ldflags path vars

Method: primary read-through + oracle comparison against the LinuxCNC 2.9 C parser
(`~/source/linuxcnc-2.9/src/libnml/inifile/inifile.cc`) for `inifile`; an independent
adversarial AI pass (refute-first) over the other three. All CONFIRMED live findings fixed
this pass; regression tests added for each.

Verified: `go test -count=1` + `-race` green (inifile, pkgreg), `go vet` clean, `gofmt`
clean, `golangci-lint` 0 issues, `modcompile` builds, downstream inifile consumers
(task, haljson, halfile, inirest) green.

---

## pkg/inifile — 2 CONFIRMED parity divergences fixed, 1 documented

The 2.9 C oracle (`IniFile::Find` / `SkipWhite` / `AfterEqual`): a comment is recognised
**only** when `#`/`;` is the *first non-whitespace character* of a line; a value is
everything after `=` (trailing whitespace trimmed); a trailing `\` continues the line; and
numeric conversion uses `strtod` (stops at the first non-numeric byte).

### I-1 (parity, HIGH — FIXED): backslash line-continuation was not implemented
2.9 joins a physical line ending in `\` with the following line(s), up to
`MAX_EXTEND_LINES` (20). gomc's parser treated every physical line separately, so a
continued value was truncated at the `\` and the continuation lines were dropped as
"unknown lines".

- **Impact:** shipped configs use this — **158 non-comment lines across the config tree**,
  notably the `[DISPLAY]APP = sim_pin \` multi-arg launcher pattern (`ini_hal_demo.ini`,
  the `ja_tests`, moveoff, qtdragon, …). gomc read `APP` as `sim_pin \` and silently lost
  every argument.
- **Fix (`parser.go` `parseFile`):** a trailing `\` (tested on the untrimmed line, so
  `\`+whitespace is *not* a continuation — matching the C parser's last-byte test) joins
  the next line(s) with the backslash removed and no separator inserted; leading whitespace
  on continuation lines is preserved; >20 continuations is an error (`ERR_OVER_EXTENDED`).
  `SourceLine` now reports the logical line's first physical line.
- **Tests:** `TestBackslashLineContinuation`, `TestBackslashContinuationLimit`.

### I-2 (parity, HIGH — FIXED): inline `;` was stripped as a comment, truncating MDI commands
`stripInlineComment` truncated a value at the first `;` (and at a whitespace-preceded `#`).
Its doc claimed *"Per LinuxCNC convention: a ';' anywhere ... starts an inline comment"* —
**factually false**: the 2.9 C parser never strips `;` inline.

- **Impact:** `;` is legitimate **data** — `MDI_COMMAND = G0 Z25;X0 Y0;Z0` chains G-code
  moves (standard user-button feature in Touchy/Axis/QtDragon/HALUI). gomc silently
  truncated it to `G0 Z25`. **36 shipped occurrences.** A grep of the whole config tree
  found **zero** configs using `;` as an inline comment, so the "feature" was pure downside.
- **Why `#` stripping was kept:** gomc converts INI numbers with `strconv`/`fmt.Sscanf`.
  `parseFloat` (`config.go`) uses `Sscanf("%f")`, which is already `strtod`-lenient, so a
  whitespace-preceded `#` on a numeric value (`MAX_VELOCITY = 5 # note`) is handled either
  way. Keeping the narrow, whitespace-preceded `#` strip reproduces 2.9's effective numeric
  tolerance with the least risk; only `;` — the one that loses string data and that no
  config uses as a comment — was removed.
- **Fix (`parser.go` `stripInlineComment`):** `;` is no longer treated as a comment; only a
  whitespace-preceded `#` (`" #"` / `"\t#"`) is. Full-line `#`/`;` comments are unaffected
  (handled separately at line-classification). Doc rewritten to the true C-parser semantics.
- **Tests:** `TestSemicolonIsDataNotComment` (MDI_COMMAND round-trip), updated
  `TestComments` / `TestInlineCommentOrdering`.
- **RULING — CONFIRMED KEEP-AS-IS (user, 2026-07-22).** The fix stands as landed: `;` is
  data, the narrow whitespace-`#` strip stays. Full 2.9 parity (dropping the `#` strip too)
  would additionally require `strtod`-lenient conversion at every INI→number site
  (centralised in `getFloatOr`/`getIntOr` + ~11 direct `strconv` sites) and was judged not
  worth the risk for behaviour the `#`-strip already reproduces. **I-2 is closed.**

### I-3 (parity, LOW — DOCUMENTED, not fixed)
An `#INCLUDE`d file that *continues* a section without repeating its `[HEADER]` lands its
keys in an anonymous section under gomc's structural parse, whereas 2.9's textual include
expansion (`handle_includes`, mirrored by gomc's own `WriteExpanded`) would concatenate
them into the current section. Niche (includes normally carry their own headers); the fix
has its own reordering risk. Left as a known LOW divergence.

---

## internal/pkgreg — 2 build-time findings fixed

### F1 (silent-failure, MED — FIXED): typo'd TYPE silently dropped a module
`ReadConfIn`/`ReadFile` accepted any first field as `EntryType(fields[0])`, but
`GenerateImports` only emits `gmi`/`gomod`. A hand-edited `gomd internal/foo` (or a line
missing its import path) parsed fine, built green, and silently omitted the blank import —
so the module's `init()`/`loadrt` registration never ran and it vanished at runtime.
- **Fix:** `ReadConfIn` now returns an error (with `file:line`) on an unknown type or a
  `<2`-field entry — a loud build failure instead of a silent runtime gap, matching the
  gmicompile fail-fast-guard philosophy. Verified the real `packages.conf` (13 `gomod`
  entries) still parses. Added `isValidType`.
- **Tests:** `pkgreg_test.go` — `TestReadConfIn_ValidAndMarkers`,
  `_UnknownTypeIsError`, `_MalformedLineIsError`.

### F2 (build-break, LOW — FIXED): discovery counted `_test.go` files
`hasGoFiles`/`hasInitFunc` matched `*_test.go`, so an `external/<mod>/` dir containing only
test files would be discovered as a `gomod` entry → blank import → hard `go build` failure
("no non-test Go files"). Now both skip `_test.go`.

### F3 (dead code, LOW — REMOVED 2026-07-22)
`ReadFile`, `(*Registry).WriteFile` and `(*Registry).Remove` had **no callers anywhere**
(`cmd/modcompile` uses only `ReadConfIn` / `Add` / `GenerateImports` / `Discover*` /
`ParseBuildFlags`), and `WriteFile`'s round-trip was **lossy**: it dropped the
`@GOMOD:TAG@` build-flag markers and every comment that `ReadConfIn` exists to interpret.
Keeping a lossy writer next to a marker-aware reader is a live trap — the first caller who
wires the two together silently strips every conditional-build marker from `packages.conf`,
i.e. drops optional modules from the build with a green compile. Deleted (the package is
`internal/`, so no out-of-tree importer can exist; same disposition as pkg/hal H-2).

### hasInitFunc regex (LOW — reviewed, no change)
The pattern is already line-anchored (`(?m)^func init\(\)`), so a mention in a comment or
an ordinary string cannot match; only a raw string literal containing a line that itself
begins with `func init()` could, in gofmt'd *generated* code. Not worth a `go/parser`
dependency — which would additionally have to decide what a syntactically invalid file
means for discovery. Rationale recorded at the regex.

---

## cmd/gomc-server — clean; 2 LOW notes (F5 fixed 2026-07-22, F4 left as-is)
Thin wrapper over `internal/launcher` (Tier-1-reviewed). Flag/arg handling is correct;
`-H` validation short-circuits safely; `-d` out-of-range is rejected by `SetDebug`;
`runtime.LockOSThread()` in `init()` is correct and documented (Boost.Python thread-state).
- **F4 (LOW):** `RtapiInitializeApp()` runs before flag parsing, so `-h` prints the POSIX
  RT note first — cosmetic (the C init does *not* hard-fail unprivileged; falls back to
  SCHED_OTHER). Left as-is (moving it risks `mlockall` ordering).
- **F5 (FIXED 2026-07-22):** a daemon-mode error exit left the pidfile the parent wrote,
  and nothing checked liveness, so the next start silently overwrote a file that still
  looked valid. Both halves closed: `main.go` now `defer`s `RemovePidFile` right after
  `Daemonize` returns in the child, so EVERY exit path drops it (it used to run only after
  a clean `Run()`, and never in `-f` halrun mode); and the cleanup contract that the review
  assigned to `internal/daemon` is now implemented there — see the daemon section below.

## internal/daemon — pidfile ownership + a slog aliasing bug (2026-07-22)
Reviewed under Tier-1 hotspot #4 in 2026-07-20 with one note ("parent+child both write the
pidfile, unlocked — same PID, low severity"); revisited here to close `U` (the package had
**no test file at all**) and F5's cleanup contract.
- **D-1 (double writer, FIXED):** parent and child both wrote the pidfile. Same value, so
  not a corruption bug — but two writers open a window where a child that fails and removes
  the file has it recreated behind its back by the parent's later write, leaving a pidfile
  that names a dead process. The **parent is now the sole writer** (that is what guarantees
  the file exists by the time the parent exits, for a supervisor reading it right after the
  fork); the child just clears the sentinel and returns.
- **D-2 (no liveness check, FIXED):** starting a second daemon over a live pidfile silently
  overwrote the record of the first — orphaning it (nothing could stop it afterwards) while
  two servers fight over the same HAL shm and REST port. `Daemonize` now refuses with
  `ErrAlreadyRunning` when the pidfile names a live process; a stale one (missing,
  malformed, or a dead/reaped PID) is overwritten as before. Liveness is signal-0, and
  **EPERM counts as alive** so a root-owned daemon does not look dead to an unprivileged
  caller.
- **D-3 (foreign-pidfile deletion, FIXED):** `RemovePidFile` unconditionally removed the
  path. After a crash a replacement instance may already own it, and deleting a live
  server's pidfile leaves it unsupervised. It now removes only when the file records this
  process or is stale.
- **D-4 (slog aliasing, FIXED — real bug):** `SyslogHandler.WithAttrs` did
  `append(h.attrs, attrs...)`, reusing the parent's spare capacity. Two handlers derived
  from the *same* parent — the ordinary `slog.With` pattern — then shared one backing array
  and the second silently overwrote the first one's attrs (mutation-verified: the test
  reports both records logging `who=beta`). Now copies. `WithGroup` had the twin defect in
  the other direction: it recorded `groups` and `Handle` never used them, so attrs from
  different groups collided under bare keys — keys are now dotted-qualified. Handler attrs
  also now precede record attrs, matching every stdlib handler.
- **Tests (new, 0 → 2 files):** `daemon_test.go` (pidfile read/write round-trip and its
  malformed/empty/negative rejections, `processAlive` self/reaped/EPERM arms, the
  already-running refusal, the child-does-not-rewrite path, and all four `RemovePidFile`
  cases) and `syslog_test.go` (severity routing, live `Leveler`, attr rendering/ordering,
  the aliasing regression, group qualification) — the latter against a `syslogWriter`
  interface introduced so the handler is testable without a syslog daemon.

## internal/config — 1 CONFIRMED dead injection (2026-07-22)
Pure ldflags-injected string vars, no logic — but the *injection* is the risk surface, and
it was untested: `paths_test.go` only asserted that 15 of the 24 vars default to empty.
- **C-1 (dead `-X`, FIXED):** `go build -ldflags -X pkg.Name=v` **silently does nothing**
  when `Name` does not exist in `pkg` — no warning, no error. The Submakefile injected
  `-X '$(GOMC_LDFLAGS_PKG).DefaultNmlFile=$(DEFAULT_NMLFILE)'`, and **no Go code has ever
  declared `DefaultNmlFile`** (an NML-era leftover; gomc has no NML). Removed. Nothing read
  it, so there is no behaviour change — the point is the class: a renamed or removed
  variable leaves the build green while the value it was meant to carry is empty at runtime.
- **Tests:** `TestLdflagsInjectionTargetsExist` parses the Submakefile's `-X` flags and the
  `paths.go` declarations and fails on any injection with no target (mutation-verified by
  re-adding the `DefaultNmlFile` line), also checking each `-X` is qualified with
  `$(GOMC_LDFLAGS_PKG)` rather than a literal package path.
  `TestUninjectedVarsAreDocumented` covers the other direction — a declared-but-never-
  injected var is always empty in a real build, legitimate only where the doc comment
  describes a fallback (`Tclsh` → PATH lookup). `TestPathsDefaultValues` is now driven off
  the parsed declarations, so a newly added variable is covered automatically instead of
  drifting out of a hand-maintained list. The parse pass additionally asserts every path var
  is an uninitialised `string` — an initializer or a non-string type would make `-X`
  silently ineffective.

---

## Matrix outcome
`pkg/inifile`, `internal/pkgreg`: L R F U RC ✅ (FP — / not applicable), S ◐ (human sign).
`cmd/gomc-server`, `internal/config`: L R F RC ✅ (no fixes needed / clean), U ◐, S ◐.
**Phase 3 review complete** — all rows reviewed; launcher/daemon done under hotspot #4.
