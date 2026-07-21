# GOMC / LinuxCNC-fork — Safety Boundary

**Status:** draft (2026-07-22). Cross-cutting deliverable for
[`PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md) → *Safety boundary document*.

> **This document is not a risk assessment, not a certification, and not legal or
> conformity advice.** It states where the boundary lies between the gomc
> software and the functional safety of a machine. Each machine builder /
> integrator remains **solely responsible** for the hazard analysis, the choice
> of required safety level, the implementation and validation of every safety
> function, and for the conformity of the delivered machine (e.g. under the EU
> Machinery Regulation (EU) 2023/1230 / Directive 2006/42/EC, or the equivalent
> in the market of use).

---

## 1. Purpose and scope

gomc is a **general-purpose machine-control software platform** (a LinuxCNC
fork) that runs as application software on a general-purpose operating system
(Linux with a real-time layer). It is a **framework**: it does not embody the
control logic of any one machine — that is supplied per machine as
configuration, HAL wiring, G-code, and components.

This document is **platform-general**. It does not certify any machine. Its job
is to draw one line clearly:

> **What the software may do, versus what must be delegated to certified safety
> hardware.**

## 2. Core principle

**Safety-critical functions — those that protect persons from hazards — must
NOT rely on gomc / LinuxCNC. They must be implemented in certified safety
hardware that is independent of the software and cannot be defeated by it.**

gomc is **not developed, verified, or rated to any functional-safety standard.**
It carries no ISO 13849-1 Performance Level (PL), no IEC 62061 / IEC 61508 SIL,
and no safety case. Every output it produces must be treated as **non-safe** by
design. No amount of software review, testing, or real-time hardening in this
repository changes that: those efforts improve **reliability and correctness**,
which is not the same as a rated, validated **safety function**.

## 3. Machine-builder responsibilities

Because gomc is a framework, the safety concept is necessarily machine-specific.
For each machine built on this platform, the builder/integrator must:

1. **Perform a hazard analysis / risk assessment** (e.g. per ISO 12100) for the
   concrete machine and its foreseeable use and misuse.
2. **Determine the required safety level** for each identified safety function
   (e.g. required PL per ISO 13849-1, or SIL per IEC 62061).
3. **Implement each safety function in certified safety hardware** to at least
   that level, independent of gomc — such that the function is achieved and
   maintained regardless of any software state, fault, crash, delay, or
   compromise.
4. **Validate** the safety functions on the assembled machine.

The safety functions that a given machine requires depend entirely on its
hazard analysis. Commonly relevant ones (non-exhaustive, machine-dependent):

- **Emergency stop** (the simplest and most common case — a certified E-stop
  chain that removes drive power or triggers a safe stop);
- **Guard / door interlocks**, with guard locking where required;
- **Safe Torque Off (STO) / safe stop** of servo and spindle drives;
- **Safely-limited speed (SLS) / safe slow motion** for setup or teach modes;
- **Enabling / hold-to-run devices**, two-hand controls;
- **Protective overtravel limits** where end-of-travel is itself a hazard.

The simplest case is an E-stop chain; a machine's analysis may require several of
the above, or others not listed. **The determination is the builder's, per
machine.**

## 4. What "not load-bearing" means

For every gomc software module, the assertion this document upholds is:

> **No operator-safety function depends on this module.**

The test is a thought experiment the builder must apply to their safety concept:
if the concept would be **violated** by the software

- crashing, hanging, or restarting;
- computing a wrong or late output;
- losing real-time determinism (GC pause, scheduler starvation, priority
  inversion);
- being driven — accidentally or maliciously — by any host that can reach its
  (unauthenticated, see §7) control ports;

then that function is **in the wrong layer** and must be moved into certified
hardware. Design every safety function around the assumption that **gomc can and
eventually will do all of the above.**

## 5. The software "E-stop" is a control feature, not a safety function

gomc/LinuxCNC exposes an **E-stop *state*** in the control layer (the iocontrol
`user-enable-out` HAL pin, driven by the `ESTOP_ON`/`ESTOP_OFF` messages from a
GUI, and the `emc-enable-in` input that stops motion when false). This is a
**machine-state and convenience** mechanism — it lets the operator and the UI
request a stop and reflects enable state — **it is not a safety function.**

By design (see `src/emc/iotask/ioControl.c`), the software enable signal is meant
to be wired **in series** with the real, external E-stop circuitry:

```
  -----|UEO|-----|EEST|--+--|EEI|--+--(EEI)----
                         |         |
                         +--|URE|--+
  UEO  = user-enable-out (software)
  EEST = external ESTOP circuitry (certified hardware)
  EEI  = machine is enabled
  URE  = user-request-enable
```

Here `UEO` (software) is just one contact in the chain. The **`EEST`** block —
the certified E-stop hardware — is what actually opens the chain, removes drive
power / triggers STO, and holds the machine safe **independent of software**. A
crashed, wedged, or compromised gomc must not be able to keep the machine
enabled: that guarantee lives in `EEST`, not in `UEO`.

## 6. Per-module / per-surface software assertions

Each row asserts the module is **non-safety-rated and not load-bearing** for any
operator-safety function. The machine's safety concept must not depend on any of
them.

| Module / surface | Role in the platform | Safety assertion |
|---|---|---|
| **RT motion controller** (`motmod`, `tpmod`, `homemod`) | Computes trajectories; enforces soft limits, velocity/accel limits, homing. | **Non-safe.** Soft limits and controlled stops are process/usability features. RT hardening (see `RT_HARDENING_CHECKLIST.md`) improves determinism, not safety rating. Overtravel *protection*, if hazardous, belongs in hardware. |
| **iocontrol** (`emcio`, `ioControl*.c`) | Software E-stop state, machine enable, tool-change I/O. | **Non-safe.** The software E-stop is a control convenience (see §5); the E-stop *chain* must be certified hardware. |
| **EtherCAT master + lcec driver** (`cmd/ethercat`, master, driver cmods) | Cyclic exchange of process data with EtherCAT slaves; PDO/CoE config. | **Non-safe.** Ordinary process data carries no safety guarantee. **If Safety-over-EtherCAT (FSoE / TwinSAFE) is used, gomc is only a *black channel*:** it transports opaque safety telegrams; the safety function lives entirely in the certified FSoE master/slaves, and telegram loss/corruption/delay is detected by the FSoE watchdog + CRC, **not** by gomc. |
| **ADS/AMS server** (`internal/ads*`) | Lets a TwinCAT-style HMI read/write HAL pins over TCP. | **Non-safe.** Can command machine outputs. No protocol authentication → also a **security** boundary (§7). |
| **HAL** (`pkg/hal`, `hal_lib`) | Signal plumbing between components. | **Non-safe.** A wiring substrate; carries no safety semantics. |
| **task / interp / milltask** (`internal/task`) | Interprets and executes G-code programs and MDI. | **Non-safe.** Program execution; can move axes and toggle I/O. |
| **REST / WebSocket API** (`apiserver`, `halrest`, `inirest`) | Remote control + status for GUIs. | **Non-safe.** Can drive controller commands. No authentication → **security** boundary (§7). |
| **MQTT bridge** (`mqttbridge`) | Publishes/consumes HAL values over MQTT. | **Non-safe.** Telemetry/convenience; not a control-integrity or safety channel. |

*(This table covers the safety-relevant control and field-I/O surfaces. New
modules that can move an axis, toggle an output, or change machine state inherit
the same assertion by default and must be added here.)*

## 7. Network / access-control trust boundary (security — related but distinct)

gomc's control surfaces — the **ADS server** (default TCP `48898`) and the
**REST/WebSocket API** — have **no built-in authentication**. Any host that can
reach them can command machine outputs and, absent the hardening in this repo,
could crash the controller. The crash/DoS surface has been hardened (see the ADS
and network-module review docs), and the **defaults are now loopback-only**
(ADS `$bind 127.0.0.1`; REST/WS bind loopback with a same-origin WS check).

Exposing any of these to a network is an **explicit deployment decision** that
must be gated by:

- an **external authenticated reverse proxy / API gateway** (authentication,
  TLS, and coarse allow/deny live outside gomc — see the security-model item in
  `PRODUCTION_READINESS.md`), and
- **network isolation** of the control segment.

> **This is a *security* boundary, not a *safety* boundary.** Authenticating and
> isolating the control surface prevents unauthorized *access*; it does **not**
> turn the software into a safety layer. §2 still applies in full: even a
> perfectly secured control surface is non-safety-rated, and operator safety
> must still be delegated to certified hardware.

## 8. Convenience / non-safety features the software provides

The platform offers features that improve usability and protect the **process,
tooling, and machine** — e.g. soft travel limits, feed/velocity/accel limits,
controlled program stop, M-code and HAL interlocks, spindle-at-speed gating,
homing. These reduce nuisance and mechanical/process risk and are worth using,
but they run on non-safety-rated software and **must not be counted toward any
required PL/SIL** or substituted for a certified safety function.

## 9. Summary

1. gomc is **non-safety-rated** general-purpose software. Treat every output as
   non-safe.
2. **Operator-safety functions must be certified hardware, independent of
   gomc**, sized to a per-machine hazard analysis.
3. The software E-stop, soft limits, and interlocks are **convenience**, not
   safety.
4. The unauthenticated control ports are a **security** boundary — isolate and
   gate them — which is **separate from** and does not relax the safety boundary.

---

### TODO for the platform owner (facts this framework doc deliberately leaves open)

These are intentionally **not** fixed here because they are machine-specific or
policy decisions the platform owner must record once:

- [ ] Decide whether to publish a short **integrator note** shipped with the
      platform that restates §2–§3 to every machine builder (recommended).
- [ ] State the **supported Safety-over-EtherCAT** posture explicitly if/when
      FSoE is offered (which certified masters/slaves, black-channel assumptions,
      watchdog expectations) — §6 currently states only the general black-channel
      principle.
- [ ] Confirm the **standards references** in §3 match the target markets
      (ISO 12100 / ISO 13849-1 / IEC 62061 are cited as the builder's typical
      toolset; adjust for non-EU markets).
- [ ] Link this document from the operator/integrator manual and the release
      notes so it is not lost.
