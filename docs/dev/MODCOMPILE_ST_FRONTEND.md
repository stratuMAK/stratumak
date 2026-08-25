# Structured Text as a modcompile frontend — design note

Status: **proposed, not yet implemented**, 2026-08-25. Written on branch
`add-modcompile-st-support`, which is currently identical to `main`. The AST
already anticipates this work: `internal/modcompile/ast/ast.go:5` names the
frontends as ".comp, future .st", and lines 342-363 carry placeholder
`TypeDef`, `FunctionBlock` and `PureFunction` stubs waiting to be filled in.

The goal is that an `.st` file becomes a HAL component the same way a `.comp`
file does — `modcompile` parses it, lowers it to the shared AST, and the
existing generator emits the module. Everything below the frontend is already
written and stays untouched: the instance struct, HAL registration, the cmod
lifecycle, GMI bindings, arch guards, modparams, `#line` mapping and man-page
generation.

## 1. Why not build on the existing prototype

A standalone ST compiler exists at `~/source/gomc-st-compiler` (~8.4k lines of
Go, tests passing). It is not carried over, for two structural reasons.

**Its backend is the thing `cgen` already does.** The prototype emits a
free-standing `Foo_t` with `Foo_init()` / `Foo_cycle()` and hardwires `inst->`
into every identifier (`pkg/codegen/generator.go:869`). That is a weaker
re-implementation of the module skeleton `cgen.go` already produces, and none
of it survives contact with the cmod API.

**It has no semantic analysis layer.** There is no symbol table, and no
expression typing — `generateExpression` threads the *destination* type down as
a hint instead of inferring anything. Two consequences, both reproduced:

```
TYPE Light : (RED, YELLOW, GREEN); END_TYPE
VAR l : Light := RED; n : INT; s : STRING[10]; END_VAR
l := GREEN;
n := s;
```

emits `inst->l = inst->RED;` — uncompilable C from the prototype's own
documented enum feature — and accepts `n := s` silently. Its own README examples
work around the first by writing raw integers. Retrofitting resolution and
typing means rewriting the parser AST and all of codegen, which is the whole
program.

Also absent, each confirmed by probe: `FUNCTION_BLOCK`, `FUNCTION`, `ELSIF`,
`EXIT`, `RETURN`, `MOD`, based literals (`16#FF`), arrays of structs, indexed
lvalues (`a[i].f := x`), `TIME`, and the standard function block library.

**What is worth taking:** the IEC-to-C type mapping table in
`pkg/types/types.go`, and `examples/*.st` plus `testdata/` as an acceptance
corpus. Roughly 400 useful lines out of 8400. The prototype was relicensed from
GPL-3 to GPL-2 on 2026-08-25 so this is possible; it is a private repository
with no external contributors, so the relicence needed no consent beyond
modusoft's own.

## 2. Settled design decisions

| Question | Decision |
| --- | --- |
| HAL surface | A `PROGRAM`'s `VAR_INPUT` / `VAR_OUTPUT` / `VAR_IN_OUT` become `in` / `out` / `io` pins. Nothing else reaches HAL. |
| Other variables | `VAR` and `VAR_GLOBAL` become per-instance state in `inst_t`, exactly like a `.comp` `variable` declaration. |
| Sub-components | `FUNCTION_BLOCK`, internal only. An instance nests as a struct inside `inst_t` and has no HAL identity. |
| File shape | One `PROGRAM` per exported HAL function. `VAR_GLOBAL` is shared between the programs in a file. |
| Metadata | CODESYS-style pragmas — `{attribute 'license' := 'GPL'}` — feeding the existing `docgen`. |
| Live watch | Deferred in `cgen`; the IR still carries full type information so the later work is confined to the backend. |

Two of these were argued over and are worth recording with their reasons.

**Function block instances do not publish HAL pins.** Publishing them was
considered and rejected. An FB's `VAR_INPUT` is written by ST expression
evaluation at the call site, so making it a HAL `in` pin gives it two writers —
the ST body and whatever signal is `net`'d to it — and HAL in-pins are
single-writer by design. Keeping them internal also keeps an instance as one C
struct rather than splitting every instance across `inst_hal_t` (shared memory)
and `inst_t` (private heap) at every nesting depth, and avoids
`STMAK_HAL_NAME_LEN` (127) path-length failures on nested or arrayed instances.
Nothing is lost: where a sub-component's input genuinely should be wirable,
declare the pin at `PROGRAM` level and pass it in.

**Live watch is deferred, but the IR is not.** `.comp` `variable` declarations
land in `inst_t`, which is on the private rtapi heap (`cgen.go:492-493`) — only
pins and params live in HAL shared memory. So mapping ST `VAR` the same way
gives the right scoping but does *not* make those variables remotely
observable; nothing outside the process can see `inst_t`. With FB I/O also off
HAL, the only observable surface is the `PROGRAM` boundary. The descriptor table
therefore becomes the sole route to live watch rather than a supplement to it,
and the IR carries full type information — array shapes, struct layouts, nested
instance paths — from day one even though nothing consumes it yet. That is what
keeps the later work inside `cgen` instead of reopening the frontend.

## 3. The `.st` dialect

The component name comes from the filename, as `modcompile` already enforces for
`.comp` (`cmd/modcompile/main.go:462`). Identifiers follow the existing HAL
naming transform — an underscore in ST becomes a hyphen in the pin name, so
`max_rate` publishes as `ramplimit.max-rate`. That is `toHALFmt`
(`cgen.go:219`), unchanged.

A complete component:

```
{attribute 'summary'     := 'Rate-limited position follower'}
{attribute 'license'     := 'GPL'}
{attribute 'author'      := 'Sascha Ittner'}

FUNCTION_BLOCK Ramp
  VAR_INPUT
    target : LREAL;
    rate   : LREAL;
    dt     : LREAL;
  END_VAR
  VAR_OUTPUT
    out    : LREAL;
  END_VAR
  VAR
    step   : LREAL;
  END_VAR

  step := rate * dt;
  IF   target > out + step THEN out := out + step;
  ELSIF target < out - step THEN out := out - step;
  ELSE                          out := target;
  END_IF
END_FUNCTION_BLOCK

PROGRAM ramplimit
  VAR_INPUT
    {attribute 'doc' := 'Commanded position.'}
    target   : LREAL;
    {attribute 'doc' := 'Maximum rate of change, units/s.'}
    max_rate : LREAL := 10.0;
    enable   : BOOL;
  END_VAR
  VAR_OUTPUT
    out      : LREAL;
  END_VAR
  VAR
    r : Ramp;
  END_VAR

  IF enable THEN
    r(target := target, rate := max_rate, dt := fperiod);
    out := r.out;
  ELSE
    out := target;
  END_IF
END_PROGRAM
```

lowers into the existing skeleton as:

```c
/* FB state: inputs and outputs persist, per IEC semantics */
typedef struct {
    double target, rate, dt;   /* VAR_INPUT  */
    double out;                /* VAR_OUTPUT */
    double step;               /* VAR        */
} Ramp_t;

static void Ramp_init(Ramp_t *self);
static void Ramp_body(Ramp_t *self) STMAK_NONBLOCKING;

typedef struct {              /* emitInstanceStruct, extended */
    cmod_t base;
    const cmod_env_t *env;
    int comp_id;
    char name[STMAK_HAL_NAME_LEN + 1];
    inst_hal_t *hal;          /* pins: target, max_rate, enable, out */
    Ramp_t r;                 /* VAR r : Ramp — private heap */
} inst_t;

FUNCTION(_) {
    /* pins are volatile: snapshot in, write back out */
    const double   __in_target   = target;
    const double   __in_max_rate = max_rate;
    const unsigned __in_enable   = enable;
    double __out_out;

    if (__in_enable) {
        __comp_inst->r.target = __in_target;
        __comp_inst->r.rate   = __in_max_rate;
        __comp_inst->r.dt     = fperiod;
        Ramp_body(&__comp_inst->r);
        __out_out = __comp_inst->r.out;
    } else {
        __out_out = __in_target;
    }

    out = __out_out;
}
```

`New()` registers the pins through the existing `emitPinCreation` —
`ramplimit.target` (in float), `ramplimit.max-rate` (in float, default 10.0),
`ramplimit.enable` (in bit), `ramplimit.out` (out float) — and exports the
function as `ramplimit` through `emitFunctionExport`.

### 3.1 Type mapping

HAL scalars are `bit`, `s32`, `u32`, `float` and `port`, and HAL `float` is a
`double` (`pkg/cmodule/stmak_hal.h:81-85`). That constrains what may appear on a
program's I/O boundary.

| ST type | As a program I/O variable | As `VAR` / FB state |
| --- | --- | --- |
| `BOOL` | `bit` — exact | `bool` |
| `SINT`, `INT`, `DINT` | `s32` — widened | `int8_t`, `int16_t`, `int32_t` |
| `USINT`, `UINT`, `UDINT` | `u32` — widened | `uint8_t`, `uint16_t`, `uint32_t` |
| `LINT`, `ULINT` | rejected — no HAL type | `int64_t`, `uint64_t` |
| `REAL` | `float` — widened, warns | `float` |
| `LREAL` | `float` — exact | `double` |
| enumeration | `s32` | `int32_t` + named constants |
| `ARRAY [0..n] OF` scalar | array pin, 1-D only | C array, index offset applied |
| `STRUCT`, `FUNCTION_BLOCK` | rejected — pins are scalar | nested struct |
| `STRING [n]` | rejected — see §7 | `char[n+1]` |
| `TIME` | rejected — see §6.1 | `int64_t` nanoseconds |

`LREAL` is the type to use on a pin: HAL `float` *is* a `double`
(`pkg/cmodule/stmak_hal.h:83`), so the mapping is exact in both directions.

`REAL` on a pin is **accepted with a warning**, not rejected. Widening a 32-bit
`REAL` to the pin is lossless, but the read back is not — a value written by HAL
and read into a `REAL` loses precision silently, and no diagnostic at the
assignment can catch it because the narrowing happens at the boundary rather
than in the ST. Rejecting it outright was considered and dropped: `REAL` is what
a PLC programmer reaches for by habit, and refusing to compile an otherwise
correct component over a precision footnote is the wrong trade. The warning
names the pin and says to use `LREAL`, and travels through the existing
`Package.Warnings` channel that `processFile` already prints
(`cmd/modcompile/main.go:468`). Tightening this later is a one-line change if
the warning turns out to be ignored in practice.

### 3.2 Semantics worth stating

**Inputs are stable for the cycle.** HAL scalars are `volatile`, so reading the
same input pin twice in one body would re-read shared memory and could see two
different values. Snapshotting `VAR_INPUT` at entry and writing `VAR_OUTPUT`
back at exit gives ST the cycle semantics a PLC programmer expects, and is
faster. `VAR_IN_OUT` maps to an `io` pin and gets an explicit read-modify-write:
read at entry, write at exit.

**Function block instances persist.** An instance keeps its inputs and outputs
between calls, which is why `Ramp_t` holds all of them and not just `step`. That
makes `r.out` a read of retained state, and it is what lets a timer or an edge
detector work at all.

**A period is available.** `fperiod` is bound to the HAL function's period in
seconds — the same `#define fperiod (period * 1e-9)` the generator already emits
for `.comp` bodies.

## 4. Real-time safety policy

These modules run inside the RT process, so a fault here stops the machine
rather than just the component. ST has no exception model to fall back on and
the generated bodies carry `STMAK_NONBLOCKING`, which means safety has to be a
property the compiler establishes. Five rules, enforced by sema, decided here
rather than patched in later.

**Loops must carry a bound the compiler can see.** `FOR` is bounded by
construction once its limit and step fold to constants, which sema checks.
`WHILE` and `REPEAT` are a compile error unless the loop carries an explicit
cap:

```
{attribute 'max_iterations' := '1000'}
WHILE NOT settled DO
  ...
END_WHILE
```

With the cap, `cgen` emits a guarded counter; exceeding it exits the loop and
raises a fault (below). Without it there is no way to prove termination, and a
runaway loop stalls the servo thread until the watchdog fires. A flat ban was
considered and rejected as impractical — iterative settling is a real pattern
and a bounded `WHILE` expresses it more honestly than a `FOR` with a `break`.

**Dynamic array indices are clamped, never trusted.** A statically-known
out-of-range index is a compile error. A runtime index is emitted with a clamp
to the array bounds plus a fault. The alternative is an out-of-bounds write
inside the RT process, which corrupts `stmakd` rather than the component that
caused it — a component bug must not become a daemon bug.

**Integer division and `MOD` are guarded.** A zero divisor yields zero and
raises a fault. Unguarded, it is `SIGFPE` in the RT thread, which kills the
process. Floating-point division is left alone: IEEE `inf`/`NaN` is a defined
result, not a fault.

**No recursion.** A `FUNCTION_BLOCK` or `FUNCTION` that can reach itself is a
compile error. The RT stack is fixed and there is no useful way to bound the
depth.

**Faults are recorded, not printed.** Every guard above increments a
per-instance fault counter and logs through a rate-limiter emitted by `cgen`; a
clamped index in a 1 kHz thread must not flood the journal. The counter should
become a HAL pin once the shape is settled, so a fault is visible from `halcmd`
without reading logs — that is a small follow-up, not part of the first
implementation.

The remaining RT properties come free: ST has no allocation, no blocking calls
and no unbounded data structures, so there is nothing to enforce.

## 5. Package layout

```
src/stmak/internal/modcompile/
  ast/          extended  IR: statements, expressions, resolved types,
                          storage classes; the three stubs made real
  comp/         unchanged .comp scanner + parser
  st/           NEW
    scanner.go            ST tokens, case-insensitive, based literals
    parser.go             recursive descent -> untyped ST syntax tree
    sema/                 symbols, typing, folding, diagnostics;
                          lowers the syntax tree into ast
  cgen/
    cgen.go     unchanged module skeleton
    stmt.go     NEW       statement + expression emitter
  docgen/       unchanged works off ast.Component already

src/stmak/cmd/modcompile/main.go     + .st dispatch in processFile
src/hal/components/Submakefile       + *.st wildcards for .so and .9
```

### 5.1 Shared IR

`ast` gains a statement and expression node set, and `ast.Function` gains a
`Body []Stmt` alongside the existing `VerbatimC` on the component. The invariant
is that exactly one of the two is populated — a `.comp` sets verbatim C, an
`.st` sets bodies — and `cgen` branches on that in one place.

Two properties matter more than the node list:

- **Every expression carries a resolved type.** Not a hint threaded down from
  the assignment target: the actual inferred type, decided by sema. This is
  precisely what the prototype lacks.
- **Every reference carries a storage class** — pin, parameter, instance
  variable, function-block field, or block-local. The backend then knows whether
  to emit `out` (a convenience macro over shared memory), `__comp_inst->acc`, or
  a plain local, without re-deriving anything.

### 5.2 What the sema pass owns

This is the layer the prototype has none of, and where most of the work sits.

- Case-insensitive symbol resolution across a scope chain: component, program,
  function block, block. ST is case-insensitive for identifiers, not only
  keywords.
- Expression typing with IEC implicit-widening rules; narrowing assignments are
  rejected rather than silently truncated.
- Enumeration literals resolved to their constants.
- Constant folding for array bounds, `CASE` labels and initialisers, all of
  which must be compile-time constant.
- The real-time rules of §4: loop bounds, index clamping, division guards,
  recursion detection.
- Function block resolution — instance declarations, named-argument binding at
  call sites, output reads, nested and arrayed instances, per-instance
  initialisation.
- Dependency ordering for struct and FB typedefs, so emitted C declares each
  type before its first use.
- Diagnostics with `file:line:col`, following the conventions
  `comp/parser.go:99` already uses.

### 5.3 What changes in `cgen`

One new file. `stmt.go` walks the IR and emits statements and expressions;
because sema resolved everything, it is close to a direct transcription with no
lookups of its own.

The existing emitter changes in three narrow places: `emitInstanceStruct` also
emits ST `VAR` members and nested FB instances; a typedef pass runs before it
for user-defined types and FB state structs; and the body emission point chooses
between verbatim C and an ST body. `#line` directives point back at the `.st`,
so compiler diagnostics, gdb and gcov attribute to the source the author edits —
the mechanism is already there and only gets a different filename.

Everything else — pin and parameter creation, function export, lifecycle, GMI,
arch guards, modparams, the stub-module fallback for non-matching architectures
— is untouched. That reuse is the entire argument for putting the frontend here
rather than shipping a separate compiler.

## 6. Staged plan

Each phase ends at something runnable. Phases 0-3 are the spine: a component
compiles and loads at the end of phase 3, and everything after widens the
language.

**0. IR foundation.** Extend `ast` with the statement and expression node set,
resolved types, storage classes, and real `TypeDef` / `FunctionBlock` /
`PureFunction` replacing the stubs. Add `Body` to `ast.Function` and the
verbatim-versus-body invariant. No behaviour change.
*Done when* the existing modcompile tests pass unmodified and `--parse` over
every shipped `.comp` produces identical JSON.

**1. ST scanner and parser.** Tokens, case-insensitive keywords and identifiers,
all three comment forms, based literals (`16#FF`, `2#1010`), typed literals
(`INT#42`), and attribute pragmas. Recursive descent to an untyped syntax tree
carrying positions. No sema, no codegen.
*Done when* `modcompile --parse foo.st` prints the syntax tree, with parser
tests covering each construct and each error message.

**2. Sema core, scalars only.** Symbol table and scope chain, expression typing
and widening rules, constant folding, positioned diagnostics, and the §4 rules.
Lower a `PROGRAM` with scalar `VAR_INPUT` / `VAR_OUTPUT` / `VAR_IN_OUT` / `VAR`
and the statement set — assignment, `IF`/`ELSIF`/`ELSE`, `CASE`, `FOR`, `WHILE`,
`REPEAT`, `EXIT` — into the IR.
*Done when* a table-driven test asserts both the accepted lowering and the
rejection of each diagnostic class: narrowing assignment, unknown identifier,
type mismatch, non-constant array bound, unbounded `WHILE`.

**3. Statement emitter, end to end.** `cgen/stmt.go`, pin snapshot and
write-back, `inst_t` members from ST `VAR`, the fault helper and rate-limiter,
`#line` back to the `.st`, and `.st` dispatch in `processFile` with the filename
check `.comp` already gets.
*Done when* an `and2.st` compiles to a `.so`, loads into `stmakd`, and a HAL
test asserts its pin behaviour.

**4. TYPE declarations and arrays.** Aliases, enumerations with literals that
resolve, `STRUCT`, one- and multi-dimensional arrays with non-zero start
indices, dependency-ordered typedefs, and array pins mapping onto
`ast.Pin.ArraySize`.
*Done when* a module driving an enumerated state machine and an array pin
passes a HAL test, including a clamped out-of-range index.

**5. Function blocks and functions.** Instance declarations, named-argument
calls, output reads, nested instances, arrays of instances, per-instance init,
and stateless `FUNCTION`.
*Done when* the `ramplimit.st` of §3 builds and runs, and tests assert the
recursion and unbound-input diagnostics.

**6. Shared state and multiple programs.** `VAR_GLOBAL` shared across programs
in a file, one exported HAL function per `PROGRAM`, and `nofp` inferred — a
program that touches no floating point exports with `fp = 0`.
*Done when* a two-program module exports `mycomp.read` and `mycomp.write` and
both are addf-able to different threads.

**7. Metadata pragmas and documentation.** Attribute vocabulary validated
against a known list, unknown attributes surfaced through `Package.Warnings`
rather than dropped, and every `docgen` field reachable from ST.
*Done when* `modcompile --document ramplimit.st` renders a man page as complete
as a `.comp` one — pin table, description, author, licence, see-also.

**8. Build wiring and corpus.** `Submakefile` wildcards for `*.st` producing
both `.so` and `.9`, `cgen/corpus_test.go` extended to sweep `.st` so every
shipped module is compile-tested, `docs/man/man1/modcompile.1` updated, and an
`st.adoc` chapter beside `docs/src/hal/comp.adoc`.
*Done when* `make` builds every in-tree `.st` and its man page, and CI is green.

### 6.1 Deferred to later work

**Standard function block library** — `TON`, `TOF`, `TP`, `R_TRIG`, `F_TRIG`,
`CTU`, `CTD` and the `TIME` type. These are what make ST worth writing for
machine logic, and they need no new compiler machinery once phase 5 lands: they
are ST source shipped with the toolchain.

**Remote live watch** — a symbol table emitted by `cgen` (name path, type,
offset) plus a GMI debug API that reads and writes by symbol. Because the IR
already carries the type information this is backend work only, and it should
cover `.comp` `variable` declarations in the same pass rather than being
ST-specific. The fault counter of §4 should become a HAL pin at the same time.

## 7. Out of scope

Named so that leaving them out is a decision rather than an omission.

- `STRING` at the HAL boundary. HAL has no string pin type and `port` is the
  wrong shape. `STRING` remains usable as `VAR` and FB state.
- Cross-file function block libraries and any import mechanism, which would need
  name resolution across compilation units.
- `LINT` and `ULINT` pins, which HAL cannot represent.
- The other IEC languages — ladder, function block diagram, sequential function
  chart. The existing `classicladder` integration already covers ladder.

## 8. Open items

**Pin name length.** `STMAK_HAL_NAME_LEN` is 127. A long component name with a
long array pin can exceed it, and truncation would silently produce colliding
pin names. Check at compile time and fail with the offending name. Small, but it
belongs in phase 3 rather than being discovered on a machine.
