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
- **RULING TO CONFIRM:** this reverses a behavior an existing test encoded as intended.
  It is a bug fix (data loss vs. 2.9, zero shipped-config regression), taken under the
  "load identically" goal. If strict full 2.9 parity is later wanted (drop the `#` strip
  too), that additionally needs `strtod`-lenient conversion at every INI→number site
  (centralised in `getFloatOr`/`getIntOr` + ~11 direct `strconv` sites) — a larger change
  deferred as not obviously worth the risk.

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

### Not fixed (documented)
- **F3 (dead code, LOW):** `ReadFile`/`WriteFile`/`Remove` have no callers; `WriteFile`'s
  round-trip is lossy (drops `@GOMOD:TAG@` markers and comments). Left in place as a
  coherent API; latent only — flag before wiring `WriteFile` to `ReadConfIn`.
- **hasInitFunc** still uses a raw-byte regex that could match `func init()` inside a
  comment/string in a generated file (unlikely in gofmt'd output) — LOW, not worth a
  go/parser dependency.

---

## cmd/gomc-server — clean; 2 LOW notes (no code change)
Thin wrapper over `internal/launcher` (Tier-1-reviewed). Flag/arg handling is correct;
`-H` validation short-circuits safely; `-d` out-of-range is rejected by `SetDebug`;
`runtime.LockOSThread()` in `init()` is correct and documented (Boost.Python thread-state).
- **F4 (LOW):** `RtapiInitializeApp()` runs before flag parsing, so `-h` prints the POSIX
  RT note first — cosmetic (the C init does *not* hard-fail unprivileged; falls back to
  SCHED_OTHER). Left as-is (moving it risks `mlockall` ordering).
- **F5 (LOW):** a daemon-mode error exit leaves the pidfile the parent wrote; no liveness
  check trips on it (next start overwrites). Cleanup contract belongs to `internal/daemon`.

## internal/config — clean
Pure ldflags-injected string vars, no logic. `paths_test.go` asserts a subset default to
empty (incomplete but not a defect).

---

## Matrix outcome
`pkg/inifile`, `internal/pkgreg`: L R F U RC ✅ (FP — / not applicable), S ◐ (human sign).
`cmd/gomc-server`, `internal/config`: L R F RC ✅ (no fixes needed / clean), U ◐, S ◐.
**Phase 3 review complete** — all rows reviewed; launcher/daemon done under hotspot #4.
