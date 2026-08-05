# Phase 4 — HAL tooling — Review Findings (Tier 2)

**Modules:** `internal/halcmd` + `cmd/halcmd` (HAL command interpreter + CLI,
~5.5k LOC), `internal/halparse` (HAL file parser/executor + templating),
`internal/halfile` (HAL file resolution), `internal/haljson` (net-new XML→HAL
module with a REST/watch surface), `internal/modcompile` + `cmd/modcompile`
(`.comp`→C cmod generator), and Tier-3 `internal/hallib`.

**Method (Tier-2 adversarial):** one independent primary read-through per module
grounded against its 2.9 oracle, each finding then adversarially refuted;
survivors adjudicated here against the actual code + oracle. Verdict tags
`CONFIRMED` / `PLAUSIBLE` / `REFUTED` as in the ADS / Network docs.

**Oracles:** `~/source/linuxcnc-2.9/src/hal/utils/halcmd.c` + `halcmd_main.c`
(halcmd + halparse tokenizer), `scripts/linuxcnc.in:876-935` (halfile resolver),
`src/hal/utils/halcompile.g` (modcompile). `haljson` is net-new (no oracle).

**Shared lens:** halcmd/haljson are reachable over the REST/WS surface (halrest
drives halcmd; haljson has its own rest/watch), so untrusted-wire alloc/panic →
controller death applies. halparse parses config at bring-up (OOM/panic on a
malformed file is a field-tech-facing failure + the planned fuzz target).
modcompile is a build-time codegen multiplier (risk-class 3 — one bad emission
replicates into every comp).

**Headline:** no HIGH wire-reachable crash was found in the REST-reachable
command path (halcmd is defensively written). The most consequential finding is
a cross-cutting **module-unload watch-registration lifecycle gap (HJ-1)** shared
with mqttbridge, plus several **codegen-correctness** fixes in modcompile and a
set of **2.9 tokenizer-parity divergences in halparse deferred for a ruling**
(they change parse semantics across the shipped-config corpus).
**Amended 2026-07-22:** closing the `U` column added two more CONFIRMED findings
that a read-through had not surfaced — **HJ-6** (haljson watch sends full state
twice per subscribe) and **HP-8** (halparse's executor tests never compiled in
any build, so the executor had zero effective coverage) — and exercising the
`internal/hallib` C core through those tests exposed a `thread_lock` deadlock
(see the Tier-3 caveat below).

---

## Tier-3 — `internal/hallib`: cleared by inspection
Its Go surface is a 12-line cgo link shim (`cgo.go`) + two test-only cgo wrappers
(`rtapialloc`, `hallibtest`). The bulk is inherited 2.9 C (`hal_lib.c`,
`uspace_rtapi_lib.c`, `uspace_rtapi_string.c`) whose RT-correctness is owned by
`RT_HARDENING_CHECKLIST.md`, not this review. No Phase-4 Go work needed.

**Caveat added 2026-07-22 — "cleared" means the *Go* surface, and inspection of
the C core did not substitute for exercising it.** Writing the `U`-closure tests
for `internal/halcmd` drove real HAL threads through create/start/stop/delete for
the first time in a unit test and immediately hit a **deadlock in that C core**:
`thread_lock` leaked locked on one of the cooperative task-exit paths, hanging
`delthread` on every non-RT-privileged deployment (fixed `3f7eec6cce`, tracked in
`RT_HARDENING_CHECKLIST.md`). Nothing about the Phase-4 Go review was wrong — the
bug is outside its scope — but it is a concrete reminder that "inherited C,
cleared by inspection" is a scoping decision, not evidence of correctness.

---

## FIXED this pass

### HJ-1 — Watch registration survives module unload → stale-serve + registry leak (UAF-class)
`internal/apiserver/ws_handler.go`, `internal/launcher/unload.go`,
`internal/haljson/module.go`. **CONFIRMED.** `[FIXED — bbc989c866]`

haljson (like mqttbridge) registers a `WatchAPI` whose Factory/Watch closures
capture the module's HAL pins. `WatchRegistry` had `Register/Get/All` but **no
unregister**, and both unload paths (`unloadCModule`/`unloadGoModule`) called
only `Registry.UnregisterByInstance` (the REST registry) before `Destroy()`. So
after a runtime REST unload (supported production path, L-3), `Destroy →
comp.Exit()` freed the pins while the `WatchAPI` stayed live in
`DefaultWatchRegistry`: a later WS `subscribe` resolved it and its tick read
freed/recycled HAL memory, and the entry leaked. (The hard-crash is softened in
practice — the HAL shmem arena is never unmapped and the push loops now
`recover()` per the network review — but serving stale values from an unloaded
module + an unbounded registry leak is a real defect.) **Fix:** new
`WatchRegistry.UnregisterByInstance`; both unload paths call a shared
`unregisterModuleAPIs()` clearing REST + watch registries before `Destroy`.
Covers haljson **and** mqttbridge. Regression test.

### HJ-3 / HJ-4 — haljson config bounds
`internal/haljson/pins.go`, `module.go`. **PLAUSIBLE (config-controlled).**
`[FIXED — bbc989c866]` Array `size` had a lower bound (`>=1`) but no upper bound
(absurd value → huge slice + runaway `hal_pin_new` at load → OOM); now capped at
`maxArraySize` (100k). An oversized `rate=` arg could overflow `time.Duration`
into a degenerate/negative hot-spin interval; clamped to `maxRateMS` (1h).

### HJ-6 — haljson watch sends the full state twice on every subscribe
`internal/haljson/watch.go`. **CONFIRMED (found 2026-07-22 while closing `U`).**
`[FIXED — c264a86db8]` The per-connection watch state pre-set every shadow to an
impossible value "to force first full send", and the first tick separately
returned `root.buildJSON()` without priming those shadows. So the tick *after*
the structured snapshot found every pin differing from its shadow and re-sent the
lot as a flat delta — **two full sends per subscriber, on every subscribe**. Not
a correctness bug for a client (the second message is redundant, not wrong), but
it doubles subscribe cost on a panel with hundreds of pins and defeats the point
of the shadow mechanism. The first tick now primes the shadows *before*
`buildJSON`; that order is deliberate — if a pin moves between the two reads the
shadow holds the older value and the next poll re-sends it (redundant but
correct), whereas priming afterwards would leave the shadow ahead of what was
actually sent and drop the update. Regression test asserts the second tick is
suppressed.

### HP-8 — halparse's executor tests never ran in any build
`internal/halparse/executor_test.go`, `link_test.go`,
`executor_integration_test.go`. **CONFIRMED (found 2026-07-22 while closing `U`).**
`[FIXED — c264a86db8]` The executor — the code that applies a parsed HAL file to
HAL — had **zero effective test coverage**, and the tests that appeared to cover
it could not compile. `executor_test.go` is `//go:build !cgo`, but the package's
`link_test.go` blank-imported the cgo-only HAL shim *unconditionally*, so the
nocgo test binary never built and nobody noticed the file had rotted: it
referenced a `LoadToken.Params` field that no longer exists and still expected
`status`/`debug`/`load` to reach the C shim. The cgo-side counterpart
(`executor_integration_test.go`, tagged `cgo && haltest`) was two `t.Skip`
placeholders. Fixed by tagging `link_test.go` `cgo` (so the nocgo suite compiles
and runs again), repairing the rotted expectations, and replacing the
placeholders with a real suite that applies a parsed HAL file to a live
in-process HAL. **Class:** a build-tag combination that no CI job builds silently
converts a test file into dead weight that still *reads* as coverage — worth
checking wherever `!cgo` test files exist.

### HP-5 — halparse template iteration unbounded → OOM
`internal/halparse/template.go`. **PLAUSIBLE.** `[FIXED — 4bac0834fe]` `seq/seq1/
count` `make()` a slice sized by a template/INI-driven count; a huge positive
count (`{{range count (atoi (ini …))}}` with an absurd value) allocated multi-GB
→ OOM at bring-up (a fatal throw, not a recoverable panic); negatives could panic
`make`. The three helpers now return an error above `maxSeqLen` (1e6) or on a
negative count. (The refuted hypothesis — negative `make` crashing via
`errRecover` — is caught by `text/template`'s `safeCall`; the surviving risk is
the positive-count OOM.)

### HC-1 — halcmd completion mid-line-TAB panic (CLI-only)
`cmd/halcmd/completion.go`. **CONFIRMED (LOW, not wire-reachable).**
`[FIXED — cfd7e349cf]` `COMP_POINT` was parsed by folding every rune into the
accumulator (a non-digit → garbage/negative) and the cursor clamped only on the
high side, so a mid-line TAB (`point < i`) panicked in `compLine[i:point]`. 2.9
guards this (`halcmd_main.c`: `if (c<0) c=0` + atoi). Extracted `parseCompPoint`/
`relevantCompLine` (atoi-style stop + both-side clamp) with unit tests.

### HC-3 — halcmd `list comp` glob dialect divergence
`internal/halcmd/halcmd.go`. **CONFIRMED (LOW parity).** `[FIXED — cfd7e349cf]`
`list comp` used Go `path.Match` while every other list type matches with libc
`fnmatch` in the C shims. Added a `halFnmatch` cgo wrapper and routed comp
filtering through it (nocgo → `path.Match` fallback, unreachable there).

### HC-4 — `newthread` at runtime rejected with `cpu=0` (GitHub issue #265)
`cmd/halcmd/main.go`. **CONFIRMED, MED (reported by a user).** `[FIXED — b4c7ffb74a]`
`halcmd newthread <name> <period>` (no cpu) against a running stmakd failed
with `cpu=0 is not an isolated CPU (isolated: [])` on a machine with no isolated
CPUs, while the same `newthread` in a HAL file at startup worked. Root cause: the
`.hal` parser defaults cpu to **-1** (auto-assign / no-affinity), but the CLI left
it nil, and a **nil nullable-`i32?` is flattened to 0 across the cgo REST dispatch**
(`halcmd_cgo.go`: `var cn C.int32_t; if p != nil {…}` → 0 when absent — the C
`int32_t` ABI has no "absent"; the trampoline then hands the impl a non-nil `&0`).
cpu=0 is a non-isolated core → correctly rejected by the RT-thread validator. The
pure-Go bridge preserves nil, but the halcmd API is registered as C callbacks
(`RegisterHalcmdAPI` → `BuildHalcmdCallbacks`), so the cgo path runs. **Fix:** the
CLI defaults cpu to -1 and sends it explicitly (survives the round-trip), matching
the parser. Regression runtest `tests/newthread-runtime` (resident server + runtime
`newthread`; passes `scripts/runtests`). Reproduced + verified live on a
no-isolcpus box.

**Follow-up (broader): nullable-scalar-through-cgo flattening — FIXED at the
generator (user ruling, commit `4e1f2ac387`).** Any `T?` scalar param handled by
the cgo callback FFI lost its "absent" and defaulted to the zero value — the
dispatch zero-filled it and the bridge trampoline always took `&local`, so a Go
provider always saw a non-nil pointer to a fabricated 0 (`addf position` → 0 =
insert-at-front; `newthread fp` → false = non-FP thread; also `newthread cpu`).
The user chose the C-ABI-pointer fix: nullable scalars now transit as pointers
(NULL = absent) across the api.h typedef, `call_X`, dispatch marshaling (malloc +
`_freeList`), and the bridge trampoline (nil-preserving `*T`). Strings excluded
(already `char*`). The one C provider with nullable scalar params
(`gmi_ethercat.c`: `master_index` ×32, `size?`, `mem_size?`) updated to the
pointer signatures; builds 0-warning. CLI band-aids (HC-4 -1 sentinels) reverted
to `nil` — the ABI carries it now, and `tests/newthread-runtime` exercises the
omitted→nil path end-to-end. cgen regression tests added. Validated by a full
runtests round (all green, regenerated ABI corpus-wide). The HC-4 CLI note above
records the original narrow fix; the generator fix supersedes it.

### HF-2 / HF-5 — halfile resolver parity
`internal/halfile/resolve.go`. **CONFIRMED / PLAUSIBLE.** `[FIXED — 45b8a58f69]`
HF-2: `os.Stat` succeeds on directories, so a directory that name-matched a HAL
file was returned as "resolved" then failed confusingly in the parser; 2.9
rejects it (`[ -d ] && foundmsg=""`) — added `isRegularFile` at all three sites.
HF-5: 2.9 tilde-expands HALFILE (`-tildeexpand`); stratuMAK passed values raw — added
`expandTilde` for a leading `~`/`~/`. The nil-INI deref class bug was verified
**ABSENT** (every `ini` touch guarded).

### MC-1 / MC-2 / MC-3 / MC-5 / MC-7 — modcompile codegen correctness
`internal/modcompile/cgen/cgen.go`, `cmd/modcompile/main.go`.
**CONFIRMED.** `[FIXED — de56bf2b47]` (risk-class-3 multiplier — verified against
halcompile.g)
- **MC-1 (MED):** array *param* defaults were silently dropped (scalar path
  guarded `ArraySize==0`, array loop emitted none) → `param rw float scale[8] =
  1.0;` left memset-0. Now emits `inst->hal->NAME[j] = <def>` in the loop.
- **MC-2 (MED):** New() `err:` path freed `inst` but not the `option data` block
  (calloc'd before extra_setup) while `inst_destroy` does → leak on every failed
  load. `err:` now frees `_data`.
- **MC-3 (LOW-MED):** a string modparam default was re-wrapped in quotes verbatim
  after the scanner unescaped it (`"c:\\ttyS0"` → tab; embedded quote →
  uncompilable C). Added `cStringLiteral` to re-escape.
- **MC-5 (LOW, real-config fix):** non-default HAL function names were emitted
  with underscores while pins/params (and halcompile) hyphenate — so `function
  read_all;` exported `comp.read_all`, but `configs/sim/axis/moveoff` does
  `addf mv.read-inputs`/`mv.write-outputs`, which could not resolve. Now routed
  through `toHALFmt`; verified end-to-end (offset/moveoff/pcl720/… regenerate to
  hyphenated names and compile clean).
- **MC-7 (LOW):** unknown dashed CLI args were absorbed as "mode" (misleading
  "unknown mode" or clobbering a real mode). Added a known-mode allowlist;
  unknown flags reject, conflicting modes error. (This corrects the previously
  documented "exits 0 silently" claim — that was already outdated.)

cgen regression tests added (the package's first).

---

## FIXED after ruling — halparse tokenizer now matches 2.9 (HP-1..HP-4)

User ruling (2026-07-21): fix HP-1/HP-2, match 2.9 for HP-3/HP-4.
`[FIXED — 7bf02a484e]` Verified against `halcmd.c` strip_comments/replace_vars/
tokenize + `halcmd_main.c` continuation loop.

- **HP-1 (CONFIRMED, MED):** per-line processing now follows 2.9's order
  `strip_comments → replace_vars → tokenize`. New quote-aware `stripComments()`
  removes `#`..EOL (respecting quotes; unterminated quote → error) and runs
  BEFORE substitution, so a `#` in a substituted INI/ENV value no longer
  truncates the line. `tokenizeLine` no longer treats `#` as a comment. Bonus:
  refs inside comments are no longer substituted (stripped first) — this keeps
  HP-2's blast radius small (0 non-comment env refs in the shipped corpus).
- **HP-2 (CONFIRMED, MED):** a missing INI var (2.9 `-5`) or env var (2.9 `-4`)
  now fails the parse loudly instead of silently substituting `""` (production
  adapter) / leaving the literal (test path). `INILookup.Get` gained a
  `found bool` (adapter derives it from `GetAll`; env via `os.LookupEnv`), so
  present-but-empty is still fine.
- **HP-3 (CONFIRMED):** dropped the backslash-escape processing stratuMAK had added —
  `\` is now an ordinary character everywhere (2.9 tokenize).
- **HP-4 (CONFIRMED):** line continuation joins with NO separator (2.9 strips the
  trailing `\` and concatenates), not an inserted space.

**Validated by a full runtests round (all green).** This is a parser-semantics
change: any `[SEC]KEY` that fails to resolve now errors — which is exactly 2.9's
behavior, so such a config was already broken on 2.9.

## FLAGGED — need a ruling before changing (parser semantics / API surface)

### HP-6 / HP-7 — `$(VAR)`/`[SEC](VAR)` syntax unsupported; `getp`/`print` output dropped
`internal/halparse/parser.go`, `executor.go`. **CONFIRMED, LOW.** Additive
parity items; HP-7 changes stdout (some `expected` files were re-baselined to the
no-output behavior) so it needs a check against the test corpus. Low priority.

### HC-2 — halcmd `resolveArgPath` over-eagerly absolutizes dotted args
`cmd/halcmd/main.go`. **CONFIRMED behavior / PLAUSIBLE impact.** A `load` arg
containing `.`/`/` but not `=` is rewritten to a cwd-absolute path, surprising a
positional dotted value (e.g. `3.14`). The CWD-rewrite exists because the CLI and
server have different cwds; narrowing "is this a path?" is genuinely ambiguous
(not a clean one-liner) → decision needed.

### HJ-2 — haljson in-flight request can race `comp.Exit()` (drain contract)
`internal/haljson/module.go`. **PLAUSIBLE, MED.** HJ-1 closes the subscribe-after-
unload window; a request already inside `buildJSON`/a watch tick when `Destroy`
frees pins is the narrower race. Mitigated (HAL shmem stays mapped, push loops
recover), but the durable fix is the module-quiesce/refcount contract shared with
**ADS A5 / pkg-hal H1** — track there.

---

## DOCUMENTED — LOW, no change

- **HF-1:** `LIB:` searches the whole HALLIB_PATH (incl. cwd `.` / `-H` dirs);
  2.9 uses `$HALLIB_DIR` only, so a cwd file can shadow a system lib. Real but
  low (self-controlled config dir); the faithful fix needs the constructor to
  carry the system-lib dir separately → churn out of proportion.
- **HF-3 / HF-4:** nil-INI mode relies on `.` always being in `halibPath` for cwd
  resolution (belt-and-suspenders); existence-check vs 2.9's readability-check
  conflates EACCES with ENOENT (Go has no clean portable readability probe).
- **MC-4:** New() flattens the init errno to `-1`. Intentionally **kept**: `r` is
  declared uninitialized and several goto-err paths (malloc/eventfd/personality-
  bounds after a prior `r=0`) don't set it, so `return r` risks garbage or a
  false 0/success — the defined `-1` is safer.
- **MC-6:** modcompile's substring feature-detection (`user_mainloop`,
  `FUNCTION(`, `RTAPI_MP_ARRAY_*`) false-positives on comments/strings → a build
  error (loud), not a shipped-broken `.so`. Robustness note.
- **HJ-5:** dead `unsafe.Pointer(&roots)` handed to the REST registry (dispatch
  ignores it); swallowed `json.Marshal` error in `buildJSON` (basic-typed maps).

---

## Cleared after scrutiny (high-value negatives)

- **halcmd wire path — no HIGH:** `watch.go` C-returns free correctly (the
  historical `watch_items` leak class is clean); `struct_generation` refreshes
  stale watch pointers before each read (no UAF; freed HAL shmem stays mapped);
  `hal_shim_net` hard-caps pins at 64; `show`/`save` growth capped
  (`showMaxCap`/`saveBufMax`); `Save`-to-file is not REST-reachable (hardcoded
  empty filename); `cpupool` lock discipline correct; numeric parsing matches
  2.9 `set_common`.
- **halparse core robust:** no reachable tokenizer infinite-loop / no-advance;
  every `parseX` length-checks before indexing; source-include recursion bounded
  (`depth>=20`); `.tcl` rejected; `text/template` depth-limits recursion;
  executor stops on first error (matches 2.9 `keep_going==0`).
- **haljson:** nil-INI fix present + complete; no double-prefix (mqtt class);
  apply path recurses over config not attacker input, array apply bounds-checked;
  no goroutines spawned in-module; post-startup tree is read-only (no Go race).
- **modcompile:** per-instance state clean (no module-global leak across
  instances); scalar param default ordering correct despite a stale comment; no
  double-free / missing HAL free (except MC-2's `_data`); name mangling + arch
  guard correct; parser yields positioned errors, not panics.
