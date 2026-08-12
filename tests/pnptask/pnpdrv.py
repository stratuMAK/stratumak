"""Shared driver for the pnptask integration scenarios (phase 7, D27).

pnptask has no GMI surface at all — its whole interface is HAL pins (D12) — so
a scenario driver is a program that writes the job handshake pins, waits, and
reads the outcome pins back. That is what this module is: pin access, the two
handshakes worth writing once (a job, an edge-triggered request), and the
report format the `expected` files are compared against.

Pins are reached over the halcmd REST API rather than by forking `halcmd`:
a fork costs tens of milliseconds per pin, and these scenarios read whole pin
trees in loops. `snapshot()` fetches every pin of a component in one request,
which is also the only way to read several pins as one consistent sample.

Usage from a scenario driver (pnpdrv.sh's `pnp_run` puts this directory on
PYTHONPATH)::

    import pnpdrv

    m = pnpdrv.Machine()
    m.wait_ready()
    m.fill_tray(10, step=0)
    m.run_job(10, 20)
    pnpdrv.check(m.get("proc.20.has-material"), "the press holds the part")
    pnpdrv.done()
"""

import json
import sys
import time
import urllib.error
import urllib.request

import gmi
import stmak_test

# The component names of the sim config. Scenario drivers name pins relative to
# the task ("error-id") or to a sim instance ("0.gripper.miss-count"); the full
# HAL names are assembled here so a renamed instance is one edit.
TASK = "pnp.task"
SIM = "pnp.sim"

# A job is real motion at simulated-machine speeds: the longest ones here (a
# swap, or a probing pass across a tray) take a few seconds. The ceiling only
# bounds the failure — a wait returns as soon as the handshake completes — and
# it honours STMAK_TEST_TIMEOUT_SCALE like every other deadline in the suite.
JOB_TIMEOUT = 60.0

# How long an edge-triggered request pin is held high. The control loop runs at
# 100 Hz and samples once per cycle, so a pulse has to outlast a cycle by a
# comfortable margin — a pulse the loop never saw is a test that hangs waiting
# for its effect.
PULSE_HIGH = 0.1

# How long start-job is held low before a job is commanded. start-job is armed
# rather than edge-detected (§7.7): the module has to sample it low before a
# high counts as a request, and the module clearing it at the end of the
# previous job is not enough — this driver could raise it again inside the same
# 10 ms control cycle, and the module would then never see a low sample.
REARM_LOW = 0.05

# The module's control-loop period (§6.5: one 100 Hz goroutine owns everything).
# Used by cycles(), for the handful of assertions whose subject is that nothing
# happens — there is no "it did not move" event to wait for.
CONTROL_CYCLE = 0.01

# How long to allow for a manual close to be judged (§8.1). PICK_SETTLE_TIME in
# the sim config is 0.1 s and the countdown adds a couple of control cycles on
# top; this is that, with room to spare on a loaded runner.
MANUAL_JUDGE = 0.5


class Error(Exception):
    """A driver-level failure: a bad pin name, a machine that never answered."""


# ─── report format ───────────────────────────────────────────────────────────
#
# Each scenario prints one PASS line per assertion and "ALL OK" at the end, and
# runtests compares that against the directory's `expected` file. The lines are
# the record of what actually ran: a scenario that dies halfway prints fewer of
# them, and the diff names the step it got to.

_checks = 0


def check(cond, label, detail=None):
    """Assert `cond`, reporting it as one PASS line. Raises on failure."""
    global _checks
    if cond:
        _checks += 1
        print("PASS %s" % label)
        sys.stdout.flush()
        return
    msg = "FAIL %s" % label
    if detail is not None:
        msg += " (%s)" % (detail() if callable(detail) else detail)
    print(msg)
    sys.stdout.flush()
    raise AssertionError(msg)


def check_eq(actual, expected, label):
    """check() for a value, with the mismatch in the failure message."""
    check(actual == expected, label,
          lambda: "expected %r, got %r" % (expected, actual))


def done():
    """Close the report. Refuses to sign off on a scenario that asserted nothing."""
    if _checks == 0:
        raise Error("scenario made no assertions")
    print("ALL OK")
    sys.stdout.flush()


# ─── pin access ──────────────────────────────────────────────────────────────


def _api():
    return gmi.rest_url() + "/api/v1/halcmd"


def _request(url, method="GET", body=None, timeout=10):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=timeout) as r:
        raw = r.read()
    return json.loads(raw) if raw else None


def _decode(info):
    """Turn a PinInfo's string value into a Python value of the pin's type."""
    value, typ = info["value"], info["type"]
    if typ == "bit":
        return value == "TRUE"
    if typ == "float":
        return float(value)
    return int(value)


def raw_get(name):
    """Read one HAL pin by its full name."""
    try:
        return _decode(_request("%s/pin/%s" % (_api(), name)))
    except urllib.error.HTTPError as e:
        raise Error("reading pin %s: %s" % (name, e.read().decode(errors="replace"))) from e


def raw_set(name, value):
    """Write one HAL pin by its full name."""
    if isinstance(value, bool):
        value = "1" if value else "0"
    try:
        _request("%s/pin/%s" % (_api(), name), method="PUT", body={"value": str(value)})
    except urllib.error.HTTPError as e:
        raise Error("writing pin %s: %s" % (name, e.read().decode(errors="replace"))) from e


def raw_snapshot(pattern):
    """Read every pin matching a glob, as one request: {name: value}."""
    pins = _request("%s/pins?pattern=%s" % (_api(), pattern))
    return {p["name"]: _decode(p) for p in pins}


# ─── the machine ─────────────────────────────────────────────────────────────


class Machine:
    """The pnptask instance of the sim config, and the sim behind it."""

    # ── naming ──

    def pin(self, name):
        """Full HAL name of a task pin ("error-id" -> "pnp.task.error-id")."""
        return "%s.%s" % (TASK, name)

    def sim_pin(self, name):
        """Full HAL name of a sim pin ("0.gripper.stuck")."""
        return "%s.%s" % (SIM, name)

    # ── raw access ──

    def get(self, name):
        return raw_get(self.pin(name))

    def set(self, name, value):
        raw_set(self.pin(name), value)

    def sim_get(self, name):
        return raw_get(self.sim_pin(name))

    def sim_set(self, name, value):
        raw_set(self.sim_pin(name), value)

    def snapshot(self):
        """Every task pin in one sample, keyed without the instance prefix."""
        pre = TASK + "."
        return {k[len(pre):]: v for k, v in raw_snapshot(TASK + ".*").items()}

    # ── waiting ──

    def wait(self, pred, desc, timeout=None):
        """Poll a predicate over a fresh pin snapshot until it holds."""
        return stmak_test.wait_until(lambda: pred(self.snapshot()), desc,
                                     timeout=timeout, interval=0.02,
                                     detail=lambda: self._state())

    def wait_position(self, picker, x, y, tol=0.5, timeout=None):
        """Wait for a picker to arrive at a taught position.

        picker.N.pos-x/pos-y report where the picker is (feedback + offset,
        D21), which is the frame stations and tray corners are taught in — so
        these compare directly against INI coordinates.
        """
        px, py = "picker.%d.pos-x" % picker, "picker.%d.pos-y" % picker
        return self.wait(
            lambda s: abs(s[px] - x) <= tol and abs(s[py] - y) <= tol,
            "picker %d to reach (%g, %g)" % (picker, x, y), timeout=timeout)

    def wait_still(self, picker=0, timeout=None):
        """Wait for a picker's reported position to stop changing."""
        px, py = "picker.%d.pos-x" % picker, "picker.%d.pos-y" % picker
        last = {}

        def _still():
            s = self.snapshot()
            was, last["p"] = last.get("p"), (s[px], s[py])
            return was is not None and was == last["p"]

        return stmak_test.wait_until(_still, "picker %d to come to a stop" % picker,
                                     timeout=timeout, interval=0.05)

    def cycles(self, n=5):
        """Let n control cycles pass.

        Only for assertions whose subject is that *nothing* happened — an
        ignored jog pin, a machine that stays off. There is no event to wait
        for in those, so the wait is a duration and the test says why.
        """
        time.sleep(n * CONTROL_CYCLE)

    def wait_manual_judged(self):
        """Give the module time to judge a manual close against the gripper.

        §8.1: a manual close over a retained record is judged once the gripper
        feedback has settled — pick-settle-time later, in the control loop. The
        verdict is not published on any pin (it is a record, not an output), so
        there is nothing to poll: what follows is a fixed wait long enough to
        cover the settle countdown, and the assertion is made on the next job's
        behaviour, which is where the record actually matters.
        """
        time.sleep(MANUAL_JUDGE)

    def _state(self):
        """The pins a failing wait wants named — the machine in one line."""
        try:
            s = self.snapshot()
        except Exception as e:  # a broken diagnostic must not mask the timeout
            return "state unavailable: %s" % e
        return ("busy=%s start-job=%s error=%s error-id=%s homed=%s machine-is-on=%s"
                % (s.get("busy"), s.get("start-job"), s.get("error"),
                   s.get("error-id"), s.get("homed"), s.get("machine-is-on")))

    def wait_ready(self, timeout=60.0):
        """Wait for stmakd to serve REST and for pnptask to enable the machine.

        machine-on is held high in the HAL file, so machine-is-on going high is
        the module having resolved its motion stack, pushed the configuration
        and completed the enable handshake — the first moment a job can be
        commanded. The REST connection is retried rather than required, because
        this is called against a server that is still starting up.
        """
        def _up():
            try:
                return self.get("machine-is-on")
            except (urllib.error.URLError, OSError, Error):
                return False

        return stmak_test.wait_until(_up, "the pnptask machine to come up",
                                     timeout=timeout, interval=0.05)

    def wait_idle(self, timeout=None):
        """Wait for the job handshake to be complete: busy low, start-job low."""
        return self.wait(lambda s: not s["busy"] and not s["start-job"],
                         "the module to go idle",
                         timeout=JOB_TIMEOUT if timeout is None else timeout)

    # ── requests ──

    def pulse(self, name, high=PULSE_HIGH):
        """Drive an edge-triggered request pin high, then low again.

        home, error-reset, set-full/set-empty and the manual picker pins are
        rising edges primed against their startup state (D26). The pin has to
        go back low afterwards or the next request would not be an edge.
        """
        self.set(name, True)
        time.sleep(high)
        self.set(name, False)

    def start_job(self, origin, dest, step=0, deadzone=0):
        """Raise the start-job handshake. Does not wait — see await_job.

        The parameters are written before the request, in the order the design
        says a PLC writes them (§7.7): start-job is sampled first in the
        module's snapshot, so a request seen high is a request whose parameters
        were already there.
        """
        self.set("start-job", False)
        time.sleep(REARM_LOW)
        self.set("origin-id", origin)
        self.set("dest-id", dest)
        self.set("process-step", step)
        self.set("deadzone-select", deadzone)
        self.set("start-job", True)

    def await_job(self, timeout=None):
        """Wait for a running job to complete and return its error id.

        The module clears start-job itself whichever way the job went (§7.4),
        and publishes the diagnosis before it does, so the error id read after
        this describes the job that just ended.
        """
        self.wait(lambda s: not s["start-job"], "the job to complete",
                  timeout=JOB_TIMEOUT if timeout is None else timeout)
        self.wait_idle()
        return self.get("error-id")

    def run_job(self, origin, dest, step=0, deadzone=0, timeout=None):
        """Command one job and return its error id (0 = it worked)."""
        self.start_job(origin, dest, step=step, deadzone=deadzone)
        return self.await_job(timeout=timeout)

    def clear_error(self):
        """Pulse error-reset and wait for the latch to actually clear."""
        self.pulse("error-reset")
        self.wait(lambda s: not s["error"] and s["error-id"] == 0,
                  "the error latch to clear")

    # ── telling the module what the world holds ──

    def fill_tray(self, station, step=0):
        """Declare every slot of a tray station occupied at `step` (D8)."""
        self.set("process-step", step)
        self.pulse("tray.%d.set-full" % station)
        self.wait(lambda s: not s["tray.%d.empty" % station],
                  "tray %d to report itself stocked" % station)

    # ── the simulated field devices (pnpsim) ──

    def miss(self, gripper, count):
        """Make the gripper's next `count` closes grip nothing (-1 = all)."""
        self.sim_set("%d.gripper.miss-count" % gripper, count)

    def holding(self, gripper):
        """Whether the simulated gripper physically holds a part."""
        return self.sim_get("%d.gripper.holding" % gripper)

    def wait_gripper(self, gripper, holding, timeout=None):
        """Wait for a gripper to finish opening or closing.

        The close command and the feedback that answers it are a settle time
        apart — that is the whole point of simulating a gripper rather than
        looping the command back — so a picker's close output going low is not
        yet a part released.
        """
        return stmak_test.wait_until(
            lambda: self.holding(gripper) == holding,
            "gripper %d to %s" % (gripper, "grip" if holding else "let go"),
            timeout=timeout, interval=0.02)

    def stick_gripper(self, gripper, stuck=True):
        """Freeze a gripper's feedback: it never actuates."""
        self.sim_set("%d.gripper.stuck" % gripper, stuck)

    def stick_fixture(self, fixture, stuck=True):
        """Freeze a fixture's released feedback: it never answers."""
        self.sim_set("%d.fixture.stuck" % fixture, stuck)

    def set_busy(self, station, busy=True):
        """Drive a process station's busy input (D15)."""
        self.set("proc.%d.busy" % station, busy)

    def reset_sim(self):
        """Put every injectable fault back to "the machine works"."""
        for i in (0, 1):
            self.miss(i, 0)
            self.stick_gripper(i, False)
            self.stick_fixture(i, False)
        for s in (20, 21):
            self.set_busy(s, False)
