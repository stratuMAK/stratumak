# motion-logger parity vs LinuxCNC 2.9.8

Certifies the stratuMAK milltask's `tests/motion-logger/` gold against the **real
LinuxCNC 2.9.8 milltask**, by diffing the two through a shared normalizer that
strips format spelling and NML-vs-GMI init/config noise. What survives is real
milltask behaviour to adjudicate.

This is a **validation harness**, not a runtests test. It does not run in CI.
The everyday CI test is `tests/motion-logger/basic/` itself, which regresses the
stratuMAK gold against stmak. This harness answers the *other* question: is that gold
actually right, or is it enshrining a regression vs 2.9?

## Why this works with zero new instrumentation

The stratuMAK interceptor cmod (`src/cnc/motion-logger/motion_logger_cmod.c`) is a
near-verbatim port of the classic 2.9 `src/cnc/motion-logger/motion-logger.c`;
the `SET_LINE` line format is byte-identical. And the 2.9 tree already ships the
same `tests/motion-logger/basic/` test with checked-in gold captured by the real
2.9.8 milltask. So both baselines already exist — the harness just normalizes
and diffs them. No re-run of either stack is required.

(The fake-motion-behind-the-old-logger vs real-motmod-behind-the-cmod difference
does **not** perturb the logged stream for these programs: the `g0` rapids are
numerically identical on both sides, because per-axis vel/acc blending is
computed in canon, above the motion boundary. Escalate to the
`tests/milltask-parity/` `motcmd_trace` oracle — real motmod on both sides — only
for programs whose emission is gated on real motion feedback: probing,
wait-for-at-speed, mid-run override timing.)

## Use

The 2.9.8 oracle is **vendored** into `oracle-2.9/` (pinned, with provenance in
`oracle-2.9/MANIFEST`), so comparing needs no 2.9 tree checked out:

```bash
./compare.sh                 # every target (basic/{g0,g1,s} + mountaindew + m98m99-12)
./compare.sh basic           # all three basic/ segments
./compare.sh basic/g1        # one segment
./compare.sh mountaindew m98m99-12
./compare.sh --self          # determinism check: stmak gold vs itself (all PARITY)
```

Exit 0 = parity for every requested target; 1 = at least one diverged (diff
printed). Every run prints the vendored oracle's 2.9 commit/date as a banner.

To (re)vendor or refresh the oracle from a 2.9 tree:

```bash
LCNC29=~/source/linuxcnc-2.9 ./sync-oracle.sh   # default ~/source/linuxcnc-2.9
git diff oracle-2.9/                            # shows any oracle drift
```

Machine bring-up is excluded on both test layouts: `basic` keeps its preamble in
a separate `builtin-startup` segment that is never vendored; the combined
captures (`mountaindew`, `m98m99-12`) inline the preamble, so `compare.sh`
normalizes them with `--strip-preamble` (drops everything before the first
move-class opcode — see `canonicalize.awk`). Both are pure NML-vs-GMI plumbing.

## Layout

```
targets.sh        shared target table (label, oracle path, stmak path, strip)
sync-oracle.sh    vendor 2.9 golds into oracle-2.9/ + write MANIFEST (reads $LCNC29)
oracle-2.9/       vendored 2.9.8 golds (committed) + MANIFEST provenance
canonicalize.awk  keep behavioural motion opcodes, drop init/config, strip fields
normalize.sh      canonicalize.awk | %.4f round   [--strip-preamble]
compare.sh        diff stmak gold vs vendored oracle through the normalizer
PARITY_FINDINGS.md  adjudication log (the load-bearing certification record)
```

Adding a test: vendor its 2.9 gold (add a row to `targets.sh`, re-run
`sync-oracle.sh`) and it joins `compare.sh`. Coverage today is every parity-able
motion-logger test; `startup-gcode-abort` (incomparable oracle — 2.9 defers the
startup move, stratuMAK dispatches it at estop) and the two `abort/*-crazy-move`
tests (real core_sim motion, judged by axis position not a logger gold) are
enabled in runtests but excluded here (see `targets.sh` / `PARITY_FINDINGS.md`).

## What the normalizer keeps vs drops

`canonicalize.awk` (full policy in its header):

- **Keeps** the behavioural canon/motion stream: `SET_LINE`, `SET_CIRCLE`,
  `PROBE`, `RIGID_TAP`, all `SPINDLE_*`, `SET_SPINDLESYNC`, `SET_VEL`, `SET_ACC`,
  `SET_TERM_COND`, `SET_OFFSET`, `SET_DOUT`/`SET_AOUT`.
- **Drops** init/config/mode plumbing: `SET_NUM_JOINTS`, `SETUP_ARC_BLENDS`,
  `SET_*_LIMIT`, per-joint/axis setup, `SET_WORLD_HOME`, `JOINT_ACTIVATE`, mode
  toggles, `ENABLE`/`DISABLE`, `*_ENABLE`, `FEED_SCALE`/`RAPID_SCALE`,
  `JOG_ABORT`, amplifier enable/disable, `ABORT`, `SET_SPINDLE_PARAMS`.
- **Strips fields** with no cross-tree meaning: `id=` on moves (the trees number
  motion ids differently — a GUI current-line tracker, not a motion parameter),
  the 2.9-only `ini_maxvel=` tail on `SET_VEL`, and the trailing
  `flags=`/`motion_type=` field on `SET_SPINDLESYNC`.
- **Rounds** every float to `%.4f` (the tolerance knob).

## Workflow

1. Run `./compare.sh`; each surviving diff is a candidate finding.
2. For each finding: **real bug** → fix stratuMAK, re-capture the affected
   `expected.*`, re-run; **benign** → record it in `PARITY_FINDINGS.md` with the
   reason it is acceptable.
3. When `PARITY_FINDINGS.md` accounts for every surviving diff, the committed
   stratuMAK gold is *certified against 2.9.8*. Routine runs of this harness become
   optional; `tests/motion-logger/` carries the frozen, certified gold from
   there on.

This is the one moment the 2.9 linkage exists — after the freeze the runtests
tests only guard against regressing away from the certified stratuMAK gold, so the
adjudication in `PARITY_FINDINGS.md` is load-bearing. Keep it.
