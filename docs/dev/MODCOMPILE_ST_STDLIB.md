# Reusable ST libraries and the standard function block library

Status: plan. Follows `MODCOMPILE_ST_FRONTEND.md`, which is implemented.

The frontend can compile a `.st` file into a component. What it cannot do is
share anything between two of them: a `FUNCTION_BLOCK` is file-local, so a
`TON` written once has to be pasted into every file that wants one. Measured
against the three production projects, that is the largest remaining gap —
larger than any language feature still missing.

## 1. What the projects actually need

Counting standard block instantiations across the three CODESYS projects:

| Block | galv | sporthalle | lackieranlage | total |
|---|---:|---:|---:|---:|
| `TON` | 13 | 10 | 26 | **49** |
| `TOF` | 3 | 3 | 1 | 7 |
| `R_TRIG` | 1 | — | 10 | 11 |
| `TP` | 2 | 2 | — | 4 |
| `F_TRIG` | — | — | 2 | 2 |
| `RTC` | 2 | — | — | 2 |
| `BLINK` | 2 | — | — | 2 |
| `SR` | 1 | — | — | 1 |
| `SEMA` | 1 | — | — | 1 |

Timers are 60 of the 79 instantiations. `T#` literals appear over a hundred
times, and `PT` is usually a struct member rather than a literal:

```
fbCallibTimeout(PT := sConfig.tCalibTimeout);
```

so `TIME` has to be a real type that works inside a `STRUCT`, not just a
literal form. That settles §3 below by measurement rather than by taste.

## 2. Source libraries, not binary ones

A library is a `.st` file that modcompile resolves, type-checks and generates
into the consumer's own `.so`. It is not a shared object of its own. The
reasons are worth writing down, because "dynamically linked" is the obvious
first instinct and it is the wrong one here.

**The state layout has to be public either way.** `Ramp_t r;` is embedded in
`inst_t`, and the whole component is one `calloc`. A consumer needs a library
block's size and layout at compile time to embed it, so the library must ship a
compile-time declaration whatever else it ships — a binary artefact is added,
not substituted. The escape is opaque state allocated by the library, which
means one allocation per block instance, pointer chasing on the RT path, and
construction that can fail at load.

**It would cost the recompile *and* buy the skew.** Changing a library block's
variables changes its layout, so consumers must be rebuilt under either model.
With a binary boundary a stale consumer is a runtime crash instead of a compile
error.

**It forecloses what the current design gets free.** A cmod `.so` links only
libc and libm; block bodies are static functions in the same translation unit,
so gcc inlines them at `-Os`. Cross-`.so` calls go through the PLT and cannot
be. Sema type-checks every call against the real declaration rather than
against a header that may have drifted, and `#line` maps a fault in a library
block back to the library's own source.

**It is not what PLCs do.** CODESYS and TwinCAT libraries are build-time
artefacts linked into the application. There is no runtime function block
library loading in a PLC.

The cost is duplication: a component using `TON` carries its own copy. `and2.so`
is 24 kB and a `TON` is a couple of hundred bytes of code, so this does not
matter.

## 3. Cycle time, and why the timers do not need it

`§3.3` of the frontend note settled that `fperiod` exists in a `PROGRAM` only,
and that a block needing the cycle time takes it as an input. A `TON` appears
to break that, and the obvious repairs are both wrong:

- **Threading `period` down the call chain** gives a block the period of
  whichever HAL function called it. That is defensible, but a block instance
  reachable from two programs in two threads then sees two different values,
  and nothing says which.
- **A helper returning the current thread's cycle time** is what TwinCAT
  exposes (`GETCURTASKINDEXEX()` with `_TaskInfo[i].CycleTime`), and it is
  worth having — see §4 — but it is not what a timer wants.

**A timer wants elapsed real time.** IEC's `TON(IN, PT)` exposes no cycle input
at all, and the semantics are about duration, not about counting cycles.
stratuMAK already has the right primitive: `env->rtapi->get_time()` is
monotonic nanoseconds, declared `STMAK_NONBLOCKING`, and already called from an
RT body (`latency_funct` in `hal/components/latency.c`).

A timer written against that clock is IEC-exact *and thread-independent*: it is
correct whichever thread calls it, and correct if two threads call the same
instance, because it never asks what a cycle is. That covers 60 of the 79
measured instantiations without any language change.

**The blocks that genuinely need cycle time take it as an input, and IEC says
so.** The standard's `DERIVATIVE` and `INTEGRAL` both carry `CYCLE : TIME`
explicitly. Keeping IEC compatibility therefore *requires* an input rather than
a query, and the call site becomes the honest place where "which thread's
rate?" is answered.

## 4. Phase 0: `task_period`, and `fperiod` everywhere

Independent of the library work, and first, because it fixes a live bug.

**The bug.** `fperiod` inside an `ACTION` generates C that does not compile:

```
error: 'period' undeclared (first use in this function); did you mean 'fperiod'?
```

An action becomes its own C function and `period` is not one of its parameters.
gcc catches it, so nothing is silently miscompiled, but an error in generated C
is exactly what the frontend exists to prevent.

**The fix, and the capability.** The RT core already has everything needed.
`rtapi->task_self()` returns the calling task's id; `get_task()` is
bounds-checked and validates a magic; and `task->period` is set in
`task_start()`. What is missing is only the accessor:

```c
/* uspace_rtapi_lib.c */
static long task_period(int task_id) {
    struct rtapi_task *task = get_task(task_id);
    if (!task) return 0;
    return task->period;
}

/* stmak_rtapi.h — beside the existing task_self */
int     (*task_self)  (void *ctx) STMAK_NONBLOCKING;
int64_t (*task_period)(void *ctx, int task_id) STMAK_NONBLOCKING;
```

plus the callback wiring in `internal/launcher/cmodules.go`.

**What `fperiod` then means.** One meaning, computed two ways: *the period of
the thread this body is running in*. Where the HAL function's `period`
argument is in scope — a `.comp` body and a `PROGRAM` body — it is used
directly and nothing changes, which is what keeps the 132 shipped `.comp`
components byte-identical. Where it is not — a `FUNCTION_BLOCK` body, an
`ACTION` — the lookup is emitted. The two agree by construction, because a HAL
function is invoked by its own thread.

**Outside an RT thread** `task_self()` returns a negative id and the lookup
yields zero, so `fperiod` is `0.0` rather than undefined. Named in the docs.

This removes the `PROGRAM`-only restriction from frontend §3.3, which that note
must be amended to say.

## 5. Phase 1: the `TIME` type

Groundwork exists: `ast.TypeTime` is already a type kind, the lexer already
recognises `T#`/`TIME#` prefixed literals, and sema already refuses them by
name (`"the TIME type is not implemented yet; it arrives with the standard
function block library"`). What is missing is the type itself.

**Representation: `int64` nanoseconds.** IEC leaves the range
implementation-defined. CODESYS `TIME` is 32-bit milliseconds, which caps at
about 49 days and quantises to 1 ms — one whole cycle of a 1 kHz thread, and
ten of a 10 kHz one. Nanoseconds cost nothing here and match `get_time()` and
`task->period`, both of which are already ns.

**Literals.** `T#0s`, `T#1000MS`, `T#500ms`, `T#1m30s`, `T#3s` all appear in the
measured sources, so the full compound form is required, case-insensitively,
with `LT#`/`LTIME#` as accepted spellings of the same thing.

**Arithmetic.** `TIME ± TIME → TIME`; `TIME * number → TIME`; `TIME / number →
TIME`; `TIME / TIME → LREAL`. Comparisons across the board. Refuse `TIME +
number`, which is the mistake that reads as though it works.

**Conversions.** `TIME_TO_DINT` and `DINT_TO_TIME` are milliseconds, matching
CODESYS, so ported sources compute the same numbers. `TIME_TO_LREAL` and
`LREAL_TO_TIME` are seconds, which is the natural stratuMAK unit and what
`fperiod` already is. Both directions documented as what they are; this is the
one place the ns representation is visible and it is worth the note.

**Not on a pin.** HAL has no duration type, and `ir.go` already says `TIME` has
no HAL representation. A component converts at the boundary — `PT :=
LREAL_TO_TIME(pt_pin)` — which is also what `ton.comp` does today with a float
`pt` param in seconds.

## 6. Phase 2: the library mechanism

**A library file** is a `.st` declaring `TYPE`, `FUNCTION` and
`FUNCTION_BLOCK` and no `PROGRAM`, marked by a file-level pragma:

```
{attribute 'library' := 'timers'}
{attribute 'summary' := 'IEC 61131-3 standard timers'}
{attribute 'license' := 'GPL'}
```

A pragma rather than a new keyword, because file-level metadata is already
carried that way (`summary`, `license`, `author`), and because the build
wildcards in `hal/components/Submakefile` must not try to turn one into a
`.so`. The marker is what makes "no PROGRAM declared" the right error for a
component and no error at all for a library.

**Use** is `USING`, which is what CODESYS 3.5 and TwinCAT spell it:

```
USING timers;

PROGRAM mycomp
  VAR
    delay : TON;
  END_VAR
  delay(IN := start, PT := T#500ms);
  running := delay.Q;
END_PROGRAM
```

**Search path**, in order: `$STMAK_ST_LIB_PATH` (colon-separated), the
directory of the file being compiled, then the shipped directory — printed by a
new `modcompile --st-lib-dir`, beside the existing `--cmod-dir` and
`--include-dir`. A library found earlier shadows one found later, and
`--verbose` says which file was taken.

**Names are flat within a library.** Two `USING` libraries both exporting
`Timer` is a compile error naming both files, not a silent win for one.
Qualified reference (`timers.TON`) is a later addition and is noted in §9
rather than built now.

**Libraries may use libraries**, which needs the cycle check the frontend
already has the shape of — `checkContainmentCycles` and `checkCallCycles` in
`st/sema/pou.go`.

**Reachability pruning is mandatory, not an optimisation.** Only blocks and
functions the component actually reaches may be generated. This is not about
binary size: every generated block body is a `static` function, and cmods build
with `-Werror`, so an unreached one is `-Werror=unused-function` and the
component fails to compile. The same trap already cost a fix once, for the
division guards — `guardName()` exists as a single source of truth precisely
because of it.

**Diagnostics point at the library source.** A type error in a call to a
library block reports the library's file and line for the declaration and the
consumer's for the call, and `#line` in the generated C names the library file,
so a fault in `TON` maps to `timers.st`.

## 7. Phases 3–5: the library contents

Everything below is written in ST, in-tree, and compiled by the same frontend
as user code. Nothing in the standard library is a compiler builtin.

**Phase 3 — no new language needed.** Pure ST on what already exists:
`SR`, `RS`, `R_TRIG`, `F_TRIG`, `CTU`, `CTD`, `CTUD`. These are the proof that
the library mechanism works, and they are worth landing before the timers so
that phase 2 is validated by something with no other moving parts.

**Phase 4 — the timers**, on `TIME` and the monotonic clock: `TP`, `TON`,
`TOF`, and `TONR` from ed.3. This is 60 of the 79 measured instantiations.

**Phase 5 — the CODESYS blocks the projects use**, which are `Standard`/`Util`
rather than IEC core but appear in the sources regardless: `BLINK`, `SEMA`, and
the analog blocks with an explicit `CYCLE : TIME` input (`DERIVATIVE`,
`INTEGRAL`, `HYSTERESIS`, `LIMITALARM`, `RAMP`).

`RTC` is **deferred** and belongs in §9: it needs wall-clock date and time, not
a monotonic count, so it needs both a `DATE_AND_TIME` type and a runtime source
that does not exist yet. `get_time()` is explicitly monotonic and is the wrong
clock for it.

## 8. Testing

Three layers, all of which already have a harness:

- **Fixtures** under `st/testdata/accept/`, one per library block, registered in
  the two ledgers (`sema/corpus_test.go`, `cgen/st_test.go`) that already catch
  an unregistered fixture.
- **A runtest** in `tests/modcompile-st/` building a component against each
  block and asserting the generated C — including that an unreached block is
  *not* generated, which is the `-Werror` trap in §6.
- **A behaviour differential.** `tests/modcompile-st-behaviour/` already drives
  two components from HAL and asserts they agree cycle by cycle. Running the ST
  `TON` against the shipped `ton.comp` is the strongest check available that
  the library block is correct, and it costs nothing to set up.

The `.comp` byte-identical gate over all 132 shipped components stands
throughout, as it did for every phase of the frontend.

## 9. Out of scope, and open

- **Qualified names** (`timers.TON`) and namespaces. Flat plus a collision
  error first.
- **`RTC`** and the date and time types, which need a wall clock.
- **Library versioning.** Nothing is checked today; a library is whatever the
  search path found. A version pragma and a `USING timers >= 1.2` constraint is
  the obvious next step and deliberately not in this plan.
- **Binary libraries**, per §2, unless something later makes the case.
- **Man pages for libraries.** `docgen` writes man9 for components, and a
  library is not a component. Section 3 is the conventional home for library
  documentation; the page would list each block with its inputs, outputs and
  their docs, all of which sema already carries. Small, but unspecified here.
- **Whether `TIME` should reach a pin** after all, as `s32` nanoseconds. §5
  refuses it; if a real component wants a HAL-settable `PT` often enough, that
  is the thing to revisit first.
