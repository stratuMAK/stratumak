"""gmi.command — Drop-in replacement for linuxcnc.command().

Sends commands to the emccmd REST endpoint. Method names match
linuxcnc.command(): cmd.state(), cmd.auto(), cmd.jog(), etc.

Usage:
    c = gmi.Command()
    c.mode(gmi.MODE_MDI)
    c.mdi("G0 X10")
    c.wait_complete()
"""

from __future__ import annotations

import json
import sys
import urllib.request
from typing import Optional

from gmi import rest_url, resolve_instance

# Socket timeout for a plain command POST. Every endpoint but /wait-complete
# returns as soon as the command is registered, so this only has to cover
# transport, not machine work.
_SOCKET_TIMEOUT = 10.0

# Extra socket headroom on top of a server-side wait. /wait-complete blocks in
# the server for up to its `timeout` and sends nothing until it resolves, so
# the socket must outlive the wait itself — otherwise the read times out first
# and raises instead of delivering the -1 the caller is waiting to see.
_SOCKET_MARGIN = 10.0


class Command:
    """Drop-in replacement for linuxcnc.command().

    All methods correspond to REST calls to the emccmd API.
    """

    def __init__(self, instance: str = None):
        self._base = rest_url() + "/api/v1/" + resolve_instance(instance)

    def _post(self, path: str, data: dict = None,
              timeout: float = _SOCKET_TIMEOUT) -> dict:
        """Send POST request to the emccmd REST endpoint."""
        url = self._base + path
        body = json.dumps(data or {}).encode("utf-8")
        req = urllib.request.Request(
            url, data=body,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as e:
            err_body = e.read().decode("utf-8", errors="replace")
            print(f"gmi.Command: {e.code} {url}: {err_body}", file=sys.stderr)
            raise

    def state(self, state: int):
        """Set task state (STATE_ESTOP, STATE_ON, etc.)."""
        return self._post("/state", {"state": state})

    def mode(self, mode: int):
        """Set task mode (MODE_MANUAL, MODE_MDI, MODE_AUTO)."""
        return self._post("/mode", {"mode": mode})

    def auto(self, cmd: int, line: int = 0):
        """Auto program control (AUTO_RUN, AUTO_STEP, AUTO_PAUSE, etc.)."""
        return self._post("/auto", {"cmd": cmd, "line": line})

    def mdi(self, command: str):
        """Execute MDI command string."""
        return self._post("/mdi", {"command": command})

    def jog(self, jog_type: int, jjogmode: bool, axis_or_joint: int,
            velocity: float = 0.0, distance: float = 0.0):
        """Jog an axis or joint.

        Synchronous like every other command: an earlier version queued jogs
        on a private async worker while abort()/state() posted directly, so a
        queued jog could be delivered AFTER an abort the caller saw complete
        (review finding GP-4). Classic NML serialized all commands on one
        channel; so does this. Velocity is mm/s at this boundary.
        """
        return self._post("/jog", {
            "jog_type": jog_type,
            "jjogmode": bool(jjogmode),
            "axis_or_joint": axis_or_joint,
            "velocity": velocity,
            "distance": distance,
        })

    def jog_stop(self, jjogmode: bool, axis_or_joint: int):
        """Stop a jog (synchronous — ordered with all other commands)."""
        return self._post("/jog-stop", {
            "jjogmode": bool(jjogmode),
            "axis_or_joint": axis_or_joint,
        })

    def spindle(self, direction: int, speed: float = 0.0,
                spindle: int = 0, wait: int = 0):
        """Control spindle (SPINDLE_FORWARD, SPINDLE_OFF, etc.).

        DIVERGENCE from classic linuxcnc.command().spindle() (finding GP-21):
        classic reads the first optional positional arg as the SPINDLE NUMBER
        for OFF/INCREASE/DECREASE/CONSTANT and as the speed for FORWARD/
        REVERSE. This signature is fixed — a classic-style positional
        ``spindle(SPINDLE_OFF, 1)`` would be read as speed=1, spindle 0.
        Multi-spindle callers must pass ``spindle=`` by keyword.
        """
        return self._post("/spindle", {
            "cmd": direction,
            "speed": speed,
            "spindle_num": spindle,
            "wait": wait,
        })

    def home(self, joint: int):
        """Home a joint (-1 = all)."""
        return self._post("/home", {"joint": joint})

    def unhome(self, joint: int):
        """Unhome a joint (-1 = all)."""
        return self._post("/unhome", {"joint": joint})

    def override_limits(self, joint: int = 0):
        """Override soft limits (joint < 0 resumes normal limit checking)."""
        return self._post("/override-limits", {"joint": joint})

    def teleop_enable(self, enable: bool):
        """Enable/disable teleop mode."""
        return self._post("/teleop", {"enable": bool(enable)})

    def feedrate(self, rate: float):
        """Set feed override (0.0 - 1.0+)."""
        return self._post("/feed-override", {"rate": rate})

    def spindleoverride(self, rate: float, spindle: int = 0):
        """Set spindle speed override."""
        return self._post("/spindle-override", {"rate": rate, "spindle_num": spindle})

    def rapidrate(self, rate: float):
        """Set rapid override (0.0 - 1.0)."""
        return self._post("/rapid-override", {"rate": rate})

    def set_feed_override(self, enable):
        """Enable/disable feed override (classic EMC_TRAJ_SET_FO_ENABLE)."""
        return self._post("/feed-override-enable", {"enable": bool(enable)})

    def set_feed_hold(self, enable):
        """Enable/disable feed hold (classic EMC_TRAJ_SET_FH_ENABLE)."""
        return self._post("/feed-hold-enable", {"enable": bool(enable)})

    def set_spindle_override(self, enable, spindle: int = 0):
        """Enable/disable spindle-speed override (EMC_TRAJ_SET_SO_ENABLE)."""
        return self._post("/spindle-override-enable",
                          {"enable": bool(enable), "spindle_num": spindle})

    def maxvel(self, velocity: float):
        """Set maximum velocity."""
        return self._post("/max-velocity", {"velocity": velocity})

    def flood(self, on):
        """Flood coolant on/off."""
        return self._post("/flood", {"on": bool(on)})

    def mist(self, on):
        """Mist coolant on/off."""
        return self._post("/mist", {"on": bool(on)})

    def brake(self, on, spindle: int = 0):
        """Spindle brake engage/release."""
        return self._post("/brake", {"on": bool(on), "spindle_num": spindle})

    def abort(self):
        """Abort current operation."""
        return self._post("/abort")

    def task_plan_synch(self):
        """Synchronize task planner."""
        return self._post("/task-plan-synch")

    def set_optional_stop(self, on: bool):
        """Set optional stop."""
        return self._post("/optional-stop", {"on": bool(on)})

    def set_block_delete(self, on: bool):
        """Set block delete."""
        return self._post("/block-delete", {"on": bool(on)})

    def load_tool_table(self, file: str = ""):
        """Reload tool table from file (empty string = default table)."""
        return self._post("/load-tool-table", {"file": file})

    def program_open(self, filename: str):
        """Open a program file."""
        return self._post("/program-open", {"file": filename})

    def wait_complete(self, timeout: float = 5.0) -> int:
        """Wait for the task to settle. Returns RCS_DONE (1), or raises.

        A wait that did not happen — a machine-side timeout, or the task not
        being ready — raises ``urllib.error.HTTPError`` carrying the server's
        reason. It used to arrive as -1 in a normal HTTP 200 body, because REST
        dispatch crossed the C ABI, which has no channel for an error; a caller
        that discarded the return could not tell it from success. A Go provider
        now serves REST directly, so a failed wait cannot be mistaken for a
        settled machine.

        A command that *errors on the machine* is a different thing and does not
        raise here: those travel on the error channel and leave the task
        settled. Read them via gmi.ErrorChannel.

        See lib/python/gomc_test.py, which turns the HTTPError into a Timeout
        naming the deadline.

        A negative timeout is meaningless (the IDL constrains it to >= 0); the
        socket deadline is floored so it never goes negative and raises a local
        urllib ValueError instead of reaching the server.
        """
        return self._post("/wait-complete", {"timeout": timeout},
                          timeout=max(0.0, timeout) + _SOCKET_MARGIN)

    def debug(self, level: int):
        """Set debug level (bitmask of DEBUG_* flags)."""
        return self._post("/debug", {"debug": level})

    def set_jog_axis(self, axis: int):
        """Set the active jog axis (0=X, 1=Y, ...)."""
        return self._post("/jog-axis", {"axis": axis})

    def set_jog_increment(self, increment: float):
        """Set jog increment (0 = continuous)."""
        return self._post("/jog-increment", {"increment": increment})

    def set_jog_speed(self, speed: float):
        """Set linear jog speed (units/sec)."""
        return self._post("/jog-speed", {"speed": speed})

    def set_ajog_speed(self, speed: float):
        """Set angular jog speed (deg/sec)."""
        return self._post("/ajog-speed", {"speed": speed})
