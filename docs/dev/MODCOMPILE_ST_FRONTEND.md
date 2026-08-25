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
| HAL surface | A `PROGRAM`'s `VAR_INPUT` / `VAR_OUTPUT` / `VAR_IN_OUT` become `in` / `out` / `io` pins, and any variable with an `AT %I` / `%Q` / `%IO` clause, at any depth. |
| Other variables | `VAR` and `VAR_GLOBAL` become per-instance state in `inst_t`, exactly like a `.comp` `variable` declaration. |
| Sub-components | `FUNCTION_BLOCK`, internal only. An instance nests as a struct inside `inst_t` and has no HAL identity. |
| File shape | One `PROGRAM` per exported HAL function. `VAR_GLOBAL` is shared between the programs in a file. |
| Metadata | CODESYS-style pragmas — `{attribute 'license' := 'GPL'}` — feeding the existing `docgen`. |
| Live watch | Deferred in `cgen`; the IR still carries full type information so the later work is confined to the backend. |

Two of these were argued over and are worth recording with their reasons.

**A function block's I/O is not published automatically, but an `AT` clause
publishes explicitly.** Automatic publishing was considered and rejected: an
FB's `VAR_INPUT` is written by ST expression evaluation at the call site, so
making it a HAL `in` pin gives it two writers — the ST body and whatever signal
is `net`'d to it — and HAL in-pins are single-writer by design.

An `AT` variable does not have that problem, which is why it may live anywhere,
inside a function block included. It is not an FB input: nothing at a call site
writes it, and its direction is explicit in the clause rather than inferred
from which `VAR` section it sits in. `%I` means HAL writes and ST reads; `%Q`
means ST writes and HAL reads. There is no second writer to conflict with.

So a `PROGRAM`'s `VAR_INPUT`/`VAR_OUTPUT`/`VAR_IN_OUT` are the concise
shorthand for the common case, and `AT` is the explicit form that works at any
depth. Neither replaces the other — a `PROGRAM` is not called by anything, so
if its `VAR_INPUT` stopped being a pin it would have no meaning left.

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

### 3.1 Mapping a pin explicitly with AT

A variable in any `VAR` block may name the HAL pin it maps to:

```
PROGRAM mycomp
  VAR
    enable AT %I       : BOOL;    (* mycomp.enable *)
    out    AT %Q       : LREAL;   (* mycomp.out    *)
    fault  AT %Q.alarm : BOOL;    (* mycomp.alarm  *)
    level  AT %IO      : LREAL;   (* an io pin     *)
  END_VAR
```

The direction comes from the clause — `%I` in, `%Q` out, `%IO` io — and the pin
name defaults to the variable's own, written out only where the two differ.
That reads like ST and avoids saying every name twice.

An `AT` name goes through the same underscore-to-hyphen transform a
`VAR_INPUT` pin does, so `at_home` and `at-home` both name `comp.at-home`.
Consistency is the point: how a pin got its name should not change what it is
called.

This is deliberately *not* IEC's located-variable syntax, which it resembles.
`%QX0.0` in CODESYS is a physical address: memory area, size code, offset. Here
`%Q` names a direction and nothing else, because stratuMAK has no physical
address space to locate a variable in — HAL signals are the addressing scheme.
An address-shaped `AT %QX0.0` is refused rather than quietly reinterpreted.

Inside a function block, an `AT` variable becomes a pin on every instance,
prefixed by the instance path:

```
FUNCTION_BLOCK Ramp
  VAR
    pos AT %Q.position : LREAL;
  END_VAR
  ...
END_FUNCTION_BLOCK

PROGRAM mycomp
  VAR
    r    : Ramp;                  (* mycomp.r.position            *)
    axes : ARRAY[0..3] OF Ramp;   (* mycomp.axes.0.position .. .3 *)
  END_VAR
```

Two limits follow from HAL's naming, and they are the limits array pins already
have: at most one array segment in the path, since a pin name carries one
index, and the whole path must fit `STMAK_HAL_NAME_LEN`.

A publishing instance lives in a `PROGRAM`'s `VAR` rather than `VAR_GLOBAL`.
Its pins are loaded and stored by the function that owns it, and an instance
shared between two programs has no single owner to do that.

### 3.2 Type mapping

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
| `STRING [n]` | rejected — see §8 | `char[n+1]` |
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

### 3.3 Semantics worth stating

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

A `FOR` needs the same cap whenever its limit or step is *not* a constant.
`FOR i := 0 TO 7` is bounded by construction; `FOR i := 0 TO count` with `count`
read from a pin is bounded by nothing better than the integer range, and two
billion iterations of a servo thread is indistinguishable from a hang. The rule
is therefore uniform: every loop carries a bound the compiler can see, and
constant `FOR` bounds are simply the case where it can see one already.

The cap itself has a ceiling of one million. That is not a correctness limit
but a plausibility one — a loop asking for ten million iterations inside a
servo cycle has misunderstood where it is running, and saying so at compile
time beats finding out from the watchdog.

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
*Done when* the existing modcompile tests pass unmodified and the generated C
for every shipped `.comp` is byte-identical to its pre-phase-0 output:

```
# from the tree root, once per branch (main, then the phase 0 branch)
snap() {
  mkdir -p "$1"
  for f in $(find "$PWD/src/hal" -name '*.comp'); do
    ( cd "$1" && "$OLDPWD/bin/modcompile" --preprocess -o "$(basename "$f" .comp).c" "$f" )
  done
}
snap /tmp/base    # on main
snap /tmp/new     # on the phase 0 branch
diff -r /tmp/base /tmp/new
```

The `-o` argument must be *relative* and the `.comp` path identical between the
two runs. `GenerateTo` writes a `#line` directive naming the output file, so an
absolute `-o` into two different directories makes every single file differ on
that one line and the check is worthless. Verified over 100 shipped components:
relative `-o`, byte-identical.

Note this gate is on the *generated C*, not on `--parse`. That mode dumps the
AST as JSON for inspection and nothing in the build consumes it; because the
`ast` structs carry no `json` tags, Go's encoder emits every field including
zero values, so adding `Body` to `ast.Function` changes every function's JSON
by construction. A JSON diff would go red on a refactor that changed nothing
that ships. The C is the artifact that matters, and `cgen/corpus_test.go`
already sweeps the same corpus for structural invariants on every run — this
adds byte-identity for the one phase where it must hold exactly.

**1. ST scanner and parser.** Tokens, case-insensitive keywords and identifiers,
all three comment forms, based literals (`16#FF`, `2#1010`), typed literals
(`INT#42`), and attribute pragmas. Recursive descent to an untyped syntax tree
carrying positions. No sema, no codegen.
*Done when* `modcompile --parse foo.st` prints the syntax tree, every file in
`testdata/parse/` parses without a syntax error, and parser tests cover each
construct and each error message.

**2. Sema core, scalars only.** Symbol table and scope chain, expression typing
and widening rules, constant folding, positioned diagnostics, and the §4 rules.
Lower a `PROGRAM` with scalar `VAR_INPUT` / `VAR_OUTPUT` / `VAR_IN_OUT` / `VAR`
and the statement set — assignment, `IF`/`ELSIF`/`ELSE`, `CASE`, `FOR`, `WHILE`,
`REPEAT`, `EXIT` — into the IR.
*Done when* a table-driven test asserts both the accepted lowering and the
rejection of each diagnostic class: narrowing assignment, unknown identifier,
type mismatch, non-constant array bound, unbounded `WHILE`.

**Integration tests.** From phase 3, when an `.st` compiles to a loadable
`.so`, the accept fixtures should also get `tests/` runtests in the shape
`tests/halcompile/names/` already uses -- a `test.sh` that drives `modcompile`
and diffs against `expected`. Go tests cover the compiler; a runtest covers the
component actually loading into `stmakd` and its pins behaving, which is the
part a unit test cannot reach. Add them as each phase makes another fixture
compile, rather than in one batch at the end.

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

One rule fell out of implementing this, and it is a deliberate divergence from
IEC: **the `PROGRAM`s of one file may not reuse a name.** Strict IEC makes a
program's `VAR` private to it, but a file here is one component with one
`inst_t` and one HAL component — two programs declaring `n`, or both exporting
a pin called `ready`, would collide in the instance struct and at pin
registration. Qualifying each program's variables with its name was the
alternative and was rejected: a name that silently means different storage in
different programs is worse than a rename, and `VAR_GLOBAL` already exists for
what is genuinely shared.

**7. Metadata pragmas and documentation.** Attribute vocabulary validated
against a known list, unknown attributes surfaced through `Package.Warnings`
rather than dropped, and every `docgen` field reachable from ST.
*Done when* `modcompile --document ramplimit.st` renders a man page as complete
as a `.comp` one — pin table, description, author, licence, see-also.

Where a pragma sits decides what it describes, and the rule is the same at
every level: **outside** a declaration it belongs to the thing it heads,
**inside** one it belongs to the member it precedes. So metadata before the
first declaration describes the component, a pragma heading a `PROGRAM`
documents that program's HAL function, and one inside a `VAR` section documents
its pin. Getting the middle case wrong is not visibly wrong — the man page
still renders, with one pin's description silently sitting on another.

A pragma value takes the escapes an ST string literal does, `$N` among them.
The EXAMPLES section of a man page needs line breaks and `$N` is how ST spells
one.

HAL **parameters** remain unreachable from ST: there is no spelling for
`param r` / `param rw`, so the PARAMETERS section is always empty. 44 of the
123 shipped `.comp` files declare one, so this is a real gap rather than a
legacy corner — it is listed in §10 rather than settled here, because the
spelling is a language decision and not a documentation one.

**8. Build wiring and corpus.** `Submakefile` wildcards for `*.st` producing
both `.so` and `.9`, `cgen/corpus_test.go` extended to sweep `.st` so every
shipped module is compile-tested, `docs/man/man1/modcompile.1` updated, and an
`st.adoc` chapter beside `docs/src/hal/comp.adoc`.
*Done when* `make` builds every in-tree `.st` and its man page, and CI is green.

**No `.st` is shipped yet.** The obvious candidate — a rate limiter — would
duplicate `limit1`, `limit2` and `limit3`, and which component ought to be
written in ST is a product decision rather than a compiler one. So the wiring
sits ready and unexercised by the tree: `make` grows `.so` and `.9` targets the
moment a file appears in `hal/components/` or `hal/drivers/`, and the
shipped-corpus sweep picks it up in the same commit, neither needing an edit.
The rules were verified by building a real `.st` through `make` — preprocess,
compile, `.so`, man page — and then removing it.

The worked example for the manual lives at `docs/src/hal/ramp.st`, following
the `rand.comp` and `out8.comp` pattern already there: included into the
chapter rather than shipped as a component, and it compiles, so the example
cannot rot.

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

## 7. Test corpus

Fixtures live in `src/stmak/internal/modcompile/st/testdata/`, in four tiers,
because a fixture is only useful when it is clear what passing means for it:
`parse/` (parses without a syntax error, nothing more), `accept/` (parses,
passes sema, lowers to the IR), `reject/` (produces a specific diagnostic,
asserted by message *and* position), and `golden/` (compiles, loads into
`stmakd`, behaves as an accompanying HAL test asserts).

A `reject/` case that passes because the compiler crashed, or because an
unrelated earlier error fired, is worse than no case at all: it stays green
while the diagnostic it was written for silently stops working. Assert the
text.

### 7.1 What was imported

There is no public IEC 61131-3 conformance suite — PLCopen certification is a
closed programme and the annex grammar is IEC-copyrighted — so the corpus is
mostly ours to write. One import gives phase 1 a real head start.

**IronPLC** (`https://github.com/ironplc/ironplc`, MIT) contributes 21 files,
251 lines, under `testdata/thirdparty/ironplc/`, with the upstream licence and a
README recording the commit and what was excluded. MIT inside this GPL-2+ tree
needs only the notice preserved; the files are test input and reach no binary.
`debian/copyright` carries a stanza for them regardless, since it covers the
source package and the source is distributed even though tests are not.

The two files that earn the import on their own are `expressions.st` — operator
precedence across arithmetic, comparison, `MOD`, `**`, `XOR` and typed literals,
which is exactly where a hand-written recursive-descent parser goes wrong — and
`textual.st`, which exercises the whole statement set in one file.

**These are parse-level fixtures.** Most reference identifiers that are never
declared, because upstream uses them to test parsing: they are valid syntax and
invalid programs. They belong in `parse/` and will be correctly rejected by sema
from phase 2 onward. Promoting one to `accept/` means editing it, and the import
README is where that gets recorded.

Excluded on purpose: `CONFIGURATION`/`RESOURCE`/`TASK` and located variables
(`AT %IX1`), the other IEC languages, references and pointers, `WSTRING`, and
vendor extensions such as `SIZEOF` and `WORD` bit access — so that a failure in
that directory always means a real defect rather than an unimplemented feature.

### 7.2 What was deliberately not imported

MatIEC (GPL-3), RuSTy and iec-checker (LGPL-3) are all worth reading and none
were copied. The reason is value, not licence: this tree already ships GPL-3+
files under a GPL-2+ package — `tests/trajectory-planner/circular-arcs/octave/`
among them — so aggregation is settled practice here and a stanza would have
sufficed. But MatIEC's actual corpus is three negative `.st` files plus a syntax
directory, and the RuSTy and iec-checker suites live inside Rust and OCaml test
harnesses rather than as portable files.

What is worth taking from them is ideas, which carry no licence at all.
MatIEC's `tests/syntax/identifier` matrix — the same identifier used as a
datatype, function, function block, label, program, enumeration value and
variable — is a good model for the case-insensitive scoping rules of §5.2, and a
reimplementation is better than a copy because it can use our attribute syntax
and assert our diagnostics.

## 8. Out of scope

Named so that leaving them out is a decision rather than an omission.

- `STRING` at the HAL boundary. HAL has no string pin type and `port` is the
  wrong shape. `STRING` remains usable as `VAR` and FB state.
- Cross-file function block libraries and any import mechanism, which would need
  name resolution across compilation units.
- `LINT` and `ULINT` pins, which HAL cannot represent.
- The other IEC languages — ladder, function block diagram, sequential function
  chart. The existing `classicladder` integration already covers ladder.

## 9. Measured against real projects

Three existing CODESYS projects, **all of them running in production**, were
compiled to find what synthetic fixtures miss. None is in this repository,
nothing was copied from them, and all three working trees were left untouched;
what came back is a gap list and a set of fixes.

**None of them ships bare `.st`.** Two are PLCopen TC6 XML and one is a set of
CODESYS 2.3 `.EXP` text exports, so an importer for each would be a separate
tool rather than part of this frontend. One project already carries
`plcopen_extract.py`, which splits the monolithic export into a per-POU tree —
prior art for what such a tool would do. The `.EXP` form is very nearly raw ST:
its metadata rides in `(* @KEY := 'value' *)` comments, which the scanner
already skips.

The smaller project holds ten POUs, all ST, about 950 lines of body. **The
parser accepted every line of it** — the only syntax errors came from the
throwaway XML-to-ST converter's own placeholders. Sema found 365 problems,
which fell to 331 after the fixes below, and the composition matters more than
the count:

| Diagnostics | Cause |
| --- | --- |
| 156 | pointer dereference — deliberate non-feature, see §8 |
| 91 | undeclared: standard conversions (`INT_TO_REAL`, `TIME_TO_DINT`), vendor calls, and cascades from unresolved types |
| 37 | unknown type: standard function block types, vendor types |
| 25 | call-site binding against blocks the converter rendered imperfectly |
| 20 | `TIME` — deferred with the standard library |
| 1 | a genuinely unbounded `WHILE` — the §4 rule doing its job |

Pointers dominate, and they are how that project passes structures into
function blocks. `VAR_IN_OUT` is the IEC mechanism for the same thing and is
already implemented; whether that covers enough of the real usage is the
question to answer before revisiting §8.

### 9.1 Parser coverage

After the spelling fixes below, the parser accepts **59 of the 69 non-library
`.EXP` files** and all but 8 lines of the 17,649-line PLCopen project. The
remainder is accounted for rather than outstanding:

- **4 files are CODESYS project structure**, not ST — `RESOURCE`, `TASK`,
  workspace and alarm configuration. Correctly rejected.
- **2 use `VAR_CONFIG`**, now refused by name rather than as a syntax error.
- **4 use `ACTION`** — a named sub-body of a POU, invoked as `POU.Action()`.
  That is a genuine IEC construct and the one real language gap left; see §10.
- **7 of the 8 remaining lines come from one expression shape**, at five sites
  in four functions of the PLCopen project — `delete_varienties`,
  `read_varieties`, `scan_varieties` and `write_varieties`. Each is a
  named-argument `concat` whose last binding reads `STR2 := ,UINT_TO_STRING(x)`:
  a comma directly after the `:=`, with the value sitting where a third
  argument would be, as though a positional call had been half-converted to
  named arguments. Every other binding on the same line has a value.

  Whether that is a source defect or something the PLCopen export lost is not
  something this measurement can settle. The text is identical in `export.xml`
  and in the per-POU extraction, but the extraction is derived from the XML, so
  that only shows what the XML says. The project is in production, which makes
  "the export dropped something" at least as likely as "four functions have
  never worked". Either way it is not a frontend gap — nothing parses `:= ,` --
  so it is left out of the coverage figures rather than counted against them.

The spellings that had to be added, none of them exotic:

| Spelling | Uses | Now |
| --- | --- | --- |
| `word.0` — partial access | 147 | parses; sema names the missing bit-string types |
| `POINTER TO T` | 30 | parses; sema points at `VAR_IN_OUT` |
| `END_STRUCT` with no `;` | 31 | accepted, as for compound statements |
| `(a := 1, b := 2)` and arrays of them | 12 | parses; sema explains |
| `STRING(11)` | 3 | accepted beside `STRING[11]` |
| `VAR_CONFIG` | 6 | refused by name |
| `ns.Type` | 6 | parses; sema names the library |

### 9.2 What it found that was broken

**A named constant did not fold.** `FOR i := 0 TO LAST_IDX` with
`LAST_IDX : DINT := 7` in a `VAR CONSTANT` was refused as an unbounded loop,
and `ARRAY[1..MINUTES_PER_DAY]` as a non-constant bound. Real ST names its
bounds far more often than it writes them out, so this made constants useless
exactly where they are most wanted. Every synthetic fixture had used literal
bounds, so nothing caught it. A reference to a constant now resolves to its
value — including one written as an expression, `(24 * 60)` — and a constant
whose references all fold costs no storage in the instance.

**Qualified enumeration constants were missing.** `EN_STATE.OFF` is how IEC
61131-3 3rd edition and CODESYS disambiguate two enumerations that share a
constant name, and the project has exactly that: `EN_HEATPUMP_STATE` and
`EN_HEATING_MODE` both declare `OFF`. The phase 4 rule refused the second
declaration outright. Now both stand, and only an *unqualified* ambiguous use
is refused — reported where it is written, naming every candidate.

**A variable named after its own type hid the type.** `mode : Mode` is
idiomatic, and ST is case-insensitive, so the variable shadowed the enumeration
and `Mode.OFF` resolved to a field access on a variable. An enumeration has no
instance members, so the qualified reading is preferred there; a struct is
deliberately not treated that way, since `p.x` on a `p : Point` does mean the
field.

## 10. Open items

**HAL parameters.** A parameter is a value in shared memory that no signal
connects to: `rw` is a tunable the component reads, `r` a diagnostic it writes.
129 declarations across 44 of the 123 shipped components use one, and ST cannot
declare one at all. The obvious spelling is another `AT` area — IEC's `%M`
names memory that is not physical I/O, which is exactly what a parameter is —
but it needs two directions, and inventing that unilaterally is how a language
accumulates regret.

**`ACTION`.** A named sub-body of a POU, written `ACTION name: ... END_ACTION`
inside the declaring POU and invoked as `POU.name()`. Four files in one
production project use it, and it has no equivalent in what is implemented —
splitting the body into a `FUNCTION_BLOCK` changes the state layout, and a
`FUNCTION` cannot touch the POU's variables. Worth doing, and it is a real
feature rather than a spelling.


**Pin name length.** `STMAK_HAL_NAME_LEN` is 127. A long component name with a
long array pin can exceed it, and truncation would silently produce colliding
pin names. Check at compile time and fail with the offending name. Small, but it
belongs in phase 3 rather than being discovered on a machine.
