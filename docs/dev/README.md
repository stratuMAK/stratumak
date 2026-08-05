# stratuMAK developer documentation

The engineering record of the stratuMAK migration: the production-readiness
program, the per-module review findings it produced, and the design notes behind
the larger architectural decisions.

This is **not** user or integrator documentation — that lives in `docs/src/` and
is built into the manuals. Nothing here is installed by `make install` or shipped
in a package.

**These documents are load-bearing.** Roughly sixty source comments across the
tree cite them by finding ID (`// See ADS_REVIEW_FINDINGS.md A7`, `(C8)`,
`(D3: one committer)`). A closed review document is kept as the decoder ring for
those comments, not as history for its own sake — see the note at the top of
`MILLTASK_REVIEW_FINDINGS.md`.

**New here?** Start with [ARCHITECTURE.md](ARCHITECTURE.md) — how the system is
put together, where the code lives, and what the rules are for changing it.

## Program and cross-cutting

| Document | What it is |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Contributor orientation: the single-process model, the HAL and GMI planes, the component model, the RT rules, startup, build and test gates, and the seams still under migration. |
| [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) | The master ledger: per-submodule verification matrix, tier assignment, design rulings, open gaps, status log. Everything else in this directory hangs off it. |
| [RT_HARDENING_CHECKLIST.md](RT_HARDENING_CHECKLIST.md) | Hard-RT correctness for the mixed Go/C process — Go-scheduler isolation, forbidden calls in the RT path, memory locking/prefault, deadline-miss handling, jitter soaks. Owns the RT correctness of the inherited C that the readiness matrix defers. |
| [SAFETY_BOUNDARY.md](SAFETY_BOUNDARY.md) | Where the boundary lies between stratuMAK software and machine functional safety. Draft, 2026-07-22. |
| [MISSING_FEATURES.md](MISSING_FEATURES.md) | What stratuMAK does not do — parity gaps vs LinuxCNC 2.9 (removed on purpose, not ported yet, behaves differently) and its own designed-but-unbuilt capabilities. |
| [PATH_RESOLUTION_INVENTORY.md](PATH_RESOLUTION_INVENTORY.md) | The "paths are server-side paths" ruling (2026-07-22) and the complete site inventory behind it. Categories A–C done; category D unresolved by design. |

## Review findings

Each document records one review pass: scope, method, findings with verdict
tags, and what was fixed. Ordered roughly as the program ran them.

| Document | Scope |
|---|---|
| [MILLTASK_REVIEW_FINDINGS.md](MILLTASK_REVIEW_FINDINGS.md) | The `verify-milltask` pre-merge review (Phase 0). Closed — kept as the decoder ring for the finding IDs cited in `internal/task/` and `internal/gmicompile/`. |
| [MILLTASK_COMMAND_PARITY.md](MILLTASK_COMMAND_PARITY.md) | Command-by-command audit of all 95 `EMC_*` types against the task layer. Closed, no open items — kept for its intentional-divergence rulings. |
| [PKG_HAL_REVIEW_FINDINGS.md](PKG_HAL_REVIEW_FINDINGS.md) | Tier 1 — `pkg/hal`, the Go↔C HAL binding layer. |
| [GMICOMPILE_REVIEW_FINDINGS.md](GMICOMPILE_REVIEW_FINDINGS.md) | Tier 1 — the `gmicompile/cgen` emission logic, ground-truthed against generated output. |
| [STATE_MACHINE_REVIEW_FINDINGS.md](STATE_MACHINE_REVIEW_FINDINGS.md) | Tier 1 hotspot #5 — every state machine and abort/E-stop path: RT motion C, iocontrol C, Go lifecycle machines. |
| [PHASE3_REVIEW_FINDINGS.md](PHASE3_REVIEW_FINDINGS.md) | Supervision and startup tail — `pkg/inifile`, `pkgreg`, `cmd/stmakd`, `internal/config`. |
| [PHASE4_REVIEW_FINDINGS.md](PHASE4_REVIEW_FINDINGS.md) | HAL tooling — `halcmd`, `halparse`, `halfile`, `haljson`, `modcompile`, `hallib`. |
| [PHASE5_REVIEW_FINDINGS.md](PHASE5_REVIEW_FINDINGS.md) | Services and auxiliaries — persist, tooltable, emccalib, halstream, samplers. |
| [NETWORK_MODULES_REVIEW_FINDINGS.md](NETWORK_MODULES_REVIEW_FINDINGS.md) | `apiserver`, `halrest`, `inirest`, `mqttbridge`, `halscope` under the untrusted-wire lens. |
| [ADS_REVIEW_FINDINGS.md](ADS_REVIEW_FINDINGS.md) | The Beckhoff ADS/AMS server — net-new code, no parity oracle, unauthenticated listener. The pass that established the untrusted-wire lens. |
| [NGCPREVIEW_REVIEW_FINDINGS.md](NGCPREVIEW_REVIEW_FINDINGS.md) | Server-side G-code preview and its Python adapter, vs the classic `gcodemodule.cc`. |
| [GMI_PYTHON_REVIEW_FINDINGS.md](GMI_PYTHON_REVIEW_FINDINGS.md) | The `gmi` Python shim — parity vs `emcmodule.cc`, wire contract, concurrency. |
| [AXIS_ADAPTATION_REVIEW_FINDINGS.md](AXIS_ADAPTATION_REVIEW_FINDINGS.md) | The AXIS NML→GMI adaptation, including the 25.4× inch-machine unit break. |
| [PYVCP_REVIEW_FINDINGS.md](PYVCP_REVIEW_FINDINGS.md) | The widget-centric pyvcp migration, reviewed-and-adopted in place of the pin-centric port. |
| [WEBAPP_REVIEW_FINDINGS.md](WEBAPP_REVIEW_FINDINGS.md) | The five non-ClassicLadder web apps: tooledit, emccalib, halshow, halscope, latency. |
| [CLASSICLADDER_REVIEW_FINDINGS.md](CLASSICLADDER_REVIEW_FINDINGS.md) | The ClassicLadder migration pre-merge review — RT C engine, Go write surface, IDL plumbing, webapp, tests. |

## Design notes and migration specs

| Document | What it is |
|---|---|
| [DYNAMIC_API_DESIGN.md](DYNAMIC_API_DESIGN.md) | The GMI inter-module communication system that replaces NML: IDL, codegen targets, transport, the full step-by-step implementation record. |
| [FIELD_VALIDATION_DESIGN.md](FIELD_VALIDATION_DESIGN.md) | Declarative field/parameter constraints in `.gmi` IDL, enforced by generated code. |
| [EXTERNAL_MODULE_INSTALL_DESIGN.md](EXTERNAL_MODULE_INSTALL_DESIGN.md) | How out-of-tree modules build against a packaged install. Implemented and verified on hardware 2026-08-01; §7 records what the implementation decided. |
| [VCP_MIGRATION.md](VCP_MIGRATION.md) | The architecture for migrating all UI code (pyvcp, gladevcp, qtvcp, full GUIs) onto stratuMAK infrastructure. The template future UI ports follow. |
| [AXIS_MULTI_CLIENT.md](AXIS_MULTI_CLIENT.md) | Centralising AXIS UI state server-side so multiple clients can run at once. |
| [MILLTASK_CLEANUP.md](MILLTASK_CLEANUP.md) | Removing Python/Boost.Python from milltask and the interpreter; making the interpreter multi-instance capable. |
| [MILLTASK_GO_IMPL.md](MILLTASK_GO_IMPL.md) | The milltask Go rewrite implementation plan. |
| [MILLTASK_GOROUTINE_PROBLEM.md](MILLTASK_GOROUTINE_PROBLEM.md) | Why the Go milltask coordinator is hardened in place rather than reduced to a single-goroutine state machine. The design record behind that decision. |
| [MILLTASK_LIFECYCLE_SWEEP.md](MILLTASK_LIFECYCLE_SWEEP.md) | The systematic tool-change/lifecycle porting sweep against 2.9's task sources. Un-xfailed 17 tests. |
| [STMAK_PORT_SPEC.md](STMAK_PORT_SPEC.md) | The `motion-logger` interceptor design — how the RT parity trace is captured under stratuMAK. |
| [UNITS_MM_CONSISTENCY_FIX.md](UNITS_MM_CONSISTENCY_FIX.md) | Converting all linear config values to internal mm at load time, with the inch-machine modal-units fix. |

## Related documents kept with their code

Deliberately not collected here — they read as part of the thing they sit next to:

- `tests/DISPOSITION.md` — the authoritative runtests disposition ledger.
- `tests/motion-logger/parity-vs-2.9/PARITY_FINDINGS.md` — parity harness findings.
- `src/gmi/README.md`, `src/gmi/idl/README.md`, `src/gmi/lib/README.md` — GMI directory, IDL format and runtime library reference.
- `src/hal/drivers/ethercat/DC-SYNC.md` — EtherCAT distributed-clock synchronisation (integrator-facing).
- `src/cnc/usr_intf/pncconf/ADDING_A_MESA_CARD.md` — how to add a Mesa card to pncconf.
- `src/hal/classicladder/zSTMAK_README.txt` — what is left of the ClassicLadder directory and why.
