# GMI — Generated Message Interface

GMI is stratuMAK's typed inter-module interface system. An API is declared once
in an IDL file and everything needed to call it — from C, Go, Python or
TypeScript, in-process or over REST/WebSocket — is generated from that
declaration. It replaces LinuxCNC's untyped NML stat/command channels.

## Directory structure

```
src/gmi/
├── idl/           # Interface definitions (33 × .gmi)
│   ├── hal.gmi        # HAL component API
│   ├── halcmd.gmi     # HAL command REST API
│   ├── emccmd.gmi     # task command surface
│   ├── emcstat.gmi    # task status surface
│   ├── motctl.gmi     # motion control
│   └── README.md      # IDL format reference — types, annotations, constraints
├── lib/           # libgmi: C runtime for generated C code
│   ├── gmi.h          # umbrella header
│   ├── gmi_error.*    # error codes
│   ├── gmi_types.*    # dynamic buffers, string slices
│   ├── gmi_json.*     # JSON parse/build (wraps cJSON)
│   ├── gmi_http.*     # HTTP client (wraps libcurl)
│   └── README.md      # library API documentation
├── python/        # hand-written Python client shim, installed as lib/python/gmi/
├── codegen/       # Submakefile driving code generation for every IDL file
└── README.md      # this file
```

## The compiler

GMI code generation is a subcommand of **`modcompile`** (`bin/modcompile`,
built from `src/stmak/cmd/modcompile/`) — the same tool that compiles `.comp`
components and manages compiled-in Go modules. There is no separate
`gmicompile` binary.

```bash
# Parse only — print the AST as JSON
modcompile gmi --parse idl/hal.gmi

# C server header: types, callback typedefs, registration helpers
modcompile gmi --server-c idl/kins.gmi -o kins_api.h

# C REST client (uses libgmi)
modcompile gmi --client-c idl/halcmd.gmi -o halcmd_client

# Python client
modcompile gmi --client-python idl/manualtoolchange.gmi -o mtc_client.py
```

### Generation targets

| Flag | Output |
|---|---|
| `--server-c` | C header: types, callback typedefs, registration helpers |
| `--server-meta` | Go dispatch metadata, type converters, package `init()` |
| `--server-go` | Go provider bridge: interface + `//export` trampolines |
| `--client-c` | C REST client on libgmi |
| `--client-cgo` | Go consumer client for an in-process C provider |
| `--client-go` | Go REST client |
| `--client-python` / `--client-python-ws` | Python REST / watch client |
| `--client-ts` / `--client-ts-ws` | TypeScript REST / watch client |
| `--stream-server-c` / `--stream-server-go` | binary stream endpoint server side |

> **`--client-c` supports a subset of the type system:** primitive scalars,
> `[]string`, and one level of nested struct. Narrow scalars (`u8`/`i16`/`f32`/…),
> enum-typed fields, non-string slices, slice-of-struct, and deeper nesting are
> rejected at generate time (a build error, never a silently-broken client). For
> APIs that use those shapes, generate a `--client-go`, `--client-python` or
> `--client-ts` client instead.

## Generated code

All generated output goes to `src/generated/` — **gitignored in full**, one Go
package per API under `src/generated/gmi/<api>/`. It is a build product, never
committed and never edited: a bug in generated code is a bug in the generator
(`src/stmak/internal/gmicompile/cgen/`).

Generation is wired up in `codegen/Submakefile`, which runs before the Go build
so that `stmakd` compiles against fresh output. That Submakefile must appear in
`SUBDIRS` after `stmak` (which builds `modcompile`) and before anything that
consumes the generated headers.

## IDLs from outside the tree

A machine configuration can define its own API, next to the component that
provides it:

```bash
modcompile add-gmi gmi/transp.gmi    # record it and rebuild stmakd
modcompile --install transp_main.c   # finds transp_api.h
modcompile rm-gmi transp             # unregister and rebuild
```

The REST routing is generated Go code compiled into `stmakd`, so an external
IDL takes the route an external Go module does (`add-gomod`): `add-gmi`
validates it and records it in the module store as `gmi-<api>/gmi/<api>.gmi`
— the trust decision, privileged on a packaged system — and the rebuild
generates `--server-c` and `--server-meta` into `gmi-<api>/gmi/<api>/`, where
external GMI packages are discovered. An API name stratuMAK already has, or
that another external module provides, is refused, as is an IDL with
`stream_server` blocks (the rebuild generates no stream bridge). An IDL an
external Go module ships itself is left alone: its package is the one the
module came with. `add-gmi` of a registered, unchanged IDL that stmakd was
built from does not rebuild; `modcompile rebuild` forces one.

`--compile`/`--install` look an API's header up there when a `.comp` names it
in `gmi_provide`/`gmi_consume`. A hand-written `.c` declares nothing, so it is
compiled with every API header directory on its include path. Client code for
a web app comes straight from the file, as for any IDL
(`modcompile gmi --client-ts gmi/transp.gmi -o ...`).

## Runtime library (libgmi)

C library backing generated C clients and servers: error codes, dynamic
buffers, JSON, and an HTTP client. See [`lib/README.md`](lib/README.md).

## Building

```bash
cd src && ./autogen.sh && ./configure && make
```

`modcompile` is built first, then every IDL file is regenerated, then the Go
build and the cmods follow. To rebuild the compiler alone:

```bash
cd src/stmak && go build ./cmd/modcompile
```

## Dependencies

Both are required and checked by `./configure`:

```bash
sudo apt install libcurl4-openssl-dev libcjson-dev
```

## Further reading

- [`idl/README.md`](idl/README.md) — IDL syntax: type system, nullable types,
  maps, constraints (`@min`/`@regex`/…), `@rc_error`, watch/delta annotations,
  and the REST fault taxonomy.
- [`../../docs/dev/ARCHITECTURE.md`](../../docs/dev/ARCHITECTURE.md) — where GMI
  sits in the system, and how it differs from the HAL data plane.
- [`../../docs/dev/DYNAMIC_API_DESIGN.md`](../../docs/dev/DYNAMIC_API_DESIGN.md) —
  the inter-module communication design: registry, dispatch, REST/WebSocket
  boundary.
- [`../../docs/dev/FIELD_VALIDATION_DESIGN.md`](../../docs/dev/FIELD_VALIDATION_DESIGN.md) —
  declarative input validation. *Implemented: server (REST + WS) enforcement
  and client-side (TS/Python) collect-all.*
