# Realtime functions in Go modules

**Status:** draft for review.
**Related:** [`RT_HARDENING_CHECKLIST.md`](RT_HARDENING_CHECKLIST.md),
[`SAFETY_BOUNDARY.md`](SAFETY_BOUNDARY.md), [`PNPTASK_DESIGN.md`](PNPTASK_DESIGN.md)

---

## 1. The asymmetry

A C module and a Go module already share a lifecycle. `pkg/stmak/stmak.go:20`
says so outright — *"Mirrors the cmod lifecycle: factory → Start → Stop →
(LateStop) → Destroy"* — and the launcher treats them as peers.

What they do not share is a capability surface:

| | C module | Go module |
|---|---|---|
| handed at construction | `cmod_env_t *env` | `(ini, logger, name, args)` |
| pins and params | `env->hal->pin_*_new` | `pkg/hal` |
| GMI apis | `env->api` | generated bindings |
| logging | `env->log` | `slog` |
| **RT function export** | `env->hal->export_funct` | **nothing** |
| **RT-hardened allocator** | `env->rtapi->calloc` | **nothing** |

The consequence is a language choice made for the wrong reason: a module that
needs *one* cyclic function has to be written entirely in C, however little of
it is actually realtime. `pnptask` is the current example — a 12 000-line Go
module whose only realtime need is a point-in-polygon test.

The proposal is to close the last two rows.

## 2. What is not being proposed

**Go code in the servo thread.** `RT_HARDENING_CHECKLIST.md` §0 records the
invariant as audited fact:

> the RT cycle dispatches only C function pointers (`hal_lib.c` `thread_task`) —
> **no Go in the cycle by construction**

That stays true. "RT in a Go module" means the module *contains* a C function
and registers it; the function itself is C and calls no Go. A cgo call into Go
from the servo thread means scheduler entry, possible stack growth and GC
interaction, and it is invisible to `-Wfunction-effects`, which cannot see
across the cgo boundary. The checker would report green over precisely the code
that broke the RT path.

So the API must make that structurally impossible, not merely discouraged — see
§4.

**Anything machine-specific.** This is platform infrastructure and its first
customer is a generic pnptask pin. Machine logic — a sphere that must not close
on a portal head, a chuck interlock — belongs in the machine's own components
and configuration, per `SAFETY_BOUNDARY.md` §1.

## 3. Memory model

The pattern is the one the EtherCAT driver already uses, and it is why no
synchronisation is needed:

```c
/* hal/drivers/ethercat/main.c:635 — init time, non-RT */
pdo_entry_regs = env->rtapi->calloc(env->rtapi->ctx,
                                    sizeof(ec_pdo_entry_reg_t) * (n + 1));
```

High-level work happens once at init, in whatever language suits it, and
assembles a flat structure with the RT-hardened allocator. The cyclic function
then walks that structure and nothing else.

Three properties follow, and together they remove the whole class of concern
that would otherwise apply:

- **Same address space.** Go modules are compiled in, C modules are `dlopen`ed
  into the same process. There is no shared-memory boundary to lay out and no
  marshalling.
- **The GC never sees it.** The structure lives in `rtapi->calloc` memory, so it
  is not Go-heap and there is nothing to pin, move or collect. This is already
  established practice: `RT_HARDENING_CHECKLIST.md` §0 records shared memory as
  C-allocated via `rtapi_calloc`, and the halscope RT↔Go ring as C-allocated
  with C11 acquire/release atomics used identically from both sides.
- **Immutable after init.** Nothing writes it once the threads are running, so
  the cyclic reader needs no lock, no atomics and no double-buffer.

A structure that needed to *change* at runtime would need the halscope
treatment (explicit atomics, or a published pointer swapped with release
semantics). Nothing here does, and a design that starts needing it should be
reviewed rather than bolted on.

### cgo pointer rules

Go pointers may be passed to C for the duration of a call and must not be
retained. Init-time assembly therefore **copies** out of Go slices into the
`rtapi->calloc` block while the call is on the stack. It never stores a Go
pointer in C memory.

## 4. The exposed surface

Two halves, deliberately different in shape.

### To the C half: the real `cmod_env_t`

C inside a Go module uses `stmak_hal.h`, `stmak_rt_check.h` and the same `env`
a C module gets. The point is that the C is *identical* to cmod C — same
headers, same `STMAK_NONBLOCKING` annotations, same review idioms — so the
project has one realtime story rather than two.

### To the Go half: a narrow wrapper

Only what Go itself calls. Not the raw struct: `pkg/hal` stays idiomatic.

```go
// Register a cyclic function. fn is a C function pointer obtained as
// C.my_rt_funct — there is deliberately no overload taking a Go func.
func (c *Component) ExportFunct(name string, fn hal.CFunct, arg unsafe.Pointer,
                                usesFP, reentrant bool) error

// The RT-hardened allocator, for structures a cyclic function walks.
func RTCalloc(n uintptr) unsafe.Pointer
func RTFree(p unsafe.Pointer)
```

`hal.CFunct` is a named type over `unsafe.Pointer` that only a
`C.<name>`-derived value can populate in practice. **There must be no code path
that accepts a Go `func`** — that single restriction is what keeps §2's
invariant structural instead of documentary. The underlying C signature is
already annotated for the checker:

```c
/* pkg/cmodule/stmak_hal.h:45 */
typedef void (*stmak_hal_funct_t)(void *arg, long period) STMAK_NONBLOCKING;
```

`log` and `api` need no equivalent: Go has `slog` and the generated GMI
bindings.

### Where the C lives

**In a real `.c` file beside the Go source, not in the cgo preamble.** A normal
translation unit is what `make rt-effects-check` can compile with
`-Wfunction-effects`; a cgo preamble is compiled by cgo's own invocation and is
not covered. The preamble declares the function, the `.c` file defines it.

Adding that file to the check target is **part of this change, not a follow-up**
— otherwise the regime silently stops covering the newest RT code in the tree,
which is the worst possible failure mode for a checker.

## 5. Lifecycle

`export_funct` is called **in the factory**, matching cmods, which export from
`New()`. HAL `addf` lines execute after all loads and before `Start`, so a
function exported any later would not exist when its `addf` runs.

Teardown is the sharp edge and must mirror the cmod contract rather than invent
one. The cyclic function must have stopped being called before the memory it
walks is freed:

1. the component's functions are removed (cmods get this from `hal_exit`),
2. every realtime thread completes a full cycle,
3. only then may `Destroy` release the `rtapi` block.

The launcher already knows how to wait for a full RT cycle — it does so between
`Stop` and `LateStop` (`pkg/stmak/stmak.go:57`). The same barrier is what makes
free-after-unexport safe here.

**Runtime unload is the case to get right.** `unloadGoModule` exists and removes
a module while the machine runs, so this is not a shutdown-only concern: a
free that races a still-registered function is a use-after-free inside the
servo thread. The design must state explicitly which side removes the function
and where the barrier sits; "it works at shutdown" is not sufficient evidence.

## 6. First customer: pnptask's dead-zone clearance

Chosen because it is small, entirely generic, and exercises the whole path: Go
parses and offsets at load, C assembles at init with the RT allocator, a cyclic
function walks it, and `-Wfunction-effects` verifies the result.

### What does not change

`deadzone.N.free` keeps its name, its direction and its meaning: *the machine
point is clear of drawing N's zones*. No consumer changes, no configuration
changes. Only the freshness improves — from a 10 ms control-loop poll of a
motstat snapshot to a value recomputed every servo cycle.

### What moves

The predicate is already written and is already realtime-shaped —
`insideAnyObstacleExcept` (`pkg/pnproute/plan.go:238`) is a bounding-box
pre-check plus point-in-polygon: no allocation, bounded loops, no calls out. It
is ported to C essentially verbatim.

At init, after `newPlanners` has offset the zones, each planner's
`OffsetZones()` is copied into a flat block:

```c
typedef struct { double x, y; } rt_point_t;
typedef struct { int n; const rt_point_t *pts; double minx, miny, maxx, maxy; } rt_poly_t;
typedef struct { int n; const rt_poly_t *polys; } rt_scene_t;
```

One `rtapi->calloc` per module, laid out scenes-then-polys-then-points so a
single free releases it. The bounds are precomputed at init because that is
where the cyclic function spends most of its time not doing work.

### The position source, and why it is an open question

The cyclic function cannot ask motstat — that is a GMI call, not RT-safe. It
needs the position from a HAL pin.

motmod exports `joint.N.pos-fb`, not cartesian axis pins. pnptask's whole
geometry model is already cartesian XY (stations, trays and the drawings are
machine coordinates), so on a `trivkins` machine wiring joint pins is exact,
and on a non-trivial kinematics it is not.

Options, to be decided before implementation:

1. **Require cartesian position input pins** (`pnp.task.pos-x` / `pos-y`), wired
   by the configuration. Honest and explicit; correct under any kinematics
   provided the configuration wires something cartesian.
2. Derive them in the Go loop and hand them to the RT function — defeats the
   purpose, the value would be 10 ms stale again.
3. Restrict the feature to trivkins and say so.

(1) is the recommendation. It costs the configuration two `net` lines.

### A self-check worth having

An unwired float pin reads 0.0, which is a perfectly legal position — so
"nobody wired it" is indistinguishable from "the head is at the origin" by
inspection of the value alone.

The Go loop still computes the same predicate at 10 ms from motstat. Having it
compare its answer against the RT pin and warn on *persistent* disagreement
turns a silent wrong answer into a log line, and continuously validates the
whole path in the field. Transient disagreement during motion is expected and
must not warn — the two answers are sampled at different instants, which is the
entire point of the change.

## 7. Open questions

1. **Teardown ownership** (§5). Which side removes the function, where the RT
   barrier sits, and how runtime unload is covered.
2. **Position source** (§6). Recommendation is explicit cartesian input pins.
3. **`usesFP`.** The dead-zone test is floating point throughout, so it must be
   exported with `uses_fp` set; worth confirming what the flag costs on the
   platforms in use before making it the default in the wrapper.
4. **Does `pkg/hal` need the `env` at all?** The wrapper needs a component id
   and the HAL callback table. `pkg/hal` already holds equivalents internally;
   whether the C half receives the same `env` pointer the launcher builds for
   cmods, or one synthesised per Go module, is an implementation choice with
   consequences for how identical the C really is.

## 8. Build order

1. `pkg/hal`: `ExportFunct` + `RTCalloc`/`RTFree`, with the no-Go-func
   restriction and the `.c`-file convention documented at the API.
2. `make rt-effects-check`: cover Go modules' `.c` translation units. Verify by
   introducing a deliberately blocking call and confirming the build fails.
3. A minimal module exercising the path end to end — export, assemble, walk,
   unload — including the runtime-unload case from §5.
4. pnptask: the RT dead-zone function, the position pins, and the §6 self-check.
5. Re-run the pnptask sim scenarios; they should be indifferent to the change,
   which is itself the point.
