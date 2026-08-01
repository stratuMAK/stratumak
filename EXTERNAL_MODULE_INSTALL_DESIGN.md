# Installing external modules on a packaged system — design note

Status: **implemented and verified on hardware, 2026-08-01**, all six steps of
§6, plus the capability audit §5 left open. Written the same day,
after `stratumak` and `stratumak-dev` 0.1.0 were installed on a real machine and
an out-of-tree Go module was built against them for the first time. The text
below is kept as written, as the record of why the layout is what it is; §7
records what the implementation decided where this note left a choice open, and
what it does not cover.

## 1. What happens today

```
$ make install
/usr/bin/modcompile add-gomod .
modcompile add-gomod: creating directory: mkdir /usr/share/linuxcnc/gomc/external: permission denied
```

Four separate problems sit behind that one message.

**Everything mutable lives under `/usr`, which dpkg owns.** `add-gomod` writes
module sources into `$(datadir)/linuxcnc/gomc/external/`, regenerates
`imports_generated.go` and `packages.conf` in the same tree, and rebuilds the
server over `/usr/bin/gomc-server`. The next `apt upgrade` overwrites the server
and every generated file the package ships, silently unregistering locally added
modules. No permission scheme fixes this; it is a question of *which* filesystem
the state lives on.

**The compiler runs as root.** `rebuildServer` passes the final install path
straight to the compiler (`src/gomc/cmd/modcompile/main.go:641`,
`go build -o outPath` where `outPath = $(bindir)/gomc-server`). So a privileged
`add-gomod` runs `go build`, cgo, `gcc` and any module-supplied build code as
root, and lands the Go build and module caches in root's `HOME`. Only the final
placement needs privilege.

**`modcompile` has no notion of privilege at all** — no `Getuid`, no `SUDO_UID`,
no capability handling beyond save/restore around the rebuild
(`main.go:660-670`). It does whatever it was started with.

**Locally built cmods have the same problem one directory over.**
`modcompile <x>.comp --install` writes into `$(EMC2_CMOD_DIR)` =
`/usr/lib/linuxcnc/cmod`, also dpkg-owned. A local `.so` there survives upgrades
(dpkg only removes what it shipped) but can silently shadow or collide with a
shipped module of the same name.

## 2. Threat model — why the obvious fixes are wrong

The obvious fix is a `stratumak-dev` group owning the directories that need
writing. **It is rejected**, for two distinct reasons.

**Directly.** `make setuid` (`src/Makefile:611`) grants the server

```
cap_net_admin, cap_net_raw, cap_bpf, cap_ipc_lock, cap_sys_resource,
cap_sys_nice, cap_sys_rawio, cap_perfmon, cap_sys_admin …
```

`cap_sys_admin` and `cap_sys_rawio` are root in all but name. A group-writable
`gomc-server` makes group membership equivalent to root: write your own bytes
into the file the kernel then grants those capabilities to. `/usr/lib/linuxcnc/cmod/`
is the same argument one step removed — those objects are dlopened into that
process.

**Indirectly, and this is the subtler one.** A group-writable *source* tree is
the same escalation, merely deferred. A group member edits the shared tree and
waits; the next `sudo modcompile rebuild` by an administrator compiles it into a
capability-bearing binary. Confused deputy, and the deputy is `setcap`. This
applies to `/var/lib/stratumak/gomc` exactly as much as to anything under
`/usr` — being neither dpkg-owned nor itself capability-bearing does not help.

So: **no directory that feeds a privileged build may be writable by an
unprivileged user, anywhere.**

## 3. The trilemma

Three properties, of which any two are easy:

1. the compiler does not run as root
2. unprivileged users cannot write build inputs that root later consumes
3. the installed server actually contains the user's module

The resolution is the one distribution build systems use: an administrator makes
an **explicit, attributable trust decision about one directory**, and the build
then runs unprivileged and ephemerally from it. Nothing shared is ever writable
by non-root.

`sudo modcompile add-gomod ~/foo` means "I vouch for `~/foo`" — the same trust a
person extends with `sudo make install`, and acceptable for the same reasons: it
is deliberate, it names its input, and it is attributable. A standing
group-writable tree is not acceptable, because the trust is invisible, deferred
and attributable to nobody.

## 4. Proposed design

### 4.1 Layout

```
/usr/libexec/stratumak/gomc-server     pristine, dpkg-owned, never modified
/var/lib/stratumak/bin/gomc-server     symlink -> the pristine one,
                                       becomes a real file only after a local rebuild
/usr/bin/gomc-server                   symlink -> /var/lib/stratumak/bin/gomc-server
/var/lib/stratumak/gomc/               root:root 0755, derived state
/var/lib/stratumak/cmod/               root:root 0755, locally built cmods
```

Symlink rather than copy. A 19 MB binary is not duplicated on every install; in
the common case an upgrade is live the moment dpkg unpacks the new pristine
binary, with no recovery logic; and "is this server locally modified?" reduces
to `test -L`.

Two packaging constraints follow. The `/var/lib/stratumak/bin/gomc-server`
symlink must **not** be shipped in the deb's data archive — and not via
`debian/stratumak.links` either, which will tempt. If dpkg owns that path,
every upgrade unpack replaces a locally rebuilt real binary with the shipped
symlink, silently discarding the local build — exactly the failure §4.4 exists
to prevent. postinst creates it, only if absent. Second, dpkg does not preserve
xattrs, so the pristine binary's file capabilities do not survive the .deb:
postinst must apply them on every install and upgrade (§5).

### 4.2 The `/var` tree is derived, not authoritative

Source of truth is the registry plus a root-owned copy of each registered
module's source. The tree is regenerated from those. "Is this tree trustworthy"
then reduces to "was every entry added through a privileged, attributable step",
which is a checkable property rather than a hope.

The module source copies must be root-owned *once copied* — not merely the
generated files, or the vector returns one directory down.

### 4.3 Privilege split inside `add-gomod` / `rebuild`

```
root   record and copy THIS source directory into the tree;    <- the trust decision
       regenerate imports and registry
drop   re-exec unprivileged: go build -o <staging path>        <- compiler never root
root   move the staged binary into place, reapply capabilities <- irreducibly privileged
```

Codegen stays in the root phase, deliberately: the tree in §4.1 is `root:root
0755`, so an unprivileged phase could not write `imports_generated.go` or
`packages.conf` into it — and it need not. Regeneration is `modcompile`'s own
string emission; no module-supplied code runs. What must never run as root is
the phase that *does* run module-influenced code — `go build`, cgo, `gcc`. That
phase only reads the root-owned tree (0755 suffices; Go builds do not write
into the source directory) and writes to its own `GOCACHE` and a staging output
path owned by the build identity; the final root phase moves the staged binary
into place. `HOME` and `GOCACHE` are set explicitly for the build phase — sudo
configurations differ on whether `HOME` is preserved, and the cache must land
in neither root's home nor the shared tree.

The build identity: `SUDO_UID` when invoked via sudo — the person who just made
the trust decision. When there is no `SUDO_UID` (a direct root shell, or the
systemd one-shot of §4.4), a dedicated unprivileged system user
(`stratumak-build`, created in postinst) with its cache under
`/var/cache/stratumak-build`. Using the dedicated user unconditionally would
also be defensible and is simpler; either way the invariant is that the
compiler never runs as root.

Dropping is by **re-exec, not in-process `setuid`**. Not for the old
one-thread reason — since Go 1.16 `syscall.Setuid` applies to all threads — but
because re-exec also yields a clean environment for the build and a natural
process boundary at the staging hand-off.

Reapplying capabilities needs `CAP_SETFCAP`, i.e. root, which is an independent
reason the install phase stays privileged even if the build does not.

### 4.4 Staleness instead of rebuilding on upgrade

`EMC2Version` is baked in by ldflags, so a locally rebuilt server records the
version of the tree it was built from. Comparing that against the installed gomc
tree at startup gives a "your local build is stale, run `modcompile rebuild`"
signal almost for free, and catches the real hazard: a server built against
gomc sources from an older release, now facing newer cmods.

A warning may be too weak a floor for that hazard. The cmod ABI carries no
version stamp today (`gomc_env.h` defines none; only the runtime API registry
is versioned per-API), so a stale server dlopening a cmod built for a newer
`cmod_env_t` is undefined behaviour with no detection at load time. Give the
cmod ABI a stamp and have the launcher *refuse* a mismatched cmod; the
staleness warning then covers the benign skew, the refusal the fatal one.

On upgrade:

- active server is a symlink → nothing to do, the new pristine binary is live
- active server is a real file and **no** external modules are registered →
  restore the symlink
- active server is a real file and external modules **are** registered → leave it
  alone, mark it stale, tell the administrator

**Nothing is recompiled in a maintainer script.** A full server build is minutes
long, needs `golang-go` and a warm module cache, and can fail — and a failing
postinst leaves the package half-configured and `apt` wedged. If the rebuild
should be automatic, a systemd one-shot after upgrade is the supportable form —
running its build phase as the dedicated build user of §4.3, since a one-shot
has no `SUDO_UID` to drop to.

### 4.5 Rejected

| proposal | why not |
|---|---|
| `stratumak-dev` group on `gomc-server` or `cmod/` | group membership becomes root via `setcap` (§2) |
| `stratumak-dev` group on the shared source tree | same escalation, deferred through the next privileged rebuild (§2) |
| recompile in `postinst` | slow, needs a toolchain and network, and a failure wedges `apt` (§4.4) |
| gate upgrade behaviour on whether `-dev` is installed | the two come apart in both directions: `-dev` with no external modules is the plain case, and external modules can outlive `-dev`'s removal — at which point a rebuild is impossible and discarding them silently is worse |

## 5. Open questions

**A cmod search path.** `resolveCModulePath` resolves against a single directory
(`src/gomc/internal/launcher/cmodules.go:348`). Relocating local cmods to
`/var/lib/stratumak/cmod` needs the launcher to search two, which raises an
order question: should a local module shadow a shipped one of the same name, or
be refused as a collision? Refusing is safer and easier to diagnose; shadowing is
what people usually expect from a local override. Recommendation: refuse, with
an explicit per-module flag if deliberate shadowing is ever wanted — a stale
local `.so` silently shadowing a shipped fix is miserable to diagnose. Decide
before implementing, since the same relocation covers both directories.

**Nothing grants realtime privileges on a packaged system.** `make setuid` is a
RIP-only make target; the postinst sets `memlock` in `limits.conf` and nothing
else. The mechanism is now specified — postinst applies the capabilities to the
pristine binary on every install and upgrade (§4.1), and the rebuild path
already save/restores them across replacement (`main.go:659-671`; `setcap`
follows the symlink chain, so file capabilities always land on whichever real
file is active). What remains open is the capability *list* itself: the
Makefile's own `TODO: check what's actually needed` — `cap_sys_admin` and
`cap_dac_override` in particular want auditing before that list is enshrined in
a postinst.

**Purge.** Anything written under `/var/lib/stratumak` is untracked by dpkg and
must be removed by `postrm purge`, alongside the `limits.conf` line already
handled there.

## 6. Implementation order

1. `modcompile rebuild`: split unprivileged build from privileged install;
   re-exec the build phase as `SUDO_UID` or the dedicated build user (§4.3).
   Fail with *"needs root: try sudo"* rather than a raw
   `mkdir: permission denied`. **This alone unblocks `add-gomod`.**
2. Introduce `/var/lib/stratumak/gomc`, move `external/` and the generated
   registry there, `GOMC_DIR` already exists as the override knob.
3. Move the pristine server to `/usr/libexec/stratumak`, add the two symlinks
   (the `/var` one created by postinst, never shipped — §4.1), postinst setcap,
   teach postinst the three upgrade cases.
4. Staleness stamp and the startup check; cmod ABI stamp and the load-time
   refusal (§4.4).
5. Local cmods to `/var/lib/stratumak/cmod` plus the launcher search path, once
   the shadow-vs-collide question is settled.
6. `postrm purge` cleanup.

## 7. As implemented

Decisions taken where §4.3 and §5 left a choice, and the places the
implementation went further or stopped short.

**Collisions are refused** (§5). `resolveCModule` searches
`/var/lib/stratumak/cmod` and `/usr/lib/linuxcnc/cmod`, and a bare name present
in both fails the load naming both files. Deliberate overriding is still
possible by loading the module by its full path, which bypasses the search
entirely. No per-module shadowing flag was added; nothing has asked for one yet.

The search half is confirmed on a real config: a set of locally built cmods
installs into `/var/lib/stratumak/cmod` and the launcher resolves them there
from bare names, at startup and through a runtime load. The refusal half has
only ever fired in a unit test — no real installation has yet had the same
module name in both directories, which is the point of separating them.

**The build identity is always `stratumak-build`** (§4.3), never `SUDO_UID`
when the account exists — one identity, one cache, and identical behaviour from
a sudo shell, a root shell and automation. `SUDO_UID` survives only as a
fallback for a tree installed with `make install` rather than from the package,
where nothing ever created the account. With neither, the rebuild refuses
rather than compiling as root.

**Upgrades warn, they do not rebuild** (§4.4). No systemd one-shot was added.
The three postinst cases are as written; the startup check reports a locally
built server older than the installed sources and names the command to fix it.

**§5 is wrong about `setcap` and symlinks**, and it cost a working install to
find out. It says "`setcap` follows the symlink chain, so file capabilities
always land on whichever real file is active" — but the reading side does not.
`getcap` `lstat()`s its argument and silently skips anything that is not a
regular file, exiting zero with nothing to say, which is indistinguishable
from "this file has no capabilities". Before the first local rebuild
`$(bindir)/gomc-server` *is* a symlink onto the package's binary, so the
rebuild read "none" for a file carrying ten, warned that there had been
nothing to carry over, and installed a server that could not do realtime.

Fixed by resolving the path before asking, and by falling back to the
package's own binary when the active one reports nothing — without that
fallback the loss is permanent, since every later rebuild copies the same
nothing forward and only a hand-written `setcap` gets realtime back. A missing
or failing `getcap` is now reported rather than silently read as "no
capabilities", which was the same mistake waiting in a second place.

**The capability list is carried across verbatim** (§5), from `make setuid`
into postinst, `TODO: check what's actually needed` included. The audit §5 asks
for has since been done statically — what each capability is actually for, and
which have no user at all:

| capability | what needs it |
|---|---|
| `cap_net_admin` | attaching the XDP program to the link (`transport_xdp.c`); creating the EoE TAP interface (`pal_eoe.c`) |
| `cap_net_raw` | `AF_PACKET`/`SOCK_RAW` (`transport_raw.c`), and the `AF_XDP` bind |
| `cap_bpf` | loading the XDP program, on kernels 5.8 and later |
| `cap_ipc_lock` | `mlockall` for realtime; the `AF_XDP` UMEM registration |
| `cap_sys_resource` | `setrlimit` of `RLIMIT_MEMLOCK` / `RLIMIT_RTPRIO` |
| `cap_sys_nice` | `SCHED_FIFO` (`uspace_rtapi_lib.c`, `cpupool.go`) |
| `cap_sys_rawio` | `iopl(3)`; `/dev/mem` in the Pi/BeagleBone GPIO drivers; the PCI BAR mapping in `transport_ccat.c` |
| `cap_dac_override` | writing a root-owned file as a process that is not root, on the mainstream path: `/proc/irq/$n/smp_affinity` `root:root 0644` (`irq_pin.c`), `/dev/cpu_dma_latency` `root:root 0600` (`uspace_rtapi_lib.c`), `/sys/bus/pci/devices/*/resource0` `root:root 0600` (`transport_ccat.c`); and on the Pi, `/dev/mem` `root:kmem 0640` plus debugfs `0700` clock rates |
| `cap_perfmon` | the BPF verifier's pointer-leak gate, `perfmon_capable()`, which `libxdp`'s dispatcher program needs to pass |
| `cap_sys_admin` | the ID-based BPF lookups (`BPF_BTF_GET_FD_BY_ID`, `BPF_PROG_GET_FD_BY_ID`), which `libxdp` uses to inspect an already-attached dispatcher. Those are gated on `capable(CAP_SYS_ADMIN)` specifically — `CAP_BPF` was deliberately not given them, because they enumerate other processes' BPF objects |

The last two rows are a correction, and the way they were wrong is the point of
recording them. A source audit concluded both were unused: no
`perf_event_open`, no `PERF_*`, no `rdpmc`, no mount-family calls. Three runs
on real hardware (6.12 RT, `xdp-native`) said otherwise, and each failed
differently:

| dropped | result |
|---|---|
| `cap_perfmon` | works — but only because `cap_sys_admin` also satisfies `perfmon_capable()` |
| both | `BPF program load failed: Permission denied` / `R1 pointer comparison prohibited` |
| `cap_sys_admin` | `Failed to get BTF 269 of the program` / `couldn't get program fd: Operation not permitted` |

So both are load-bearing, for unrelated reasons, and **the audit is closed:
nothing on the list can be removed.** The `TODO: check what's actually needed`
that `make setuid` has carried for years is answered.

Two things this cost. Neither requirement is in this tree — one lives in the
kernel's verifier, the other in the `bpf()` syscall's permission checks, both
demanded on behalf of a program shipped inside `libxdp`. No amount of grepping
our own source could have found either. And removing capabilities one at a
time is misleading when two of them satisfy the same gate: the first run
"passed" only because the capability it removed was being covered by the next
one on the list.

`cap_dac_override` is not the peripheral thing a first pass made it look like.
Every realtime EtherCAT machine needs it: pinning the NIC's interrupt writes
`/proc/irq/$n/smp_affinity`, and holding the PM QoS floor writes
`/dev/cpu_dma_latency`. Both are root-owned files opened for writing by a
process that deliberately is not root, so nothing but `CAP_DAC_OVERRIDE` gets
them open — and the symptom of removing it would be latency quietly reverting
to whatever the housekeeping CPU does, which is precisely the class of fault
that takes weeks to attribute.

Nothing is trimmed, and now for a reason rather than for caution: every
capability on the list has a caller. The ruling to carry it across verbatim
turned out to be the correct one twice over — the audit that would have
justified trimming was wrong in both directions, understating
`cap_dac_override` and overstating what could go. The list stays verbatim
until a run on real hardware says otherwise.

**The unprivileged phase builds in its own copy of the tree.** §4.3 has the
build phase read the root-owned tree directly, which does not survive contact
with `go mod tidy`: dependency resolution rewrites `go.mod` and `go.sum`, and
the tree is root-owned 0755 precisely so that it cannot. The build phase
therefore mirrors the tree into `/var/cache/stratumak-build/tree` first and
resolves and compiles there. The shared tree stays root-owned and unwritten,
the compiler still never runs as root, and root still consumes only the staged
binary.

**Two authoritative directories, not one** (§4.2). `/var/lib/stratumak/modules`
holds the root-owned copy of each registered module's source and is the source
of truth; `/var/lib/stratumak/gomc` is the build tree derived from the pristine
sources plus those copies, regenerated in full on every rebuild. Deriving it
each time is what makes an upgrade correct: the pristine sources change under
it, and a tree that only ever had modules added would keep compiling the
release it was first built from.

**Behind all of this: the installed gomc tree did not compile, and now does.**
The note assumes throughout that `$(datadir)/linuxcnc/gomc` can be rebuilt; it
could not, and never could — the permission error in §1 was simply the first
thing in the way. `gomc-install` shipped only `*.go`, so every cgo package
arrived without its C sources, and a dozen headers the build needs were
installed nowhere. Five separate causes, all now fixed:

1. The tree ships its own `.c/.h/.cc/.hh` (26 files, previously 9).
2. `SRCHEADERS` gained the headers nothing had installed: `config.h`,
   `hal_priv.h`, `rtapi_task.h`, `uspace_common.h`, `canon_interface.hh`,
   `interp_ext.h`, `interp_inspection.hh`, `interp_parameter_def.hh`,
   `interp_parameter_io.hh`, `rs274ngc_interp.hh`, `tp_debug.h`. `saicanon.hh`
   had moved to `emc/sai/` years ago and the stale path was being swallowed by
   a `-cp`.
3. Headers are installed a **second** time under their source-relative path.
   Several gomc packages include them the way the source tree does
   (`"hal/hal_priv.h"`, `"emc/rs274ngc/interp_parameter_io.hh"`), which
   resolves against `-Isrc` in a build tree and against nothing at all in a
   flat include directory. Both spellings now work under the one `-I` that
   `cgoFlags` already passes; no new include path had to be invented.
4. Four C *implementation* files are `#include`d textually — `emc/tp/tc.c`,
   `blendmath.c`, `spherical_arc.c`, `emc/nml_intf/emcpose.c` — and are
   installed alongside the headers as `SRCINCLUDED_SOURCES`. To a tree that
   has to be rebuildable they are headers in everything but name.
5. `cgoFlags` put its include path in `CGO_CFLAGS`, which never reaches the
   C++ compiler, so `interp_shim.cc` could not find `config.h` while the C
   sources beside it compiled. It is `CGO_CPPFLAGS` now, and the build tree's
   own parent goes first so that a `"gomc/generated/..."` include resolves
   against the sources being compiled rather than an installed copy of them.

One collision had to be broken by hand. `emc/rs274ngc/interp_shim.h` and
`gomc/internal/task/interp_shim.h` were different headers sharing a basename
*and* an include guard, and the flat copy of the first silently won over the
second's own directory. The gomc one is now `task_interp_shim.h`, guard and
all — the smaller blast radius of the two, since nothing outside `internal/task`
referred to it — so no special case remains in the install and neither
consumer has to qualify anything.

`make` now refuses the situation outright: `check-header-collisions` compares
the basenames of everything landing in the flat `$(includedir)/linuxcnc`
against the headers inside the gomc tree, and fails the build naming both
files. The original cost an afternoon precisely because every error pointed at
the innocent file.

Two further build-system repairs fell out of the same work. `internal/halscope/
testrt` is pruned from the install, as `kinstest` already was — shipping `.h`
files had swept its mock `hal.h` and `rtapi.h` into a runtime package. And
`GOMC_SRC_BASE` did not glob `*.cc`, although its own comment claimed the
interp shim was covered, so the one C++ translation unit in the tree was
exactly the file an edit to which rebuilt nothing.

Verified by staging an install and building `cmd/gomc-server` from it, the way
the unprivileged build phase would: a complete 27 MB server, from the installed
tree alone.

**What the Go tests cannot reach.** The privileged half only runs as root on a
machine where the package is installed, so
`src/gomc/cmd/modcompile/verify-privileged-rebuild.sh` covers it instead: run
it with `sudo` after installing the new `.deb`. It checks that postinst left a
usable state tree and build account, that `rebuild`, `add-gomod`, `--install`
and `rm-gomod` all complete, that the capabilities are carried across the
replacement, and — the point of the whole exercise — that nothing under
`/var/cache/stratumak-build` ends up owned by anyone but the build identity,
which is the fingerprint a compiler running as root would leave. It registers
a module whose `init()` prints a marker, because an unreferenced Go constant
never reaches the binary and grepping for one would prove nothing.

It passes, on a machine installed from the built `.deb`: all six sections,
including a `.comp` with a local header installed out of a `0700` directory,
and `/var/cache/stratumak-build` owned end to end by the build identity. The
first attempt did not — it caught the `getcap` bug above, which is the whole
argument for having written it.

An out-of-tree module then went through the flow it was designed for, from an
ordinary project `Makefile`: `make install` builds as the invoking user and
escalates only for `sudo modcompile add-gomod .`, which records the source,
rebuilds unprivileged as `stratumak-build`, and reapplies the capabilities.
That is §1's opening failure — `mkdir /usr/share/linuxcnc/gomc/external:
permission denied` — closed.

**`modcompile --install` drops privilege too.** §6 step 5 asked only for the
relocation, but leaving `gcc` running as root over module-supplied source
would have been §3's first property honoured in one place and abandoned one
directory over. It now takes the same three phases as a server rebuild: root
stages the generated C and the source directory's headers where the build
identity can read them, that identity compiles, root places the `.so`.

Headers only, and only from the source directory, because that directory is
exactly what the compile's include path reached before — `--install` gets run
from wherever the source happens to sit, and copying a stranger's working
directory into a shared cache is not a header search path. The staged C sits
beside those headers, so a relative `#include` resolves by proximity and needs
no `-I` at all.

The condition is "am I root, on a layout that has an unprivileged identity to
be instead", not "am I writing to the cmod directory": `sudo modcompile
--compile` runs the same compiler over the same source and gets the same
treatment.
