# Path resolution — complete site inventory (cmods + Go modules + GMI surface)

**Purpose.** stratuMAK splits the CLI/REST client from the server, so "a path" in a
module argument no longer has an unambiguous meaning. The ruling (2026-07-22) is:
**paths are server-side paths**, resolved by one shared rule — the same rule the
startup HAL-file processing already uses (`internal/halfile.(*Executor).resolvePath`:
tilde → `LIB:` → absolute → configDir → HALLIB_PATH, regular-file check) — with
containment validation. `cmd/halcmd`'s client-side `resolveArgPath` heuristic is
deleted (finding **HC-2**).

This document is the inventory of every site that must be migrated. Finding the
sites is the bulk of the work; the per-site change is small.

## The invariant that holds things together today

`internal/launcher/launcher.go:206` **chdirs the server process to the INI file's
directory** at startup. So "relative to the server cwd" already *is* "relative to
the config dir" in normal operation. That is why `hal/drivers/ethercat/conf.c`
`fopen`s its `config=` value verbatim and still works, and why `"."` leads
`halibPath` (`launcher.go:392`). The invariant is undocumented and does **not**
hold in halrun mode (no INI, arbitrary cwd). After migration it is no longer the
mechanism — only the documented default base.

## Design consequences the sweep produced

1. **Resolve where the file is opened, not where the argument is parsed.** The
   ethercat XML config carries `<initCmds filename="…">` attributes that are
   opened one level below the argument (`conf.c:1391` → `conf_icmds.c:176`). Any
   arg-boundary scheme misses nested paths entirely. Same class: INI-derived
   paths, `SUBROUTINE_PATH` entries.
2. **The argument name is not a usable signal.** `config=` is a *file* for
   ethercat/mb2hal, and a *config string* for `hm2_pci`, `hm2_eth`, `hm2_7i43`,
   `hm2_7i90`, `hm2_spi`, `hm2_spix`, `hm2_test`, `mux_generic`, `matrix_kb`,
   `sendkeys`. Only the module knows. Resolution must be opt-in per call site.
3. **Device paths must be exempt.** `spidev_path=/dev/spidev1.0`,
   `port=/dev/ttyUSB0`, `/dev/mem`, `/proc/*`, `/sys/*` must never be
   config-dir-anchored or containment-checked. This is a separate arg class, not
   a resolver flag applied loosely.
4. **Read and write need different policies.** Several sites create files
   (`filestream outfile=`, `classicladder save_project`, `persist_sqlite dbpath`,
   `emccalib` INI rewrite). Containment matters more for writes; the regular-file
   existence check does not apply.
5. **`ngcpreview` is the model.** `collectAllowedDirs` + `EvalSymlinks` +
   allow-list check (`module.go:1139`, read at `module.go:1231`) is already
   exactly the containment design proposed here — the shared resolver should
   generalise it, and ngcpreview should then use the shared one.

---

## Category A — config-derived read paths (migrate)

### C cmods
| # | site | arg / source | opens |
|---|---|---|---|
| A1 | `hal/drivers/ethercat/conf.c:559` | `config=` | `:596` `fopen` |
| A2 | `hal/drivers/ethercat/conf.c:1391` | **nested** `<initCmds filename=>` in the XML | `conf_icmds.c:176` `fopen` |
| A3 | `hal/components/filestream.c:312` | `infile=` | `:364` `fopen` |
| A4 | `hal/drivers/mesa-hostmot2/hm2_modbus.c:2567` | `mbccbs=` (modbus command-control-block file) | `:2058` `open` |
| A5 | `hal/user_comps/mb2hal/mb2hal_init.c:16` | `config=` (quoted form supported) | `mb2hal.c:479` `fopen` |
| A6 | `hal/user_comps/xhc-hb04.c:663` | `I=` (button config) | `:168` `fopen` |
| A7 | `hal/components/z_level_compensation.c` | filename via the GMI `load()` call — see C4 | `:283` `fopen` |

### Go modules
| # | site | arg / source | opens |
|---|---|---|---|
| A8 | `internal/haljson/module.go:100` | `config=` | `config.go:40` |
| A9 | `internal/mqttbridge/module.go:65` | `config=` | `config.go:116` |
| A10 | `internal/adsmodule/module.go:95` | `config=` | `adsconfig/serverconf.go:54` |
| A11 | `internal/pyvcpmodule/module.go:77` | `xml=` | `panel.go:146` |
| A12 | `internal/classicladder/module.go:92` | **positional** `.clp` — the only positional path arg in the tree, and the sole reason `resolveArgPath` existed | `fileformat.go:31` |
| A13 | `internal/tooltable/module.go:103` | INI `[EMCIO]TOOL_TABLE` | `import_tbl.go:19` |
| A14 | `internal/task/config.go:432` | INI `COMP_FILE` (per joint) | `:451` |
| A15 | `internal/inirest/inirest.go:57` | INI `[RS274NGC]PARAMETER_FILE` — **served over REST** | `:65` |
| A16 | `internal/emccalib/module.go` | INI file itself (backup/restore) | `:241`, `:315` |
| A17 | `internal/halparse/parser.go:810` | `source` include directive | own resolution — reconcile with the shared rule |
| A18 | `internal/ngcpreview/module.go:1231` | GMI `get_file`/`gen_preview` | **already contained** (`allowedDirs`) — convert to the shared resolver |

## Category B — write paths (migrate, different policy)

| # | site | source |
|---|---|---|
| B1 | `hal/components/filestream.c:313` → `:374` `fopen(…, "w")` | `outfile=` |
| B2 | `internal/classicladder/fileformat.go:129` `os.Create` | GMI `save_project(path)` — REST-reachable write |
| B3 | `internal/persist_sqlite/module.go:71` `MkdirAll` + `:146` `sql.Open` | `dbpath=` (directory) |
| B4 | `internal/emccalib/module.go:273` `os.Create`, `:319` `os.WriteFile` | rewrites the INI file |
| B5 | `internal/halcmd/halcmd.go:640` `os.Create` | `save` — **not** REST-reachable (hardcoded empty filename, confirmed in the Phase-4 review) |
| B6 | `internal/daemon/daemon.go:90` | pidfile — launcher-controlled, not config-derived |

## Category C — GMI/REST-reachable filename parameters (highest exposure)

These take an arbitrary string straight off the wire.

| # | IDL | note |
|---|---|---|
| C1 | `emccmd.gmi:201 program_open(file)` | |
| C2 | `emccmd.gmi:191 load_tool_table(file)`, `emcio.gmi:88 tool_load_table(file)` | |
| C3 | `classicladder.gmi:242/247 load_project(path)` / `save_project(path)` | read **and** write |
| C4 | `z_level_compensation.gmi:19 load(filename)` | feeds A7 |
| C5 | `ngcpreview.gmi:90/102 gen_preview(filename)` / `get_file(filename)` | already contained — the reference implementation |
| C6 | `canon.gmi:318 set_parameter_file_name(name)` | internal caller |

## Category D — false friends: must NOT be resolved or contained

- **Config strings named `config=`:** `hm2_pci.c:814`, `hm2_eth.c:1604`,
  `hm2_7i43.c:490`, `hm2_7i90.c:467`, `hm2_spi.c:505`, `hm2_spix.c:678`,
  `hm2_test.c:589`, `mux_generic.c:216`, `matrix_kb.c:167`, `sendkeys.c:175`.
- **Other spec strings:** `sampler`/`streamer` `cfg=`, `hal_parport cfg=`,
  `lcd fmt=`, `enum enums=`, `hal_gpio inputs=/outputs=`.
- **Device nodes** (config-derived but *not* filesystem-relative):
  `hm2_spi.c:512` / `hm2_spix.c:697` `spidev_path=`, `mitsub_vfd.c:108`,
  `pmx485.c:95`, `scorbot-er-3.c:73` `port=`, `hal_input.c:545` device paths,
  `shuttle.c:345` device args + `/dev/hidraw*` glob.
- **Hardcoded system paths:** `/dev/mem` (`hal_bb_gpio.c:66/85`,
  `hal_pi_gpio.c:180`, `hm2_rpspi.c:984`), `/dev/gpiomem`, `/dev/uinput`
  (`sendkeys.c:106`), `/proc/cpuinfo`, `/proc/device-tree/*`
  (`hm2_rpspi.c:1229+`, `spix_rpi3.c:651+`), `/sys/devices/system/cpu/isolated`
  (`halcmd/cpupool.go:195`).
- **`ethercat.gmi:483/489 foe_read/foe_write file_name`** — an EtherCAT FoE
  device-side filename, not a host path.

## Rulings (2026-07-22) — implemented

1. Absolute paths: **allow if contained**.
2. Containment roots: **configDir + HALLIB_PATH, without `.`** (this also rules
   **HF-1** — `.` is no longer a search root; the base covers the working
   directory).
3. Enforcement: **hard fail**, no warn-then-enforce period.

## Status

**Mechanism — DONE.** `internal/pathres` holds the single rule (`Read`/`Write`/
`Dir` modes, `EvalSymlinks` containment, `SetDefault` published by the launcher
after its chdir). `halfile.resolvePath` is a thin wrapper over it.
`cmd/halcmd`'s `resolveArgPath` is deleted — arguments go over the wire
verbatim. `pkg/cmodule/stmak_path.h` adds `env->path->resolve(ctx, name, mode,
&err)`; `cmod_env_t` gained a trailing `path` field.

Two rules that fell out of the implementation:
- A **relative write target resolves under the base only**, never into a library
  directory — otherwise `outfile=core.hal` would find and overwrite the system
  `core.hal`.
- **Non-regular files are refused in both directions.** `loadModuleNamed` holds
  `modMu` across a whole load, so opening a FIFO would wedge every load, unload
  and shutdown.

**Category A/B Go sites — DONE** (A8–A18, B2–B4). Six hand-rolled "join against
the INI dir" copies collapsed into one call.

**Category A/B C sites — DONE:** A1/A2 (ethercat `config=` **and** the nested
`<initCmds filename=>` — resolved at the `fopen`, which is what makes nesting
safe), A3/B1 (filestream `infile=`/`outfile=`), A4 (`hm2_modbus mbccbs=` — its
"path is not absolute" warning is gone, relative paths are first class now),
A5 (`mb2hal config=`), A6 (`xhc-hb04 I=`), A7/C4 (`z_level_compensation`'s
probe map, which arrives straight off the GMI `load()` call). A17 (halparse
`source` include) needed no change — it already resolves through
`halfile.Executor.Resolve`, now the shared rule.

**Category C — DONE.** Ruling (2026-07-22): G-code is user data, not
configuration, so program paths get their own roots — **PROGRAM_PREFIX +
SUBROUTINE_PATH + `<EMC2_HOME>/share`**, the set ngcpreview already shipped.
`pathres.ProgramDirs`/`ProgramResolver` now own that definition (moved out of
ngcpreview, which used to be its only home) on top of the shared base rule.
- C1 `program_open` (`task/commands.go:761`) — had **no containment at all**;
  now resolved before the interpreter sees the name. The busy check still runs
  first, so a rejected-anyway request keeps reporting `ErrBusy`.
- C3 classicladder `load_project`/`save_project` — config paths (Read/Write).
- C4 `z_level_compensation load()` — resolved in the cmod at the `fopen`.
- C5 ngcpreview — shared resolver + program roots; its `isAllowedPath` is gone.
- C2 `load_tool_table`/`tool_load_table` — **no change needed**: both iocontrol
  implementations are no-ops (`ioControl.c:716`, `ioControl_v2.c:971`); the
  tooltable module owns the data and no file is opened.
- C6 `set_parameter_file_name` — **not REST-reachable**: `canon.gmi` declares no
  `@path`, it is the interpreter's C callback interface.

**Category D** is unchanged by design — those sites must never be resolved.
