# Installing external modules on a packaged system — design note

Status: **proposal, nothing implemented.** Written 2026-08-01 after `stratumak`
and `stratumak-dev` 0.1.0 were installed on a real machine and an out-of-tree Go
module was built against them for the first time.

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

Symlink rather than copy. A 19 MB binary is not duplicated on every install; an
upgrade is picked up automatically in the common case with **no maintainer-script
logic at all**; and "is this server locally modified?" reduces to `test -L`.

### 4.2 The `/var` tree is derived, not authoritative

Source of truth is the registry plus a root-owned copy of each registered
module's source. The tree is regenerated from those. "Is this tree trustworthy"
then reduces to "was every entry added through a privileged, attributable step",
which is a checkable property rather than a hope.

The module source copies must be root-owned *once copied* — not merely the
generated files, or the vector returns one directory down.

### 4.3 Privilege split inside `add-gomod` / `rebuild`

```
root   record and copy THIS source directory into the tree     <- the trust decision
drop   re-exec as SUDO_UID: regenerate imports, go build       <- compiler never root
root   install the binary, reapply capabilities                <- irreducibly privileged
```

Privilege dropping in Go needs **re-exec, not `setuid`**: `setuid(2)` affects
only the calling thread and the Go runtime is multithreaded. The supported shape
is for `modcompile` to re-exec itself for the build phase under the invoking
user's uid, taken from `SUDO_UID`.

Reapplying capabilities needs `CAP_SETFCAP`, i.e. root, which is an independent
reason the install phase stays privileged even if the build does not.

### 4.4 Staleness instead of rebuilding on upgrade

`EMC2Version` is baked in by ldflags, so a locally rebuilt server records the
version of the tree it was built from. Comparing that against the installed gomc
tree at startup gives a "your local build is stale, run `modcompile rebuild`"
signal almost for free, and catches the real hazard: a server built against
gomc sources from an older release, now facing newer cmods.

On upgrade:

- active server is a symlink → nothing to do, the new pristine binary is live
- active server is a real file and **no** external modules are registered →
  restore the symlink
- active server is a real file and external modules **are** registered → leave it
  alone, mark it stale, tell the administrator

**Nothing is recompiled in a maintainer script.** A full server build is minutes
long, needs `golang-go` and a warm module cache, and can fail — and a failing
postinst leaves the package half-configured and `apt` wedged. If the rebuild
should be automatic, a systemd one-shot after upgrade is the supportable form.

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
what people usually expect from a local override. Decide before implementing,
since the same relocation covers both directories.

**Nothing grants realtime privileges on a packaged system.** `make setuid` is a
RIP-only make target; the postinst sets `memlock` in `limits.conf` and nothing
else. Whatever layout is adopted, the capabilities have to land on the *active*
binary and be reapplied after every rebuild. That is unspecified today.

**Purge.** Anything written under `/var/lib/stratumak` is untracked by dpkg and
must be removed by `postrm purge`, alongside the `limits.conf` line already
handled there.

## 6. Implementation order

1. `modcompile rebuild`: split unprivileged build from privileged install; add
   `SUDO_UID` re-exec. Fail with *"needs root: try sudo"* rather than a raw
   `mkdir: permission denied`. **This alone unblocks `add-gomod`.**
2. Introduce `/var/lib/stratumak/gomc`, move `external/` and the generated
   registry there, `GOMC_DIR` already exists as the override knob.
3. Move the pristine server to `/usr/libexec/stratumak`, add the two symlinks,
   teach postinst the three upgrade cases.
4. Staleness stamp and the startup check.
5. Local cmods to `/var/lib/stratumak/cmod` plus the launcher search path, once
   the shadow-vs-collide question is settled.
6. `postrm purge` cleanup.
