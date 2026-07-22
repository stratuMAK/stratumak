"""Shared synchronisation helpers for the gomc runtests suite.

Why this module exists
----------------------
The suite used to synchronise with the machine by sleeping a fixed amount and
then asserting, e.g.::

    c.mdi("M61 Q3")
    c.wait_complete()
    time.sleep(0.1)          # "let the change propagate"
    s.poll()
    assert s.tool_in_spindle == 3

That is a bet, not a wait. On an idle workstation it wins; on a loaded CI
runner it loses, and when it loses the test does not report "timed out waiting
for the tool change" — it reports a confusing value mismatch, or silently
passes having tested nothing. Every waiter here instead polls a *predicate*
against a wall-clock *deadline* and fails loudly, naming what it was waiting
for and what it saw.

Two rules the whole suite depends on:

1. **Never sleep-then-assert.** Use `wait_stat`/`wait_until`/`wait_pin`. A
   deadline that is too long costs nothing on the happy path (the poll returns
   as soon as the predicate holds); a sleep that is too short costs a flake.
2. **Never call `gmi.Command.wait_complete()` bare.** A wait that did not happen
   now reaches the caller as an HTTP error rather than the old silent -1, but
   the useful message is the server's, not urllib's. `gomc_test.Command` (below)
   translates it into a `Timeout` naming the deadline.

Timeouts are deliberately generous. Set GOMC_TEST_TIMEOUT_SCALE=3 to stretch
every deadline in the suite (slow/emulated/race-instrumented runners) without
re-tuning individual numbers.

Usage::

    import gomc_test
    gomc_test.install_constants()      # linuxcnc.STATE_ON etc.

    c = gomc_test.Command()            # strict wait_complete
    s = gomc_test.Stat()
    gomc_test.wait_for_startup(s)

    c.mdi("M61 Q3")
    c.wait_complete()
    gomc_test.wait_stat(s, lambda st: st.tool_in_spindle == 3,
                        "tool 3 in spindle")
"""

import os
import subprocess
import sys
import time
import urllib.error

import gmi
from gmi.command import Command as _GmiCommand
from gmi.constants import INTERP_IDLE, RCS_DONE, STATE_ON

# Default deadline for every waiter. Sized for a loaded shared CI runner, not
# for an idle workstation: on the happy path a poll returns as soon as its
# predicate holds, so a generous ceiling is free. It only bounds the failure.
DEFAULT_TIMEOUT = 30.0

# Poll interval. Fast enough that the wait itself adds no measurable latency,
# slow enough not to spin a core. Waiters that shell out to halcmd override it.
DEFAULT_INTERVAL = 0.02

_SCALE_ENV = "GOMC_TEST_TIMEOUT_SCALE"


class Timeout(Exception):
    """A waiter's deadline expired. Always carries what it waited for."""


def scale() -> float:
    """Multiplier applied to every deadline (GOMC_TEST_TIMEOUT_SCALE)."""
    try:
        v = float(os.environ.get(_SCALE_ENV, "1"))
    except ValueError:
        return 1.0
    return v if v > 0 else 1.0


def _deadline_secs(timeout):
    return (DEFAULT_TIMEOUT if timeout is None else timeout) * scale()


def wait_until(pred, desc, timeout=None, interval=DEFAULT_INTERVAL,
               detail=None):
    """Poll `pred()` until it returns a truthy value; return that value.

    Arguments:
        pred     — zero-arg callable; truthy return ends the wait.
        desc     — what is being waited for, phrased to complete the sentence
                   "timed out after 30.0s waiting for ...". Required: a waiter
                   that cannot say what it wanted produces a useless failure.
        timeout  — seconds, before GOMC_TEST_TIMEOUT_SCALE. None = DEFAULT_TIMEOUT.
        detail   — optional zero-arg callable returning a diagnostic string for
                   the failure message (e.g. the state actually observed).

    Raises Timeout if the deadline expires. `pred` is always evaluated at least
    once, and once more immediately before the deadline check, so a predicate
    that becomes true during the final sleep is still seen.
    """
    secs = _deadline_secs(timeout)
    deadline = time.monotonic() + secs
    while True:
        value = pred()
        if value:
            return value
        if time.monotonic() >= deadline:
            msg = "timed out after %.1fs waiting for %s" % (secs, desc)
            if detail is not None:
                try:
                    msg += " (%s)" % detail()
                except Exception as e:  # a broken diagnostic must not mask the timeout
                    msg += " (detail unavailable: %s)" % e
            raise Timeout(msg)
        time.sleep(interval)


def wait_stat(stat, pred, desc, timeout=None, interval=DEFAULT_INTERVAL,
              detail=None):
    """Poll the status buffer until `pred(stat)` holds.

    Replaces the `time.sleep(...); stat.poll(); assert ...` idiom. Note that
    gmi.Stat.poll() is a synchronous REST GET (not a read of the WS push
    cache), so each iteration observes genuinely fresh server state — what we
    are waiting on is the machine doing the work, not the cache catching up.

    `detail`, if given, is called with the stat to describe what was actually
    observed when the deadline expires.
    """
    def _p():
        stat.poll()
        return pred(stat)

    return wait_until(_p, desc, timeout=timeout, interval=interval,
                      detail=(lambda: detail(stat)) if detail else None)


def wait_for_startup(stat, timeout=None):
    """Wait for the machine to finish booting into a steady, usable state.

    Polls until the status buffer looks initialised rather than merely
    allocated. Replaces both the `wait_for_linuxcnc_startup` copies and the
    bare `sleep 0.5` that test scripts used after spotting the milltask HAL
    component — the component appearing does not mean the machine is ready.
    """
    def _ready(s):
        return (s.angular_units != 0.0
                and s.axis_mask != 0
                and s.cycle_time != 0.0
                and s.linear_units != 0.0
                and s.max_velocity != 0.0
                and s.interp_state == INTERP_IDLE
                and s.state == RCS_DONE)

    def _detail(s):
        return ("interp_state=%d state=%d axis_mask=%d linear_units=%g"
                % (s.interp_state, s.state, s.axis_mask, s.linear_units))

    # Booting takes seconds, not milliseconds — no point hammering REST at the
    # default interval for the whole of it.
    return wait_stat(stat, _ready, "the machine to finish starting up",
                     timeout=timeout, interval=0.05, detail=_detail)


class Command(_GmiCommand):
    """gmi.Command with a wait_complete() that reports a failed wait usefully.

    A wait that did not happen used to arrive as -1 in a normal HTTP 200 body,
    so a caller that ignored the return proceeded against a machine that never
    settled and the failure surfaced somewhere unrelated. The server now reports
    it as an HTTP error, so nothing is silent — but urllib's exception says
    "HTTP Error 500", and what a failing test needs to read is the deadline and
    the machine's own reason. This translates one into the other.

    A command that *errors on the machine* is still not an exception here: those
    travel on the error channel and leave the task settled, so tests can issue
    them (e.g. G10 L1 P0 with no tool) and introspect the resulting state.
    """

    def __init__(self, instance=None):
        super().__init__(instance=instance or gmi.instance())

    def wait_complete(self, timeout=None):
        secs = _deadline_secs(timeout)
        try:
            return super().wait_complete(secs)
        except urllib.error.HTTPError as e:
            reason = e.read().decode("utf-8", errors="replace").strip()
            raise Timeout(
                "wait_complete failed after %.1fs — the task never settled (%s); "
                "any state read after this point is unsynchronised" % (secs, reason)
            ) from e


def Stat():
    """A gmi.Stat. Provided so tests import one module, not two."""
    return gmi.Stat()


def ErrorChannel():
    """A gmi.ErrorChannel. Provided so tests import one module, not two."""
    return gmi.ErrorChannel()


# How long the machine must be observed out of STATE_ON before drain_mdi calls
# it a fault rather than a blip. See the debounce rationale in drain_mdi.
_NOT_ON_GRACE = 0.5


def drain_mdi(stat, timeout=None):
    """Wait for the MDI queue to empty and the interpreter to return to idle.

    gmi's wait_complete has no serial-number tracking, so back-to-back MDIs can
    overrun the queue ("MDI queue full"). c.mdi() is a synchronous POST — the
    command is registered before it returns — so waiting for (queue empty AND
    interp idle) cannot mistake "not started yet" for "finished". Verified in the
    server (internal/task/commands.go, Task.MDI → executeMDI): while the /mdi
    dispatch runs synchronously under cmdMu, Task.MDI either appends to mdiQueue
    (so queued_mdi_commands is already > 0) or calls executeMDI, whose first act
    is setInterpState(InterpReading) BEFORE ExecuteString and before it returns.
    There is no window in which the POST has returned yet the command is neither
    queued nor reflected in interp_state. Since the sequencer blocks on any
    M-code handler, an idle+empty reading also means the handler finished.

    Fails if the machine drops out of STATE_ON: command endpoints report failure
    as an RCS code in a 200 body, which c.mdi() discards, so a machine that
    faulted would otherwise read as a clean no-op run and the test would "pass"
    having executed nothing.

    The not-ON check is debounced rather than tripped on a single sample. A
    transient non-ON snapshot was seen in practice (a boot-era STATE_ESTOP
    surfacing mid-run while the server never left STATE_ON). The known root
    causes have since been fixed — the WS recv task being garbage-collected
    mid-flight, and poll() reading the push cache instead of fetching — but the
    debounce is retained deliberately: a real fault persists and is still
    caught, so the only thing this can cost is a false alarm.
    """
    state = {"not_on_since": None}

    def _settled(s):
        if s.task_state != STATE_ON:
            now = time.monotonic()
            if state["not_on_since"] is None:
                state["not_on_since"] = now
            elif now - state["not_on_since"] > _NOT_ON_GRACE:
                raise Timeout(
                    "machine left STATE_ON during MDI (task_state=%d for >%gs)"
                    " — the command was silently rejected"
                    % (s.task_state, _NOT_ON_GRACE))
            return False
        state["not_on_since"] = None
        # Require an ON snapshot for success too: after a real drop the queue is
        # flushed and the interp forced idle, which would otherwise read as a
        # clean drain.
        return s.queued_mdi_commands == 0 and s.interp_state == INTERP_IDLE

    def _detail(s):
        return ("queued=%d interp_state=%d task_state=%d"
                % (s.queued_mdi_commands, s.interp_state, s.task_state))

    return wait_stat(stat, _settled, "the MDI queue to drain", timeout=timeout,
                     interval=0.005, detail=_detail)


# ─── HAL access (halcmd-backed) ───────────────────────────────────────────────
#
# Each call forks halcmd (~10s of ms), so pin waiters poll more slowly than
# stat waiters — a tight interval would spend the whole deadline on fork/exec
# rather than on waiting.

_PIN_INTERVAL = 0.05


def getp(pin):
    """Read a HAL pin/signal value. Returns bool for TRUE/FALSE, else float."""
    out = subprocess.check_output(["halcmd", "gets", pin])
    v = out.decode().strip().split()[-1]
    if v in ("TRUE", "FALSE"):
        return v == "TRUE"
    return float(v)


def setp(pin, value):
    """Write a HAL pin/signal value."""
    if isinstance(value, bool):
        value = "1" if value else "0"
    subprocess.run(["halcmd", "sets", pin, str(value)], check=True)


def wait_pin(pin, value, timeout=None, tolerance=1e-6):
    """Wait for a HAL pin to reach `value`.

    Floats compare within `tolerance` — a pin driven by the servo thread rarely
    lands on an exact bit pattern.
    """
    def _at_value():
        actual = getp(pin)
        if isinstance(value, bool) or isinstance(actual, bool):
            return actual == value
        return abs(actual - value) <= tolerance

    return wait_until(_at_value, "HAL pin %s to become %s" % (pin, value),
                      timeout=timeout, interval=_PIN_INTERVAL,
                      detail=lambda: "pin is %s" % getp(pin))


class Hal:
    """Minimal halcmd-backed stand-in for the `hal` module.

    The classic tests drove a userspace HAL component to poke the io signals;
    under gomc those signals are reached with halcmd instead. Component
    construction is a no-op — only item access does real work.
    """

    HAL_S32 = HAL_U32 = HAL_FLOAT = HAL_BIT = 0
    HAL_IN = HAL_OUT = HAL_IO = 0

    def component(self, *a, **k):
        return self

    def newpin(self, *a, **k):
        return None

    def ready(self, *a, **k):
        pass

    def connect(self, *a, **k):
        pass

    def __getitem__(self, name):
        return getp(name)

    def __setitem__(self, name, value):
        setp(name, value)


hal = Hal()


# ─── Files written by another process ────────────────────────────────────────


def wait_file_stable(path, timeout=None, settle=0.15, samples=3):
    """Wait for `path` to exist and stop growing, then return its contents.

    For logs a *different* process appends to (motion-logger, halsampler, the
    gomc server log). The suite used to sleep ~0.2s and read, which truncates
    the tail whenever the writer is behind.

    CONTRACT, because this one is a heuristic and the subtlety bites: "finished"
    is inferred from the size holding steady across `samples` consecutive reads
    `settle` apart — a quiet window of (samples-1) * settle. That is only sound
    if the quiet window exceeds the writer's largest gap between writes. Too
    small a window and this returns a half-written file, which is exactly the
    bug it exists to prevent.

    Prefer wait_file_contains() whenever you know what the output should say: a
    real predicate beats inferring completion from silence.
    """
    def _stable():
        try:
            size = os.path.getsize(path)
        except OSError:
            return False
        for _ in range(samples - 1):
            time.sleep(settle)
            try:
                if os.path.getsize(path) != size:
                    return False
            except OSError:
                return False
        return True

    wait_until(_stable, "%s to be written and stop growing" % path,
               timeout=timeout, interval=settle)
    with open(path) as f:
        return f.read()


def wait_file_contains(path, text, timeout=None, count=1):
    """Wait until `path` contains `text` at least `count` times; return contents.

    For assertions on output another process is still appending to — polling
    the real predicate instead of sleeping long enough to "probably" see it.
    """
    def _has():
        try:
            with open(path) as f:
                body = f.read()
        except OSError:
            return None
        return body if body.count(text) >= count else None

    return wait_until(
        _has, "%r to appear %dx in %s" % (text, count, path),
        timeout=timeout, interval=_PIN_INTERVAL,
        detail=lambda: "seen %dx" % _count_in(path, text))


def _count_in(path, text):
    try:
        with open(path) as f:
            return f.read().count(text)
    except OSError:
        return 0


# ─── Constants ───────────────────────────────────────────────────────────────


def install_constants(module=None):
    """Publish gmi's constants on the `linuxcnc` module namespace.

    linuxcnc.so is a deprecation stub under gomc: linuxcnc.command/stat/
    error_channel raise, directing callers to gmi. But the tests (and
    linuxcnc_util) still spell constants `linuxcnc.STATE_ON`, so the name has
    to carry them. Copying them onto the real module object rather than a
    stand-in keeps `import linuxcnc` working in helper modules too.

    Returns the module the constants were installed on.
    """
    if module is None:
        import linuxcnc as module
    import gmi.constants as consts
    for name in dir(consts):
        if not name.startswith("_"):
            setattr(module, name, getattr(consts, name))
    return module


def fail(msg):
    """Abort the test with a diagnostic on stderr and a nonzero exit."""
    print("*** %s" % msg, file=sys.stderr)
    sys.exit(1)
