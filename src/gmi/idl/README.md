# stratuMAK Interface Definition (GMI)

Machine-readable API definitions for LinuxCNC's inter-module communication.

## Purpose

GMI files define:
- Data types (structs, enums)
- Function signatures with request/response types
- API-level metadata (versioning, REST exposure, RT safety)

## Codegen Targets

From each `.gmi` file, generate:
- **C**: Server callbacks + types (`*_api.h`)
- **C**: Client library using cJSON/libcurl (`*_client.h`, `*_client.c`)
- **Go**: Server handlers + HTTP routing
- **Python**: Client library using requests

## File Format

```
@api <name>
@version <n>
@prefix "<path>"
@rest_export true|false

enum Name {
    VALUE = n
    ...
}

type Name {
    field: type
    ...
}

@method "GET|POST|PUT|DELETE"
@path "/endpoint"
@rt_safe "true|false"
@doc "description"
func name(params) -> ReturnType
```

Function annotations (`@method`, `@path`, `@rt_safe`, `@doc`) precede the `func`
declaration. Values must be quoted strings. Functions do not use braces.

## Reporting Failures: `@rc_error`

A callback that hands its payload back as the C return value has nowhere left
to report a failure, so a provider error reaches every consumer as a zeroed
struct — indistinguishable from a valid empty answer. `@rc_error` is the shape
that fixes it: the `i32` return is the status channel (`0` = success) and the
payload travels in an `out` parameter.

```
@rc_error
func get_entry(handle: i32, key: string, entry: Entry out) -> i32
```

Both Go signatures are unchanged by the conversion — consumer and provider each
stay `GetEntry(handle int32, key string) (Entry, error)` — because the generated
bridge maps the provider's `error` to the rc and back. The REST request and
response bodies are unchanged too: the out parameter *is* the response body. So
only C callers see the difference, where the payload becomes a pointer argument.

Without it, an `i32` return is a value the provider supplies itself (canon's
out-param getters return `-1` for "not found" that way); `@returns_value` says
the same thing for a function with no out parameter, and the two annotations are
mutually exclusive. A slice payload (`entries: []Entry out`) is passed as an
owning `{data, len}` struct — the provider mallocs, the caller frees, exactly as
for a slice return — and is only allowed on an `@rc_error` func.

## Field & Parameter Constraints

Fields (in `type` blocks) and parameters may carry inline validation
constraints, written after the type — on a parameter, after any
`byref`/`out`/`ptr` mode keyword (`name: type [mode] [@constraints…]`):

```
type ToolEntry {
    pocketno: i32    @min(0) @max(1000)
    comment:  string @maxlen(255)
}

func put_tool(toolno: i32 @min(1), entry: ToolEntry) -> PutToolResult
```

| Constraint            | Applies to               | Meaning                       |
|-----------------------|--------------------------|-------------------------------|
| `@min(n)` / `@max(n)` | `i*`, `u*`, `f*`         | numeric bound (inclusive)     |
| `@minlen(n)` / `@maxlen(n)` | `string`, `[]T`, `[N]T` | length bound (chars / elems) |
| `@notempty`           | `string`, `[]T`, `[N]T`  | length > 0                    |
| `@notnull`            | any nullable `T?`        | value must be present         |
| `@regex("…")`         | `string`                 | full-match pattern (server only) |

Enum-typed fields/params are validated **automatically** (value must be a
declared variant) — no annotation needed; opt out with `@enum_open`.

Enforcement: the server rejects the first violation with an HTTP 400
`{error, code, field, constraint}`; the generated TypeScript/Python clients
collect **all** violations and raise before sending. `@regex` runs on the Go
server only (avoids JS/Python/RE2 flavor mismatch). Mistyped or contradictory
constraints (e.g. `@maxlen` on an `i32`, `@min > @max`) fail the build.

See `../FIELD_VALIDATION_DESIGN.md` for the full design.

## Failure reporting

A `@rest_export` function's Go provider returns `(result, error)`, and the error
reaches the client — REST dispatch for a Go provider does not cross the C ABI,
which has no error channel and would substitute a zero value. The status comes
from what *kind* of failure it was, which the provider says with
`apiserver.NewFault`:

| kind | status | meaning |
|---|---|---|
| `FaultState` | 409 Conflict | the machine's current state forbids it ("must be in MDI mode"). Nothing happened; re-sending unchanged fails the same way. |
| `FaultNotReady` | 503 Service Unavailable | the module is not started, or was stopped. The same request may succeed shortly. |
| `FaultNotFound` | 404 | the named thing does not exist. |
| `FaultCapacity` | 503 Service Unavailable | a resource limit is reached. Distinct from `FaultNotReady`: the module is running and healthy, it is simply full. |
| `FaultInternal` (zero value) | 500 | the controller itself failed. |

An unclassified error keeps the conservative 500, and a bare or wrapped errno
still maps (`EBUSY`/`EEXIST` → 409, `ENOENT` → 404, `EINVAL`/`ERANGE` → 400,
`EPERM` → 403, `ENOSYS` → 501). A `@constraint` violation is a 400 before the
provider is reached.

**Classify refusals.** A 500 says the controller broke: it invites a retry
against a machine presumed sick and is what monitoring escalates on. Most
failures on a command surface are not that — they are the machine correctly
declining, which is a 409.

**A command that was accepted and then faulted is not a transport error at
all.** The interpreter rejecting `G10 L1 P0` is a machine event: it is published
on the error channel and the call reports `RCS_ERROR` in a normal response,
because the call did its job — the command reached the machine — and the caller
still needs to read the resulting state. Only a *refusal* is a transport error.

## Type System

| GMI       | C              | Go        | Python         |
|-----------|----------------|-----------|----------------|
| bool      | bool           | bool      | bool           |
| i32       | int32_t        | int32     | int            |
| u32       | uint32_t       | uint32    | int            |
| i64       | int64_t        | int64     | int            |
| u64       | uint64_t       | uint64    | int            |
| f64       | double         | float64   | float          |
| string    | const char*    | string    | str            |
| T?        | T* (nullable)  | *T        | Optional[T]    |
| [T; N]    | T[N]           | [N]T      | list[T]        |
| []T       | T*, size_t len | []T       | list[T]        |
| map[string]T | — (none)    | map[string]T | dict[str, T] |
| ptr       | void*          | unsafe.Pointer | N/A       |

`T?` is **nullable, not merely optional**: null and the zero value are distinct,
all the way through. `string?` is `*string` in Go and a possibly-NULL `const
char*` in C, so a C callee tests supplied-ness the usual way (`if (req->field)`)
and a Go provider returns nil for "absent" and `&""` for "present and empty".
Use it only where that difference is real — `[RS274NGC]PARAMETER_FILE` absent
versus set to nothing, or an EoE field left alone versus cleared. Where empty
already means absent (a glob pattern, a filter), plain `string` says so more
honestly.

The `?` binds to the **element** in a collection, so `[]T?` is a slice of
nullable `T` — which is almost never what is meant and is rejected by the
checker. A nil/empty collection already expresses absence: write `[]T`.

### Maps: `map[string]T`

A JSON-object map. The key is locked to `string` because a JSON object key
**is** a string; the checker rejects any other key type, nested maps, and
nullable values (a missing key already expresses absence).

A map has **no C ABI**, so it is confined to the one position that never
crosses C: the full return type of a **watch-only** func (`@watch true`, no
`@method`). A watch frame is Go-marshaled JSON end to end, and a map return is
what makes per-key delta encoding meaningful (see `@watch_delta`). The C
surface skips such a func entirely — the generated header documents the gap —
so a map-returning watch is servable by **Go providers only**; every other
placement fails the build. First consumer: `pyvcp.watch_state`
(`map[widget_id]WidgetState`).

### `@watch_delta`

`@watch_delta true` on a `@watch` func registers the watch with per-connection
delta encoding: the first push is a full snapshot, subsequent pushes carry only
the top-level JSON keys whose value changed. Combine with a `map[string]T`
return for per-entry deltas (a struct return diffs on its field names instead).
Rejected on a non-`@watch` func and on a binary (`[]u8`) watch, which has no
JSON keys to diff.

## Files

- `common.gmi` — shared types (Position, etc.)
- `hal.gmi` — HAL component API
- `halcmd.gmi` — HAL commands (launcher integration)
- `motion.gmi` — motion control
- `task.gmi` — task/program control
- `status.gmi` — read-only status queries
