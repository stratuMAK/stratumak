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
| `SR` | 1 | — | — | 1 |

Two rows of an earlier version of this table were wrong and are gone: `BLINK`
matched `fbBlinkDelay`, which is a `TON`, and a comment; `SEMA` matched a line
of configuration that is not ST at all. Nothing in the three projects
instantiates either, nor `DERIVATIVE`, `INTEGRAL` or `PID` — those three match
only comments, in a file where somebody wrote a PID by hand.

**And the table is not the specification.** It says what these three projects
needed, which is a sample of three. A standard library is judged by what a PLC
programmer reaches for, so §7 builds blocks with no row here at all — and the
hand-written PID is evidence *for* shipping one, not against.

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

`§3.3` of the frontend note originally settled that `fperiod` exists in a
`PROGRAM` only, and that a block needing the cycle time takes it as an input.
A `TON` appears to break that, and the obvious repairs are both wrong — §4
below settles it a third way, and has been implemented:

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

## 4. Phase 0: `task_period`, and `fperiod` everywhere — **done**

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
has been amended to say. `fperiod` stays out of a `FUNCTION`, with a message
that gives the reason rather than "not declared".

**Verified on the machine, not only in the generated C.**
`tests/modcompile-st-period` runs one `FUNCTION_BLOCK` from two threads a
decade apart and captures the `fperiod` each instance saw:

```
0.000100 0.001000
```

which is the claim itself — a constant would fail it, and so would anything
resolving to the wrong thread. The helper is emitted only where something
reaches it, because it is a `static` function and cmods build with `-Werror`.

## 5. Phase 1: the `TIME` type — **done**

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

**Conversions.** The plan originally said `TIME_TO_LREAL` would be seconds.
That is a trap and it was not built: in CODESYS `TIME_TO_REAL(T#1s)` is
`1000.0`, because a CODESYS `TIME` is milliseconds, so a ported source would
silently compute a number a thousand times off with nothing to see.

So **every IEC-named conversion is milliseconds**, exactly as CODESYS has it —
`TIME_TO_DINT`, `TIME_TO_UDINT`, `TIME_TO_LINT`, `TIME_TO_ULINT`,
`TIME_TO_REAL`, `TIME_TO_LREAL` and the six inverses. Seconds are what
stratuMAK works in — `fperiod` is seconds, and so is every float pin carrying a
time — so they get a pair of names of their own: `TIME_TO_SECONDS` and
`SECONDS_TO_TIME`. Those are not IEC names and appear in no CODESYS source,
which is the point: there is no spelling that could be read either way. A unit
test asserts the split, so a conversion added later cannot quietly join the
wrong group.

**Not on a pin.** HAL has no duration type, and `ir.go` already said `TIME` has
no HAL representation. A component converts at the boundary — `PT :=
SECONDS_TO_TIME(pt_pin)` — which is also what `ton.comp` does today with a
float `pt` param in seconds. Both the `VAR_OUTPUT` path and the `AT` path now
say that by name rather than falling back on "no HAL representation".

**Two holes the fixture found**, both from a duration not being on the numeric
lattice and so slipping past checks written in terms of `IsInteger`:

- Dividing a duration by a runtime value was **unguarded**, where every other
  integer division goes through `__st_div_s`. A zero divisor would have raised
  `SIGFPE` and taken the RT process with it. `guardName()` now names `TIME`.
- A divisor written out as `0` was accepted, where the numeric path makes it a
  compile error. §4 of the frontend note is explicit that the runtime guard is
  for divisors that cannot be known at compile time, not for one written out.

A third was a plain omission: the new IR node was not in either expression
walker, so the `fperiod` use-scan missed a reference inside a conversion and
the helper went unemitted. The generated C then failed to compile, which is how
it was caught.

## 6. Phase 2: the library mechanism — **done**

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
directory of the file being compiled, then `<cmod-dir>/st-lib`. A library found
earlier shadows one found later, which is what lets a project override a
shipped one without moving anything.

Not built: `--st-lib-dir` and the `--verbose` report of which file was taken.
modcompile has no `--verbose` flag to hang the latter on, and inventing one was
not worth it — the case where the question is actually asked is a library that
was *not* found, and that message lists every directory searched.

**Names are flat within a library.** Two `USING` libraries both exporting
`Timer` is a compile error naming both files, not a silent win for one.
Qualified reference (`timers.TON`) is a later addition and is noted in §9
rather than built now.

**Libraries may use libraries**, which needs the cycle check the frontend
already has the shape of — `checkContainmentCycles` and `checkCallCycles` in
`st/sema/pou.go`.

**Reachability pruning**, and one thing it cannot express. Only blocks and
functions the component reaches are generated. This was not new with libraries:
a component's own unused `FUNCTION_BLOCK` already failed the build, because a
block body is a `static` function and cmods build with `-Werror`. Pruning fixes
that too, and it is what keeps a 30-block library from costing every consumer
30 compiled function bodies to use one of them.

The obvious alternative is `-Wno-unused-function`, and it was considered. It is
a global loosening of a warning that is genuinely useful in hand-written
`.comp` C, where a stranded static function usually is a mistake — and it would
not save the compile time.

But reachability has two levels and pruning only has one. `VAR s : Held;
END_VAR` puts a `Held_t` in `inst_t`, so the *type* is needed, while no
statement invokes it, so the *body* is not. Splitting the pass in two to
express that would be a lot of machinery for a narrow case, so the generated
block body and init carry `__attribute__((unused))` — the same attribute the
cmod headers already use for a static inline a given module may not call. That
is the surgical form of the flag idea: scoped to generated code, where "unused"
is not a mistake, and leaving `-Werror` intact everywhere else.

A related hole showed up on the way: a `VAR_GLOBAL` instance never went through
`noteInstance`, so pruning dropped its block and the generated C named a type
that was no longer there. Globals now count as reachability roots.

**Diagnostics point at the library source.** A type error in a call to a
library block reports the library's file and line for the declaration and the
consumer's for the call, and `#line` in the generated C names the library file,
so a fault in `TON` maps to `timers.st`. That last part needed a fix: `lineTo`
named one file for the whole generation, which was true while every position
came from one file and stopped being true here.

**Also refused, each by name:** compiling a library directly, a `USING` that
names no library, a file whose `library` attribute disagrees with its
filename, a library declaring a `PROGRAM` or a `VAR_GLOBAL`, and a ring of
libraries — reported as the ring, `la -> lb -> la`. A name declared by both a
component and a library is an ordinary redeclaration, which already names both
files and needed nothing new.

## 7. Phases 3–5: the library contents

Everything below is written in ST, in-tree, and compiled by the same frontend
as user code. Nothing in the standard library is a compiler builtin.

**Phase 3 — no new language needed — done.** `SR`, `RS`, `R_TRIG`, `F_TRIG`,
`CTU`, `CTD`, `CTUD`, in `src/hal/st-lib/standard.st`, installed to
`<cmod-dir>/st-lib` so `USING standard;` resolves with no configuration.

One library rather than several, matching CODESYS's `Standard.lib`: pruning
makes an unused block free, so splitting it would only make the reader choose a
subset by hand.

**One deliberate difference from the standard's own bodies.** IEC declares a
counter's clock as `CU : BOOL R_EDGE`, and stratuMAK refuses that qualifier —
an input that silently becomes a one-cycle pulse is invisible when reading the
body. The counters hold an `R_TRIG` and the edge is written out, which also
proves a library block may use another from the same file.

**Three frontend gaps it found**, none of them in the blocks:

- A library could not declare `VAR_GLOBAL CONSTANT`. The phase 2 rule refused
  every `VAR_GLOBAL`, but a constant is not state: it folds to its value and
  emits no storage. The counters are written in terms of `CV_MAX`/`CV_MIN`.
- Constant globals were declared *after* the POUs were checked, so a
  `FUNCTION_BLOCK` body could not see one at all. They now live with the types.
- `-32768` was not a writable `INT`. The literal was range-checked before the
  negation folded, so the most negative value of every signed type was
  unwritable. The sign now folds into the literal first.

**And one bug in a block**, caught by running it rather than by reading it:
`F_TRIG`'s `M` must start true. An instance loads with its members zeroed, so
with `M` false and `CLK` low the first cycle computes `NOT CLK AND NOT M` and
reports a falling edge that never happened. IEC initialises it for exactly
that reason. `tests/modcompile-st-standard` drives the blocks from HAL and
asserts the properties by name — which latch wins when both inputs are true,
that an edge lasts one cycle, that a counter counts edges and not levels — so
a broken block is named rather than showing up as a diff line.

**Licensing.** The libraries are LGPL-2.1-or-later with an added permission,
not GPL, so a proprietary module may use them. The permission is not
ceremonial: the LGPL is written around linking against a work that stays
separate, and nothing here is separate — `USING` compiles the declarations into
the consumer's own module, which is closer to including a header than to
linking, and §6's relink obligations make no sense for it. `src/hal/st-lib/COPYING`
says so explicitly rather than leaving the intent to a later reading. The LGPL
text itself is not yet bundled; a `COPYING.LIB` belongs beside the existing
`COPYING` before these are distributed.

**Phase 4 — the timers — done.** `TON`, `TOF`, `TP` and `TONR`, in the same
`standard` library. 60 of the 79 measured instantiations.

They needed one new thing: ST could not read the clock. `MONOTONIC` is now a
builtin of type `TIME`, alongside `fperiod`, and reads
`env->rtapi->get_time()` — a duration since an origin the runtime does not
define, so only differences between two readings mean anything, which is what
a timer needs and all it needs. It is emitted inline rather than through a
helper: one expression, so there is no static function that could go uncalled
and no use-scan to keep in step with it. Like `fperiod` it is refused in a
`FUNCTION`, for the sharper reason that a function returning something
different on each call would not be one.

**Why not `fperiod`.** §3 argued this before the timers existed and the
implementation did not change it: IEC's `TON(IN, PT)` exposes no cycle input
because its semantics are elapsed real time. A timer adding `fperiod` each
cycle measures cycles times the *nominal* period, so it runs slow by whatever
the thread loses to jitter or an overrun, and two threads driving one instance
would disagree. Reading the clock costs one call and has neither problem.

Each body reads the clock once into a member `now`, so a reading taken and
used in the same cycle cannot differ from itself. There is no `VAR_TEMP` here,
so that member costs eight bytes per instance — the price of the reading being
consistent.

**Testing them is not like testing the counters.** Which cycle a transition
lands on depends on how long a cycle actually took, so
`tests/modcompile-st-timers` has no `expected` file to diff: pinning the
transition would fail on a loaded machine rather than on a broken timer. It
asserts what holds at any rate — the state well before a deadline, the state
well after, and the two things that are exact regardless of timing: that `ET`
stops at `PT`, and that a pulse does not follow its input. The margins are
one-sided on purpose, because a late thread means more real time per cycle and
so a timer that fires in *fewer* cycles, never more. Verified by making `TP`
retriggerable, which the check names rather than reporting as a diff.

**Phase 5 — the rest of a usable library — done.** `SEMA`, `INTEGRAL`,
`DERIVATIVE` and `BLINK`, plus `LIMITALARM`, `HYSTERESIS`, `LIN_TRAFO`, `RAMP`
and `PID`.

The plan said "the CODESYS blocks the projects use", and that premise turned
out to be false — see §1. The criterion is what a PLC programmer reaches for,
not what three files happened to contain, so these were built anyway.

**The analog blocks take `CYCLE : TIME`** rather than reading `fperiod`, which
is IEC's own arrangement and the right one: the caller decides what interval
the block integrates or differentiates over, and passing `fperiod` is only the
common case. The timers are different because elapsed real time is the whole
of what they measure; here the cycle is a coefficient.

**`DERIVATIVE` uses IEC's four-point form**, not a difference of the last two
samples. A backward difference amplifies noise by `1/CYCLE` — a thousandfold
at a 1 kHz servo rate — and this block's output usually goes somewhere that
acts on it. The coefficients are what make it exact on a ramp, and the test
asserts exactly that: for an input rising 2.0 per one-second call the output
must read `2.000000` and nothing else, so a wrong constant is a wrong number
rather than noise.

**`PID` is the one with real choices in it**, all documented in the block: the
classic gain / reset-time / rate-time parameterisation because that is what
tuning procedures are written in; derivative on the measurement rather than
the error, so a setpoint step does not arrive as a spike of `TV/CYCLE` times
its size; and clamping anti-windup, without which a long excursion leaves the
controller saturated well after the error reverses. It is not a bit-exact
clone of any vendor's, and says so.

**`HYSTERESIS` takes two explicit thresholds** rather than CODESYS's
centre-and-width. The signature differs, so a ported source fails to compile
instead of running with the polarity inverted — the right way round to be
wrong.

`tests/modcompile-st-analog` drives all of these with a fixed `CYCLE` of one
second, so unlike the timers the numbers are arithmetic rather than a
measurement and the expected file can be exact. Verified by breaking
`DERIVATIVE`'s constant and by turning `HYSTERESIS` into a plain comparator.

**`RTC` — done, and the reason it was deferred was wrong.** The note said it
needed a wall-clock source that does not exist. It does not: IEC's `RTC` is
seeded from its own `PDT` input on a rising edge of `EN` and counts forward
from there, so the only thing it measures is how long it has been running, and
`MONOTONIC` is exactly that. Where `PDT` comes from is the caller's problem,
and rightly — a machine with an RTC chip, an NTP client or nothing at all all
look the same from inside the block.

What it did need was the type. `DATE_AND_TIME` (`DT`) is now implemented,
held as nanoseconds since 1970-01-01 UTC — the same unit as `TIME`, which is
what lets one be added to the other without a scale factor appearing from
nowhere. It is a separate type from `TIME` on purpose, and the arithmetic is
what makes the distinction earn its keep: two points subtract to the duration
between them, a point and a duration add to another point, and two points
added together are refused, because what would twice last Tuesday be?

`DT#2026-08-26-08:30:00` is the literal. Dates are validated by constructing
and reading back, because Go's `time.Date` normalises `2026-02-30` into
March 2 rather than refusing it. The conversions are seconds since the epoch,
which is the unit CODESYS holds a `DT` in, so there is no millisecond form of
them to disagree about — the split that `TIME` needed does not arise here.

`DATE` and `TIME_OF_DAY` are still not implemented. They carve a point in time
into halves, and the message for each now points at the whole rather than
claiming the family is missing.

## 7a. Ahead: GMI in Structured Text

Not part of this plan, recorded because it constrains one decision that would
otherwise be made carelessly.

A `.gmi` API maps onto IEC 61131-3 ed.3 almost one-to-one, so the idiomatic
spelling is the standard's own rather than anything invented:

| GMI | ed.3 |
|---|---|
| `.gmi` API | `INTERFACE` |
| `func pktuart_config(...) -> i32` | `METHOD pktuart_config : DINT` |
| `gmi_provide home` | `FUNCTION_BLOCK X IMPLEMENTS home` |
| `gmi_consume hm2_serial from hostmot2` | `VAR s : hm2_serial;`, bound at load |

Three things make it fit.

**The `ctx` disappears.** Every GMI callback takes `void *ctx` first, and on
the provider side that ctx is the instance — so a method on a function block is
the shape the callback already has, minus the plumbing. On the consumer side it
is bound into the handle, where today a `.comp` writes
`HM2_SERIAL->pktuart_config(HM2_SERIAL->ctx, ...)` by hand.

**It does not reopen pointers.** An interface reference resembles the
`REFERENCE TO` that §8 of the frontend note refuses, but a consumer handle is
resolved once in `New()` and never reassigned or offset. A bound handle is not
a pointer, and the difference is enforceable.

**`@rt_safe` becomes a compile-time check.** The IDL already marks each method,
and the only thing enforcing it today is `STMAK_API_NONBLOCKING`, which needs
clang 20 — a gcc build gets nothing, and a `.comp` body may call anything. An
ST body running in a HAL thread could be refused by name for calling a method
the IDL does not mark rt-safe.

**Lookup should be `USING`.** Phase 2 built one mechanism for bringing outside
declarations in; a `.gmi` surfaced as a synthesised library reuses it rather
than adding a second.

**The decision this forecloses — settled.** All of the above needs methods,
`inst.m(args)` with a result, and calling a function block's `ACTION` from
outside claims the same `inst.something()` spelling.

**They stay distinct, and share the call syntax.** An action has no interface
and a method has one, so they are not the same construct, and merging them
would mean giving an action parameters it has no use for. Sharing the syntax
is what CODESYS does, and it is what makes `motor.stop()` read the way it
should. A name is one or the other, never both, so resolution is unambiguous.

**What keeps them apart is the argument list.** From outside, an action call
takes none. Inside the declaring POU, `Act(v := x)` is shorthand for setting
one of that POU's own variables first (§3.4) — from outside the identical
spelling would reach past the instance's interface into its state, and it is
also exactly what a method call looks like. Refusing it means
`inst.name(a := 1)` is a method call and nothing else whenever methods arrive,
rather than something whose meaning depends on what `name` turned out to be.

`inst.Action()` is implemented on that basis. Methods are still not, and are
GMI's business. None of `INTERFACE`, `METHOD`, `IMPLEMENTS`, `EXTENDS` or
`THIS` is reserved yet, so nothing there is spent.

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
- ~~**Calling a function block's action from outside it**~~ Done, on the terms
  §7a settles: shared syntax, distinct constructs, and no argument list on an
  action call so that methods can have one. None of the three projects relied
  on it — their `inst.Name()` calls are CODESYS *methods*, a separate ed.3
  feature that is still out of scope.
- **Binary libraries**, per §2, unless something later makes the case.
- ~~**Man pages for libraries.**~~ Done. `modcompile --document` on a library
  writes one man3 page per function block, installed to `docs/man/man3` by the
  build, so `man 3 TON` answers the question somebody actually asks — a block
  is the unit you use, not the library. This was the first of these items to
  do, because shipping the standard library created the problem: 126 `doc`
  pragmas on the blocks' interfaces, and no way to read one short of opening
  the source. The pages are rendered from the syntax tree rather than a
  checked package, because a library has no `PROGRAM` and so is not a
  component — and because `PT : TIME` as the author spelled it is the answer
  to "what do I pass", where the resolved kind is not.
- **Whether `TIME` should reach a pin** after all, as `s32` nanoseconds. §5
  refuses it; if a real component wants a HAL-settable `PT` often enough, that
  is the thing to revisit first.
