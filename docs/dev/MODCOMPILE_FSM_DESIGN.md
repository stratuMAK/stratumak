# Modcompile FSM Design

This document describes declarative finite state machines in `.comp` files: an
`fsm` block in the component header that replaces the usual hand-written
`switch (state)` with its `#define`s, timer bookkeeping and output
assignments.

## Status (October 2026)

| Slice | Scope | Status |
|-------|-------|--------|
| 1 | Scanner + parser for `fsm` blocks, AST description | ✅ Done |
| 2 | Semantic checks (names, directions, graph, sensitivity) | ✅ Done |
| 3 | C lowering + generic cgen hook | ✅ Done |
| 4 | docgen state table | ✅ Done |
| 5 | Runtests, `comp.adoc` user documentation | ✅ Done |

All slices landed. `fsm` blocks parse into `comp/fsm.go`'s structure, are
checked in `comp/fsm_check.go` (identifier scans share `comp/ctok.go`),
lowered in `comp/fsm_lower.go` to `ast.Component.GenPrologue`/`GenEpilogue`
and described in `ast.Component.FSMs`, which docgen renders as a STATE
MACHINES section with one table per fsm. The golden output for the example
below is `cgen/testdata/fsm.c`; `tests/modcompile-fsm` drives the execution
model in the servo thread; the user documentation is the "State machines"
section of `docs/src/hal/comp.adoc`. `tests/multiclick-fsm` validates the
implementation on a real component: an fsm rewrite of `multiclick` (a test
copy; the shipped `multiclick.comp` is unchanged) runs on the inputs and
expected output of `tests/multiclick`. `tests/modcompile-fsm-multi` covers
two fsms in one component and the parts of the model the first runtest does
not reach.

A review of the implementation (October 2026) led to these changes, all
reflected below: timeouts compare in whole nanoseconds; transition actions
are helper functions like `on_enter`/`on_exit`/`during`; every generated
name is checked for collisions; writes inside fsm blocks are checked like
writes in the verbatim C; `state_var` must be an `out` pin; reachability is
transitive; bracket errors name the bracket left open. A second review round
marked the helpers unused (a helper that is never called broke `-Werror`
builds), extended the collision check to the enum tag and the framework's
names, and made `enable` and `timeout` expressions reading an output an
error. A pre-merge review round attributed fsm calls to the `FUNCTION`
bodies holding them (for the "never called" and `nofp` warnings, through
parent fsms too), made every leading-zero integer literal in a timeout an
error, accepted every integer C type for `state_var`, kept gcc's columns
right after a rewritten unit literal, treated an array read as a whole as a
read of every element, named the element pin of an array `state_var` in the
man page, and shared the comment and literal skipping between the tokenizer
and the scanner. Its follow-up made fsms that run each other an error,
checked narrow `state_var` types against the number of states, left the
operand of `sizeof` out of the reads, and put the `nofp` warnings in
declaration order.

### Extensions (October 2026)

Migrating `transp_slider` (CoatV2conf) showed these gaps, closed on top of
the slices above:

- **io `state_var`, restored.** A component whose state is retained across a
  restart (HAL retain needs io-only signals) needs an io state pin. It is
  read on the first run only: a valid state number is taken over, with the
  timer at 0 and `on_enter` pending, so a state that measures time starts
  its timer anew. Afterwards the fsm keeps the state in a hidden variable and
  writes the pin every run, so a write from outside has no effect; the
  declared graph stays the only way between states. Before, an io
  `state_var` was an error and the only idiom was a dispatch state, which
  re-derives the state from a model and needs one.
- **`output` blocks.** A disabled fsm froze its transitions and set its
  outputs to their defaults, so a status output (`empty`, `full`) went to 0
  while automatic was paused. The per-state `output` block maps the state to
  the outputs in every run, enabled or not, and in reset, where it maps the
  initial state; `during` stays enabled-only, runs before the `on` items
  (see the design decision "During before the transitions") and keeps what the state does
  while it runs (fault checks).
- **`<fsm>_reset_outputs()`** writes the outputs to their defaults from user
  code, for a component that clears them at machine-off after the run,
  without writing fsm outputs outside the fsm.
- **`out` pins in `inputs`.** An out pin set by user code before the call
  (a command mux feeding a second fsm) is a legitimate input.
- **`persistent` list.** Data the fsm owns that must survive a restart with
  the state, such as a retained tray model (`tray_id`, `load`): written to
  its default on reset only. `latched` cannot hold it, since it is written
  on init too and would clear the retained value; without the list the
  reset lived in user code. Conditions read it without `inputs`; writes
  from outside (the stations feeding the model) do not warn.
- **Unlisted writes.** An fsm that declares `inputs` gets a warning for
  every pin or variable its blocks write that is in none of its written
  lists, so the lists are its whole interface.

- **Internal `on`.** `on (cond) { action }` in a state, without a target:
  the action runs and the state is not left (no exit, no entry, the timer
  runs on), but it is the match of the run, so the items after it and the
  timeout wait while it holds. A fault that holds a state up is written once,
  `on (!tray_present) { err |= ...; }` ahead of `on (target_empty) -> ...`,
  instead of a check in `during` plus the negated fault in the transition's
  condition. A self-transition is no substitute: it re-enters the state
  every cycle and restarts its timer.

`tests/modcompile-fsm-restore` covers all of them except the unlisted-write
warning, which the unit tests cover, and the internal `on`, which
`tests/modcompile-fsm-internal` covers.

## Motivation

State machines are common in `.comp` files (`carousel`, `moveoff`,
`multiclick`, `plasmac`, ...), and they are always written the same way: a
`#define` or `enum` per state, a `switch`, a hand-rolled timer, and output
assignments scattered over the cases. That costs a lot of repeated code and
invites a familiar set of bugs:

- an output set in one state and never cleared on some other path;
- a transition to a misspelled or forgotten state;
- a timer that is not reset on entry, or reset twice;
- a transition condition that reads a signal nobody thought about.

The `fsm` block declares states, transitions, timeouts and outputs once. The
frontend generates the C and checks the graph.

## Scope

- `.comp` frontend only. The feature is not IEC 61131-3 conformant, so it does
  not go into the `.st` frontend.
- Flat state machines. Several FSMs per component are allowed; hierarchical
  FSMs (one FSM run from another's `during`) work technically but are not
  supported.
- Whole arrays are not allowed in any list; single elements with a constant
  index are (see [Array elements](#array-elements)).

## Syntax

An `fsm` block is a header declaration (before `;;`) like `pin` or `variable`
and is terminated by `;`.

```
fsm test_fsm {
    inputs:    in1, in2;
    outputs:   out1, out2, out3 = 1, special_timeout;
    latched:   latched1, latched2 = 3;
    reset:     (reset_fsm);
    enable:    (enable_fsm);
    state_var: fsm_state;
    timer_var: fsm_timer;
    initial:   IDLE;

    any {
        on (estop) -> FAULT;
        timeout -> TIMEOUT;
    }

    state IDLE {
        on_enter { out1 = 1; }
        on_exit  { out2 = 1; latched1 = 1; }
        on (in1 || in2 == 1) -> WAIT_RELEASE;
        timeout (60s) -> TIMEOUT { special_timeout = 1; }
    }

    state WAIT_RELEASE {
        output { out3 = 0; }
        during { latched2++; }
        on (!in1) -> IDLE;
        timeout (wait_s);           /* handled by any { timeout -> ... } */
    }

    state TIMEOUT {
        on (ack) -> IDLE;
    }

    state FAULT {
        on (!estop && ack) -> IDLE;
    }
};
```

### Grammar

```
fsm_decl    := 'fsm' NAME '{' fsm_item* '}' ';'

fsm_item    := 'inputs'    ':' name_list ';'
             | 'outputs'   ':' def_list ';'
             | 'latched'   ':' def_list ';'
             | 'persistent' ':' def_list ';'
             | 'reset'     ':' '(' cexpr ')' ';'
             | 'enable'    ':' '(' cexpr ')' ';'
             | 'state_var' ':' NAME ';'
             | 'timer_var' ':' NAME ';'
             | 'initial'   ':' STATE ';'
             | 'any' '{' any_item* '}'
             | 'state' STATE '{' state_item* '}'

name_list   := ref (',' ref)*
def_list    := def (',' def)*
def         := ref ('=' cexpr)?
ref         := NAME
             | NAME '(' INT ')'     /* pin or param array element */
             | NAME '[' INT ']'     /* array variable element */

any_item    := 'on' '(' cexpr ')' '->' STATE action
             | 'timeout' '->' STATE action

state_item  := 'on_enter' block
             | 'on_exit'  block
             | 'during'   block
             | 'output'   block
             | 'on' '(' cexpr ')' '->' STATE action
             | 'on' '(' cexpr ')' block          /* internal, no target */
             | 'timeout' '(' texpr ')' ('->' STATE action | ';')

action      := ';' | block
block       := '{' C statements '}'
```

- `cexpr` and `block` are C, captured verbatim. The scanner balances `()`,
  `[]` and `{}` and skips string/char literals and comments. A `def` default
  ends at the first top-level `,` or `;`, so `x = fmin(a, b)` works.
- Names in the lists are the C names used in code (`in-1` is written `in_1`).
- `on_enter`, `on_exit`, `during` and `output` appear at most once per state; their
  order in the source does not matter. `on` and `timeout` items keep their
  source order (it is their priority, see below).
- A state has at most one `timeout`. `timeout (...)` without a target takes
  no action block; it is handled by `any { timeout -> ...; }`.

### Items

| Item | Required | Meaning |
|------|----------|---------|
| `inputs` | no | Sensitivity list for checks only; generates no code |
| `outputs` | no | Written to their default every cycle before the FSM runs (default `0`) |
| `latched` | no | Written to their default on init and reset only (default `0`) |
| `persistent` | no | Written to their default on reset only (default `0`); init and a restore keep them |
| `reset` | no | Level-triggered reset; omitted = never reset |
| `enable` | no | omitted = always enabled |
| `state_var` | no | Pin or variable holding the state number; omitted = hidden variable |
| `timer_var` | no | Pin or variable receiving the time in the current state, in seconds |
| `initial` | no | Initial state; omitted = the first `state` listed |

What a name may refer to:

| List | Allowed |
|------|---------|
| `inputs` | pins of any direction, variables, elements of either |
| `outputs`, `latched`, `persistent` | `out`/`io` pins, variables, elements of either |
| `state_var` | `out` or `io` `s32` or `u32` pin, integer variable |
| `timer_var` | `out`/`io` `float` pin, `double`/`float` variable |

Pins that may not exist are not allowed in the written lists (`outputs`,
`latched`, `persistent`, `state_var`, `timer_var`): a pin with an `if <personality>`
condition, or an element of a personality-sized array. Such a pin has no
storage when it is not created, and the FSM writes its list entries every
cycle. In `inputs` they are allowed, since that list generates no code.

An `io` `state_var` is read on the first run only (a restore, see the
execution model); afterwards the fsm writes it every run from a hidden
variable, so a write from outside is no `goto` around the declared graph.
An `out` pin in `inputs` is read as user code left it before the call; the
fsm's own outputs are still an error there. `state_var` and `timer_var` may be array
elements (`state_var: st_arr(1);`). An index may carry an integer suffix
(`in_arr(1u)`).

An `inputs` entry listed twice, or one that is also an output of the same
fsm, is an error.

### Array elements

Whole arrays are never allowed in a list. A single element is, written the
way it is accessed in code:

- pin and param arrays use the function-like accessor: `in_arr(1)`;
- array variables use C indexing: `buf[1]`.

The index must be an integer literal and is checked against the declared size.
For the checks, a condition reading an element with a literal index must find
that element in `inputs` / `latched`; an element read with a computed index
(`in_arr(i)`), or an array read as a whole (`sum(buf)`), cannot be matched
and gives a warning. An array read as a whole counts as a read of every
element for the unread-input check. Likewise, a write outside the FSM to an
output array with a computed index gives a "may overwrite" warning.

Default values are C expressions and are evaluated every time they are
applied, so `out3 = some_param` follows the param.

### Timeouts

The `timeout` expression is in seconds (double). Numeric literals may carry a
unit suffix that the frontend rewrites to a scale factor:

| Suffix | Rewritten to |
|--------|--------------|
| `s` | `(N)` |
| `ms` | `(N * 1e-3)` |
| `us` | `(N * 1e-6)` |
| `ns` | `(N * 1e-9)` |

So `timeout (60)`, `timeout (60s)`, `timeout (wait_s)` and
`timeout (wait_ms * 1ms)` all mean what they say. The expression is evaluated
every cycle, so a param change takes effect at once. A value `<= 0` means "no
timeout". The timer itself counts integer nanoseconds from `period`, so it
does not drift.

The timeout is rounded to whole nanoseconds before it is compared with the
timer, so a timeout that is a multiple of the period fires exactly on that
cycle. Compared as a double, `0.3 * 1e9` is slightly above `300000000`, and
a 300 ms timeout fired at 301 ms on a 1 ms period. A timeout of 292 years or
more (beyond `int64` nanoseconds) never fires.

Unit suffixes are rewritten only inside `timeout (...)`; anywhere else
(`on (fsm_timer >= 5ms)`) they are a C error. An integer literal with a
leading `0` (`010s`, `010`) is an error, since C would read it as octal and
nobody means eight seconds. The rewritten literal is generated text; the
user text around it keeps its `#line` mapping and column, so a C error after
a unit literal is still reported where it is in the `.comp`.

> Note: the discussion first proposed integer-nanosecond timeout expressions
> (`1s` = `1000000000LL`). That makes `timeout (wait_s)` silently mean
> nanoseconds, so the expression is in seconds instead.

## Execution model

The FSM is run by calling `test_fsm();` from a `FUNCTION`. It must be called
exactly once per cycle; every call advances the timer by `period`.

One run:

1. **Outputs.** The `reset` expression is evaluated first, so it sees the
   outputs as the previous run left them. Then every `outputs` entry is
   written to its default (the helper behind `<fsm>_reset_outputs()`). This
   happens in every run, including while reset or disabled.
2. **Reset / init.** On the first run, and in every run in which `reset` is
   true: state = initial, timer = 0, every `latched` entry written to its
   default, `on_enter` marked pending. `persistent` entries are written to
   their default only when `reset` is true, never on init or a restore. On the first run without reset, an
   `io` `state_var` holding a valid state number gives the state instead of
   `initial` (a restore). `timer_var` is set to 0. No `on_exit` runs; a
   reset is an abort, not a transition. Reset works while disabled. A run in
   which `reset` is true continues as a disabled run (step 3): the `output`
   of the initial state runs, nothing else.
3. **Enable.** If `enable` is false, or `reset` is true, steps 4 to 6 are
   skipped; `output` still runs. State, timer and a pending `on_enter` are
   frozen.
4. **Pending entry.** If `on_enter` is pending (first run, or first enabled
   run after reset or restore): run `on_enter` of the current state and
   nothing else up to `output`. `during` and the transitions start with the
   next run, as after a transition.
5. **Timer and during.** timer += `period`; `timer_var` = timer × 1e-9.
   Then `during` of the current state runs: the state stays this far, and
   does its continuous work before anything decides to leave it.
6. **Transitions.** The first match, in this order, fires:
   1. `any` `on` items in source order, skipping those whose target is the
      current state;
   2. the state's `on` items in source order;
   3. the state's `timeout`, if it has a target;
   4. the `any` `timeout`, if the state's `timeout` has no target.

   A firing transition runs: `on_exit` (old), transition action, state = new,
   timer = 0 and `timer_var` = 0, `on_enter` (new). At most one transition per run. A
   self-transition (`A -> A` inside `state A`) runs exit and enter and resets
   the timer.

   An internal `on` (no target) that matches runs its action only: the
   state stays, no exit or entry runs, the timer is not reset. It is the
   match of the run all the same, so the items after it, the state's
   `timeout` and the `any` `timeout` included, are not checked. The timer
   keeps running meanwhile: a timeout that fell due fires in the first run
   in which the internal `on` no longer matches.
7. **Output.** An `io` `state_var` is written. Then `output` of the current
   state runs, which is the new state if a transition fired, also while
   disabled. `output` may write the fsm's `outputs` only: it is the mapping
   from the state to the outputs, and may read anything (an input refining
   an output). `during` must not write the fsm's outputs: it runs before the
   transitions, so the value would carry over into the next state for the
   cycle a transition fires.

So each run of a state is either entering it (`on_enter`: entry run or
transition) or staying in it (`during`, then the decision), and the cycle a
state is left still runs its `during`. Since an action runs after `during`,
it overrides what `during` wrote: `during { tray_id = 0; }` keeps an idle
state's id cleared every cycle, and `on (load) -> FEED_IN { tray_id =
tray_id_in; }` after it still takes the new id.

`timer_var` is therefore always current: `during`, the `on` conditions and the
`timeout` check see the same time. A
timeout with several targets is written as `on` items on `timer_var` in front
of the `timeout`:

```
on (fsm_timer >= delay && pj_needed) -> PUDDLE_JUMP;
timeout (delay) -> CUT_HEIGHT;
```

A plain `variable double` is enough as `timer_var` when no pin is wanted.

Consequences worth stating in the user documentation:

- An output written in `on_enter`, `on_exit` or a transition action is a
  **one-cycle pulse**. An output that should hold while in a state is written
  in `output`.
- `on_enter` always runs on entering a state, including after init and reset.
- While reset is held, the fsm sits in the initial state with latched and
  persistent values at their defaults; only the initial state's `output` block runs. User code
  that needs every output off during a reset (or at machine off) calls
  `<fsm>_reset_outputs()` after the run.

## Checks

### Errors

- Unknown target state; duplicate state; `initial` naming an unknown state.
- A name in a list that is not a declared pin or variable, or has the wrong
  direction/type for its list (see the table above).
- A name in more than one of `outputs` / `latched` / `state_var` /
  `timer_var`, or an output listed in two FSMs.
- An `on` condition, the `enable` expression or a `timeout` expression that
  reads an FSM output. Outputs always hold their default at that point, so
  this is a bug (`enable: (o)` with default 0 never runs the fsm). `reset`
  is read before the outputs are written and is exempt.
- A state with `timeout (...)` without target and no `any { timeout -> ...; }`.
- A `during` block assigning one of the fsm's `outputs`: it runs before the
  transitions, so the value would carry over into the next state. With a
  computed index into an array of which only some elements are outputs, it
  is a warning naming them.
- An `on` without a target in `any` (it would hold back every state), or
  one with neither a target nor an action (`on (x);`).
- Duplicate `on_enter` / `on_exit` / `during` / `timeout` in a state.
- An `on` condition that reads an output array through a computed index
  when every element of the array is an output. When only some are, it is
  a warning naming them.
- A generated name (see "Generated code") that collides with another
  generated name, with a pin, param, variable or modparam, with a C keyword,
  or with a name the generated file defines (`period`, `fperiod`,
  `personality`, the `math.h` functions, `inst_t`, `inst_start`, `New`,
  `funct_<function>`, ...), or that starts with a prefix of the headers it
  includes (`hal_`, `rtapi_`, `stmak_`, `cmod_`, `mcode_`, and the same in
  upper case). Other C library names (`EXIT_SUCCESS`, `uint8_t`) are not
  checked; a collision there is reported by the C compiler. The enum tag
  `<fsm>_state` is checked against declared and framework names only, since
  C keeps tags apart from other names (a state may be named `state`).
  Examples: a state named `run` (`<fsm>_run`) or `in` (`<fsm>_in`), an fsm
  `feed` next to a pin `feed_in`, an fsm named `if` or `fabs`, a pin
  `<fsm>_state`, an fsm `inst` with a state `start`. fsm names starting with
  `__` are reserved.
- An `output` block assigning a declared pin or variable that is not one of
  the fsm's `outputs` (locals of the block pass); through a computed index,
  unless every element of the array is an output.
- A timeout integer literal with a leading `0`
  (`010s`, `010`), or a unit on a number that is not decimal (`0x10s`).
- An expression or default that holds only comments (`on (/* x */)`).
- fsms that run each other in a cycle, an fsm running itself included
  (`m` calls `n()` in a block and `n` calls `m()`): the calls would recurse
  without end in the realtime thread.
- A `state_var` variable of a narrow type that cannot number every state
  (`int8_t`/`char` up to 128 states, `uint8_t` 256, 16-bit types 32768 and
  65536).
- Unbalanced brackets in captured C. The message names where the bracket
  left open was opened; a `;` inside a condition outside braces is reported
  as a missing `)`. Brackets are counted in all captured text, so code inside
  `#if 0` must still balance. A `//` comment ending in a backslash is an
  error: C continues it onto the next line, and the capture would not.

### Warnings

- **Sensitivity:** an `on` condition reads a pin or variable that is not in
  `inputs`, `latched` or `persistent` and is not this FSM's `state_var` /
  `timer_var`.
  Params are exempt (configuration, like VHDL generics). Identifiers that are
  not declared pins/params/variables (C functions, macros, enum constants,
  locals) are ignored, which keeps the check free of false positives.
  Skipped when the FSM has no `inputs` list.
- **Unread input:** an `inputs` entry that does not appear in any piece of
  the fsm's C: conditions, actions, `on_enter`/`on_exit`/`during`, defaults,
  and the `reset`, `enable` and `timeout` expressions.
- **Write outside the FSM:** the verbatim C after `;;` assigns an FSM output,
  `state_var` or `timer_var` (`=`, compound assignment, `++`, `--`). An
  output and `timer_var` are overwritten on the next run; the state changes
  only through transitions. The blocks of one fsm are checked the same way
  against every other fsm, and against their own fsm's `state_var` and
  `timer_var` (an fsm's own outputs are its to write). Heuristic: writes
  through pointers or macros are not found.
- **Unlisted write:** an `on_enter`, `on_exit`, `during` or action block
  assigns a pin or variable that is in none of the fsm's `outputs`,
  `latched` and `persistent` (an entry listed only in `inputs` included),
  so the fsm's lists show everything it writes. Params (configuration, not
  listable), locals, the fsm's own `state_var` / `timer_var` (warned above)
  and entries another fsm writes by its lists are exempt. A write with a
  computed index warns unless every element of the array is listed.
  Skipped when the FSM has no `inputs` list, like the sensitivity check.
- Unreachable state: not reached from the initial state along the
  transitions. `any` `on` items lead out of every reachable state; the `any`
  `timeout` only out of reachable states whose `timeout` has no target.
- State without a way out (no own `on`/`timeout` and no `any` transition
  leading elsewhere; an internal `on` is no way out).
- `test_fsm()` never called: neither in the verbatim C nor in another fsm's
  blocks (a child fsm run from its parent's `during`). A child whose parent
  is never called is not reported again; since cycles are an error, every
  such child has a parent that is reported.
- An `any` `timeout` that no state uses (no `timeout` without a target), and
  an `any` `on` that cannot fire because its target is the only reachable
  state.
- An fsm with a `timeout` or `timer_var` (the floating-point parts of the
  generated code) is run from a `nofp` function. A call is attributed to the
  `FUNCTION(name) { ... }` body holding it, through parent fsms, and to the
  one function when the verbatim C has no `FUNCTION` (cgen wraps it). A call
  from a helper of the user's cannot be attributed; it is reported, at the
  fsm, when every function is `nofp`. The warnings come in the order the
  functions are declared.

The `timeout`, `reset` and `enable` expressions are not part of the
sensitivity check.

All identifier scans share one C tokenizer: strings, char literals and
comments are skipped, and names after `.` or `->` (member access) are not
identifiers of the component. The operand of `sizeof` is not evaluated and
is not a read. The tokenizer, `CaptureC` and the header
scanner step over comments and literals with the same helpers; only what
they do at the end differs (`CaptureC` reports an unterminated literal or a
`//` comment ending in a backslash, the header scanner does not splice).

## Generated code

For the example above, roughly:

```c
/* prologue, before the user code */
enum test_fsm_state {
    test_fsm_IDLE = 0,
    test_fsm_WAIT_RELEASE = 1,
    test_fsm_TIMEOUT = 2,
    test_fsm_FAULT = 3,
};
static void test_fsm_run(inst_t *__comp_inst, long period);
#define test_fsm() test_fsm_run(__comp_inst, period)
#define test_fsm_in(s_) (fsm_state == test_fsm_ ## s_)
static const char *test_fsm_state_name(int s);

/* after the user code, so actions can use the user's helpers */
static void test_fsm_run(inst_t *__comp_inst, long period) {
    int __rst = (reset_fsm);

    out1 = 0; out2 = 0; out3 = 1; special_timeout = 0;

    if (!__fsm_test_fsm_init || __rst) {
        __fsm_test_fsm_init = 1;
        fsm_state = test_fsm_IDLE;
        __fsm_test_fsm_timer = 0;
        fsm_timer = 0;
        latched1 = 0; latched2 = 3;
        __fsm_test_fsm_enter = 1;
        if (__rst) goto out;
    }
    if (!(enable_fsm)) goto out;

    if (__fsm_test_fsm_enter) {
        __fsm_test_fsm_enter = 0;
        switch (fsm_state) { /* on_enter blocks */ }
        goto out;
    }

    __fsm_test_fsm_timer += period;
    fsm_timer = __fsm_test_fsm_timer * 1e-9;

    switch (fsm_state) { /* during blocks */ }

    switch (fsm_state) {
    case test_fsm_IDLE:
        if ((estop)) { /* exit IDLE */ fsm_state = test_fsm_FAULT; /* timer = 0, fsm_timer = 0, enter FAULT */ break; }
        if ((in1 || in2 == 1)) { ... break; }
        { double __to = (60); if (__to > 0 && __fsm_test_fsm_timer >= __to * 1e9) { ... } }
        break;
    ...
    }

out:
    switch (fsm_state) { /* output blocks */ }
}
```

The implementation differs from this sketch in form, not behaviour:
`on_enter`, `on_exit`, `during` and transition actions are helper functions
(`__fsm_test_fsm_enter_IDLE()`, `__fsm_test_fsm_action_0()`, ...), so there
are no `goto`s, a `return` in a block only ends that block (a transition
still completes), and a stray `break` cannot leave the generated `switch`.
The timeout comparison is
`__to > 0 && __to < 9.2e9 && timer >= (int64_t)(__to * 1e9 + 0.5)`.
The helpers and `<fsm>_run` are marked `__attribute__((unused))`: some are
never called (the `on_exit` of a state without a way out, the action of an
unused `any` item, an fsm that is never run). The checks warn about those
instead of a `-Werror` build failing.
`cgen/testdata/fsm.c` is the authoritative output.

Names the user sees: the enum constants `<fsm>_<STATE>`, `<fsm>_run`,
`<fsm>_state_name`, and the macros `<fsm>()`, `<fsm>_in()` and
`<fsm>_reset_outputs()`. Everything
else is prefixed `__fsm_<fsm>_`, which is reserved.

- State numbers follow source order, starting at 0. `initial` does not
  renumber.
- Hidden per-instance variables (`__fsm_<name>_init`, `_enter`, `_timer`, and
  `_state` when `state_var` is omitted or an `io` pin) are added as ordinary
  `ast.Variable`s, so every instance has its own FSM.
- User code fragments are emitted with `#line` directives pointing into the
  `.comp`, so compiler errors land on the right source line.
- `test_fsm_state_name()` returns the state name as a string, for messages.
- There is deliberately no `goto` from outside the FSM; the declared graph is
  the only source of transitions.

## Patterns

The `fsm` block is deliberately strict. Code that does not fit is restructured
rather than supported with more syntax; the idioms below cover what an
analysis of existing components (see [Fit with existing code](#fit-with-existing-code))
turned up.

### Resume from a model

A component that must continue from a model (pins, a tray model) after
init, reset or setup mode starts in a dispatch state, listed first:

```
reset: (!enable);

state RESUME {
    on (target_load) -> FEED_OUT;
    on (load)        -> FEED_IN;
    on (tray_id)     -> SETTLE_IN;
    on (1)           -> EMPTY;
}
```

Costs one extra cycle after init or reset (entry run, then the dispatch).
When the state itself is retained, an `io` `state_var` restores it directly
instead.

### Manual / setup mode

Setup mode is an ordinary state, not a feature of the block. Manual buttons
latch into user variables; the state's `output` drives the outputs from them:

```
any { on (switch_setup) -> MANUAL; }

state MANUAL {
    output { stop_release = manu_release; }
    on (!switch_setup) -> RESUME;
}
```

The state pin shows MANUAL while in setup.

### Faults

A fault that only holds a state up, and is reported through an error word,
is an internal `on` ahead of the transitions it blocks:

```
state FULL {
    output { full = 1; }
    on (!tray_present) { err |= ERROR_TRAY_MISSING; }
    on (target_empty)  -> FEED_OUT { target_load = 1; }
}
```

A fault with behaviour of its own (a message, other outputs, its own way
out) becomes a state of its own instead of a guard repeated in every `on`.
The message goes in `on_enter`, so it is logged once per occurrence without
edge detection (`err_last`):

```
state FULL {
    output { full = 1; }
    on (!tray_present) -> FAULT_NO_TRAY;
    on (target_empty)  -> FEED_OUT { target_load = 1; }
}
state FAULT_NO_TRAY {
    on_enter { STMAK_LOG(STMAK_LOG_ERROR | STMAK_LOG_OPER, "..."); }
    output   { err = 1; }
    on (tray_present) -> FULL;
}
```

### Decisions computed in the state

Code that computes the next step (a helper returning a status, a planning
loop) runs in `during` and leaves its result in a `latched` entry or a
variable; `during` runs before the transitions, so an `on` item reacts to
it in the same cycle.

### Requests from outside, child FSMs

There is no goto from outside. A request is a flag that an `any` transition
(or a state's `on`) consumes; the action clears it. A child FSM is started
and observed through a go/done handshake in variables, the same way two
components hand over through io pins.

### Other timers

The FSM timer measures time in the current state only. Watchdogs, timers that
span several states, timers armed elsewhere or paused under a condition stay
ordinary C timers in user code.

### State in `option data`

Lists accept pins and variables only. State, flags and timers kept in an
`option data` struct move to `variable`s.

### Operating mode

An operating mode spread over boolean flags (homing, running, error, re-init)
is a state machine without a declared graph, and nothing prevents inconsistent
flag combinations. It becomes an FSM of its own, and its `<fsm>_in()` macro
enables the sequence FSMs:

```
fsm mode {
    any { on (is_error) -> ERROR; }
    state READY  { on (to_base) -> HOMING; on (go) -> RUNNING; }
    state ERROR  { on (go) -> REINIT { init_all(); } }
    state REINIT { on (init_done) -> READY; }
    ...
};

fsm seq {
    enable: (mode_in(RUNNING));
    ...
};
```

### One signal from several FSMs

An output belongs to one FSM. When two FSMs contribute to one signal (an
error pin), each gets its own output and user code combines them:
`err = flow_err || coil_err;`.

## Implementation

### 1. Scanner + parser (`comp/`)

- New declaration keyword `fsm` in `parseDeclaration`.
- Scanner support for `{`, `}`, `->` and a raw mode that captures a balanced
  C fragment with its `Pos`.
- Parse into a frontend-local FSM structure.

### 2. Checks (`comp/`)

Run after the whole header is parsed, since lists refer to pins and variables
declared anywhere in the header, and the write check needs the verbatim C.

### 3. Lowering + cgen hook

The frontend lowers each FSM to:

- hidden `ast.Variable`s;
- C text for a prologue (enum, forward declaration, macros) and an epilogue
  (function definitions), each fragment with its source `Pos`.

cgen gets one generic, FSM-agnostic hook in `ast.Component` (e.g.
`GenPrologue` / `GenEpilogue` as positioned fragments), emitted directly
before and after `emitUserCodeBody()`, while the convenience macros are still
defined. cgen learns nothing about FSMs.

### 4. docgen

`ast.Component` gets a descriptive `FSMs` field (name, states with numbers,
transitions with conditions as text, timeouts, outputs, latched values). It
is used only by docgen, which adds a state table per FSM to the man page.

### 5. Tests and documentation

- Parser and check unit tests, a cgen golden test, a corpus entry.
- A cgen test (`fsm_cc_test.go`) that compiles the generated C with gcc
  `-Wall -Werror`, as modcompile builds modules: the golden component, the
  fsm runtest components and edge cases (helpers no transition calls, `return`
  in every kind of block, two machines). A text comparison cannot see what
  only the compiler reports, such as a function defined but never used. It
  also checks that an error after an umlaut is reported at the right column.
  Skipped without gcc.
- `tests/multiclick-fsm`: an fsm rewrite of `multiclick` as a test copy,
  checked against `tests/multiclick`'s expected output.
- `tests/modcompile-fsm-multi`: two fsms in one component, source order
  between `any` items, `any` against a state's own `on`, `on_exit` on a
  self-transition, the `any` timeout, a timeout in ms re-read every cycle
  (including `<= 0`, and one a double comparison fired late), expression
  defaults, a hidden `state_var`, `initial` other than the first state, and
  `_in()` / `_state_name()` across fsms.
- A runtest that drives a test component through the execution model in the
  servo thread: init, reset held/released, disable freezing state and timer,
  `any` priority and self-target skipping, timeout last, pulse outputs, the
  entry run not advancing the timer, and a `return` that leaves `during`
  early.
- The byte-identical `.comp` gate, run by hand against `main` while the
  branch was open: no existing `.comp` may change one byte of generated
  output.
- User documentation in `docs/src/hal/comp.adoc`.

## Fit with existing code

An estimate (October 2026) of what converting existing components would
save, counted on FSM-related lines and sketched `fsm` blocks:

| Code base | FSM lines | As `fsm` | Saved |
|-----------|-----------|----------|-------|
| stratuMAK tree (multiclick, eoffset_per_angle, carousel, moveoff, plasmac, ...) | ~2450 | ~2060 | ~340 |
| CoatV2conf components (transp_*, coating_cam, coating_pnp, ...) | ~1640 | ~870 | ~770 |

Most stratuMAK state machines are arithmetic behind a `switch`; outputs hold
across states and the C stays. Only `multiclick` is a clean fit, and
`plasmac`, `carousel` and `moveoff` should stay as they are. The CoatV2conf
components are classic sequencers (wait for a signal, act, time out, hand
over) and fit well once restructured along the [Patterns](#patterns).

A PLC project in Structured Text (paint line portal, 173 POUs) was checked
for comparison. Its sequences are numbered step chains in `IF/ELSIF`
(`ELSIF step = 40 AND cond THEN actions; step := 50;`), which map one to one
onto `on (cond) -> S50 { actions }`. The `AND Automatik` repeated in 60 step
conditions is `enable`, the shared timer handshake used by about 15 steps is
`timeout`, and the operating mode lives in boolean flags (see
[Operating mode](#operating-mode)). Nothing new was needed.

## Design decisions

- **Braces and explicit targets.** Indentation blocks don't fit a header made
  of `;`-terminated declarations, and `:` is ambiguous with C's `?:`. A target
  in the transition header keeps the graph static: checkable, documentable,
  and at most one transition per cycle.
- **Outputs reset every cycle.** Outputs depend on the state only; no output
  can stick because a path forgot to clear it.
- **No types in the FSM block.** `x = 0` is valid C for every scalar HAL type,
  and the declarations already carry types for the direction checks.
- **Init keeps the model, reset clears it.** `latched` is for what a run
  produces (errors, counters): cleared on init and reset. `persistent` is
  for what the state stands for (a tray model): a restore brings the state
  back, so init must not clear the model it belongs to; only a reset, which
  also sends the state to `initial`, does.
- **Enable freezes, reset dominates.** Disabling pauses the machine, timer
  included; its `output` blocks keep showing the state. Reset works in any
  situation: it resets state, timer and `latched` entries only, and the
  outputs follow from the initial state through its `output` block, as in a
  disabled run. Outputs at their defaults would belong to no state.
- **Restore re-enters.** A restored state is entered like any other:
  `on_enter` runs and the timer starts at 0. Only the state is retained;
  time spent in it before a restart is not, and a settle or dwell time
  starts over rather than being cut short.
- **During before the transitions.** `during` is what a state does while it
  stays: it runs before the `on` items, like the body of a hand-written
  `case` ahead of its `if`s, so a decision sees this cycle's work and an
  action overrides it (an idle state keeps an id cleared in `during`; the
  transition out sets the new one). The entering cycle runs `on_enter`
  only. `during` therefore must not write outputs, which would carry over
  into the next state; outputs come from `output`, which runs after the
  transitions in the current state. Before `output` blocks existed,
  `during` set the outputs and had to run after the transitions in the new
  state; that order was dropped with them.
- **Timeouts last.** A real event that arrives in the same cycle as the
  timeout wins.
- **An internal `on` blocks, the timer runs on.** It is a guard: while it
  matches, the state makes no progress, timeout included. The time in the
  state is not paused, so a settle time that ran out under the fault does
  not start over when it clears; pausing it would be a second mechanism.
  Internal `on` items are not allowed in `any` for now: there they would
  hold up every state; allowing them later breaks nothing.
- **`any` replaces a global `on_timeout`.** One mechanism for every
  state-independent transition; skipping self-targets keeps
  `any { on (estop) -> FAULT; }` from re-entering FAULT every cycle.
- **No opt-out from `any`.** `any` is already a convenience; a state that
  must not take an `any` transition writes its transitions per state instead.
- **No HAL function binding.** FSMs mostly run alongside other logic in the
  same component, so the user calls `test_fsm()` from their own `FUNCTION`
  where it fits; exporting the FSM as a separate HAL function is not planned.
- **Strict over convenient.** The goal is clean state machines, not covering
  every existing one. Things the analysis of existing code asked for and that
  have a clean idiom are not added as syntax: no `on_init`/resume block (a dispatch state), no manual mode
  (an ordinary state), no transition guard (a fault state), no external goto
  (request flags), no timer hold or multi-state timers (user C timers).
- **No explicit state numbers.** States are numbered in source order. Code
  that must show its own step numbers (e.g. 10, 20, 30 with gaps) assigns
  them to a `u32` output in `output`.
- **No single-step support.** Single-step mode is an edge detect on the step
  button added to the `enable` expression: `enable: (go || step_edge);`.
- **`timer_var` is always current.** It is written when the timer advances
  and when a transition resets it, so conditions and `during` see the live
  value; no separate elapsed-time accessor is needed.
- **`.comp` only.** Not part of IEC 61131-3, so it stays out of `.st`; cgen
  only gets a generic hook.

## Open items

1. Graphviz state diagram from docgen in addition to the state table.
2. The floating-point check attributes calls to `FUNCTION` bodies; a call
   from a helper function of the user's is attributed to no function and
   only warns when every function is `nofp`.
3. The fsm timeout fires at `timer >= timeout`; the hand-written
   `multiclick` uses `timer > timeout`. Every timeout that is a multiple of
   the period (its 250 ms defaults at 1 ms) fires one period earlier in the
   fsm rewrite; others fire on the same cycle. The runtest's timings are not
   multiples and match cycle for cycle. `multiclick_fsm.comp` lists this and
   its other differences from the original.
