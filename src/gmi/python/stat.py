"""gmi.stat — Drop-in replacement for linuxcnc.stat().

Subscribes to the emcstat WebSocket watch channel and maintains a local
cache of all stat fields. Attribute access (stat.task_mode) reads from
the cache. poll() fetches a fresh snapshot synchronously over REST —
classic linuxcnc.stat.poll() was a synchronous NML peek, and drivers
rely on poll() observing the effect of a command they just sent; the
WS cache alone can be up to rate_ms stale.

Usage:
    s = gmi.Stat()
    s.poll()
    print(s.task_mode, s.task_state)
"""

from __future__ import annotations

import asyncio
import http.client
import json
import threading
import urllib.parse
from typing import Any, Optional

try:
    import websockets
except ImportError:
    websockets = None

from gmi import rest_url, ws_url


class _ToolEntry:
    """A tool table entry supporting both indexed and named attribute access.

    Supports both indexed access (entry[0], entry[3], entry[10]) and
    named attributes (.id, .xoffset, etc.) matching the linuxcnc C extension.
    """
    __slots__ = ("_fields",)

    def __init__(self, toolno=0, xoffset=0.0, yoffset=0.0, zoffset=0.0,
                 aoffset=0.0, boffset=0.0, coffset=0.0, uoffset=0.0,
                 voffset=0.0, woffset=0.0, diameter=0.0, frontangle=0.0,
                 backangle=0.0, orientation=0):
        self._fields = (
            toolno, xoffset, yoffset, zoffset, aoffset, boffset,
            coffset, uoffset, voffset, woffset, diameter, frontangle,
            backangle, orientation,
        )

    @classmethod
    def from_dict(cls, d):
        """Create from a REST API tool dict."""
        return cls(
            toolno=d.get("toolno", 0),
            xoffset=d.get("x_offset", 0.0),
            yoffset=d.get("y_offset", 0.0),
            zoffset=d.get("z_offset", 0.0),
            aoffset=d.get("a_offset", 0.0),
            boffset=d.get("b_offset", 0.0),
            coffset=d.get("c_offset", 0.0),
            uoffset=d.get("u_offset", 0.0),
            voffset=d.get("v_offset", 0.0),
            woffset=d.get("w_offset", 0.0),
            diameter=d.get("diameter", 0.0),
            frontangle=d.get("frontangle", 0.0),
            backangle=d.get("backangle", 0.0),
            orientation=d.get("orientation", 0),
        )

    def __getitem__(self, idx):
        return self._fields[idx]

    def __bool__(self):
        return self._fields[0] != 0

    @property
    def id(self):
        return self._fields[0]

    @property
    def xoffset(self):
        return self._fields[1]

    @property
    def yoffset(self):
        return self._fields[2]

    @property
    def zoffset(self):
        return self._fields[3]

    @property
    def aoffset(self):
        return self._fields[4]

    @property
    def boffset(self):
        return self._fields[5]

    @property
    def coffset(self):
        return self._fields[6]

    @property
    def uoffset(self):
        return self._fields[7]

    @property
    def voffset(self):
        return self._fields[8]

    @property
    def woffset(self):
        return self._fields[9]

    @property
    def diameter(self):
        return self._fields[10]

    @property
    def frontangle(self):
        return self._fields[11]

    @property
    def backangle(self):
        return self._fields[12]

    @property
    def orientation(self):
        return self._fields[13]


class Stat:
    """Drop-in replacement for linuxcnc.stat().

    Connects to the emcstat watch channel and caches the latest StatFull.
    All attributes from the stat struct are accessible as properties.
    """

    def __init__(self, instance: str = "milltask"):
        self._instance = instance
        self._data = {}
        self._lock = threading.Lock()
        self._connected = threading.Event()
        self._loop = None
        self._thread = None
        self._ws = None
        self._poll_conn = None
        self._poll_lock = threading.Lock()
        self._start_watch()

    def _start_watch(self):
        """Start background thread for WebSocket watch."""
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        self._connected.wait(timeout=10)

    def _run(self):
        self._loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self._loop)
        try:
            self._loop.run_until_complete(self._connect_and_subscribe())
        except Exception as e:
            import sys
            print(f"gmi.Stat: WebSocket connect failed: {e}", file=sys.stderr)
            self._connected.set()
            return
        self._connected.set()
        self._loop.run_forever()
        self._loop.close()

    async def _connect_and_subscribe(self):
        url = ws_url()
        # Retry connection — REST server may not be ready yet.
        for attempt in range(20):
            try:
                self._ws = await websockets.connect(url)
                break
            except (OSError, ConnectionRefusedError):
                await asyncio.sleep(0.25)
        else:
            raise ConnectionError(f"gmi.Stat: could not connect to {url} after retries")
        # Subscribe to emcstat.get_stat at 50ms
        msg = {
            "action": "subscribe",
            "api": "emcstat",
            "instance": self._instance,
            "func": "get_stat",
            "rate_ms": 50,
        }
        await self._ws.send(json.dumps(msg))
        # Keep a strong reference: asyncio holds tasks only weakly, so an
        # unreferenced recv task can be garbage-collected mid-flight — the
        # socket stays open but nothing reads it and the cache silently
        # freezes at the last-delivered snapshot (observed as a boot-era
        # STATE_ESTOP surfacing mid-run in gmi.Stat).
        self._recv_task = asyncio.get_event_loop().create_task(self._recv_loop())

    async def _recv_loop(self):
        try:
            async for raw in self._ws:
                msg = json.loads(raw)
                if msg.get("type") == "update" and msg.get("func") == "get_stat":
                    # Delta merge: server sends only changed keys after
                    # the initial full snapshot.
                    self._merge_update(msg.get("data", {}))
                elif msg.get("type") == "error":
                    import sys
                    print(f"gmi.Stat: watch error: {msg}", file=sys.stderr)
        except asyncio.CancelledError:
            pass
        except Exception as e:
            import sys
            print(f"gmi.Stat: recv error: {e}", file=sys.stderr)

    def _merge_update(self, data):
        """Merge a snapshot/delta into the cache, dropping out-of-order data.

        Both the WS watch thread and poll() write into the same cache. The
        server bumps stat.heartbeat once per status build, so it is present
        in every WS delta and every poll snapshot — use it to order them: a
        WS delta sampled BEFORE a poll's fresh GET must not overwrite the
        newer data when it arrives a moment after poll() returns (that
        re-introduces exactly the stale-read window poll() exists to close).
        The wrap guard keeps a server restart (heartbeat resets to 0) or an
        i32 rollover from freezing the cache.
        """
        hb = data.get("heartbeat")
        with self._lock:
            if hb is not None:
                last = self._data.get("heartbeat")
                if last is not None and hb < last and (last - hb) < 2**30:
                    return  # stale out-of-order update — drop it
            self._data.update(data)

    def poll(self):
        """Fetch a fresh stat snapshot synchronously (like linuxcnc.stat.poll()).

        The WS watch keeps the cache warm between polls, but a driver that
        polls right after issuing a command must see that command's effect —
        the push cache alone can serve a snapshot up to rate_ms stale. On
        fetch failure the (possibly stale) cache is kept rather than raising:
        drivers poll in loops and the watch channel still converges.
        """
        try:
            data = self._poll_fetch()
        except Exception as e:
            import sys
            print(f"gmi.Stat: poll failed ({e}), keeping cached data", file=sys.stderr)
            return
        self._merge_update(data)

    def _poll_fetch(self):
        """GET the stat snapshot over a persistent keep-alive connection.

        Drivers call poll() in tight loops; a new TCP connection per call
        would be pure connect/teardown churn (and TIME_WAIT buildup). Reuse
        only spares the socket setup — every call is still a fresh GET, so
        the post-command freshness contract of poll() is unchanged. A broken
        or server-closed connection is re-opened once per call.
        """
        path = f"/api/v1/{self._instance}/stat"
        with self._poll_lock:
            for attempt in (0, 1):
                if self._poll_conn is None:
                    u = urllib.parse.urlsplit(rest_url())
                    self._poll_conn = http.client.HTTPConnection(
                        u.hostname, u.port, timeout=10)
                try:
                    self._poll_conn.request("GET", path)
                    resp = self._poll_conn.getresponse()
                    body = resp.read()
                    if resp.status != 200:
                        raise OSError(f"HTTP {resp.status}")
                    return json.loads(body)
                except Exception:
                    self._poll_conn.close()
                    self._poll_conn = None
                    if attempt:
                        raise

    # All known stat attribute names, for dir() support.
    _ALL_ATTRS = {
        # Task
        "task_mode", "task_state", "interp_state", "exec_state",
        "file", "command", "motion_line", "current_line", "read_line",
        "queued_mdi_commands", "optional_stop", "block_delete",
        "task_paused", "g5x_index",
        # Motion
        "motion_mode", "enabled", "inpos", "paused", "feedrate",
        "rapidrate", "max_velocity", "velocity", "distance_to_go",
        "current_vel", "motion_id", "motion_type", "queue", "queue_full",
        # Positions
        "position", "actual_position", "probed_position",
        "g5x_offset", "g92_offset", "tool_offset", "dtg",
        "joint_actual_position", "rotation_xy",
        # Collections
        "joints", "joint", "spindle", "axis",
        "gcodes", "mcodes", "settings",
        "homed", "limit",
        # Scalars
        "kinematics_type", "num_extrajoints", "axis_mask",
        "flood", "mist", "tool_in_spindle", "pocket_prepped",
        "tool_from_pocket",
        "linear_units", "angular_units", "state", "debug",
        "tool_table",
        # NML-parity scalars/arrays
        "cycle_time", "acceleration", "max_acceleration", "active_queue",
        "estop", "interpreter_errcode", "lube_level",
        "probe_tripped", "probe_val", "probing",
        "joint_position", "ain", "aout", "din", "dout",
        # Methods
        "poll", "stop",
    }

    def __dir__(self):
        return sorted(self._ALL_ATTRS)

    # ─── Flat attribute access (matching linuxcnc.stat() API) ───

    # Names that need special handling — skip the generic data[name] lookup.
    _SPECIAL_NAMES = {
        "joints", "joint", "spindle", "axis", "dtg",
        "position", "actual_position", "probed_position",
        "g5x_offset", "g92_offset", "tool_offset",
        "joint_actual_position", "joint_position",
        "gcodes", "mcodes", "settings",
        "homed", "limit",
        "ain", "aout", "din", "dout",
    }

    def machine_units(self):
        """Return an opt-in read-through view whose linear position/offset/limit/
        velocity/accel fields are converted from the server's internal mm to the
        machine's configured units (e.g. inch), matching what classic
        linuxcnc.stat() reports. All other fields pass through unchanged, and the
        base Stat keeps reporting mm. See MachineUnitsStat."""
        return MachineUnitsStat(self)

    def __getattr__(self, name):
        if name.startswith("_"):
            raise AttributeError(name)

        with self._lock:
            data = self._data

        # Direct top-level fields (only for non-special names)
        if name not in self._SPECIAL_NAMES and name in data:
            return data[name]

        # Task fields (s.task_mode → data["task"]["mode"])
        task = data.get("task", {})
        _TASK_MAP = {
            "task_mode": ("mode", 0),
            "task_state": ("state", 0),
            "interp_state": ("interp_state", 0),
            "exec_state": ("exec_state", 0),
            "file": ("file", ""),
            "command": ("command", ""),
            "motion_line": ("motion_line", 0),
            "current_line": ("current_line", 0),
            "read_line": ("read_line", 0),
            "queued_mdi_commands": ("queued_mdi_commands", 0),
            "optional_stop": ("optional_stop", 0),
            "block_delete": ("block_delete", 0),
            "task_paused": ("task_paused", 0),
            "g5x_index": ("g5x_index", 0),
            "call_level": ("call_level", 0),
            "input_timeout": ("input_timeout", 0),
            "program_units": ("program_units", 0),
            "delay_left": ("delay_left", 0.0),
        }
        if name in _TASK_MAP:
            key, default = _TASK_MAP[name]
            return task.get(key, default)

        # Motion fields (s.motion_mode → data["motion"]["mode"])
        motion = data.get("motion", {})
        _MOTION_MAP = {
            "motion_mode": ("mode", 0),
            "enabled": ("enabled", False),
            "inpos": ("in_position", False),
            "paused": ("paused", False),
            "feedrate": ("feedrate", 0.0),
            "rapidrate": ("rapidrate", 0.0),
            "max_velocity": ("max_velocity", 0.0),
            "velocity": ("velocity", 0.0),
            "distance_to_go": ("distance_to_go", 0.0),
            "current_vel": ("current_vel", 0.0),
            "motion_id": ("motion_id", 0),
            "motion_type": ("motion_type", 0),
            "queue": ("queue", 0),
            "queue_full": ("queue_full", False),
            "feed_override_enabled": ("feed_override_enabled", False),
            "adaptive_feed_enabled": ("adaptive_feed_enabled", False),
            "feed_hold_enabled": ("feed_hold_enabled", False),
        }
        if name in _MOTION_MAP:
            key, default = _MOTION_MAP[name]
            return motion.get(key, default)

        # Position fields (return as 9-tuple for linuxcnc.stat() compat)
        _POS_FIELDS = {
            "position", "actual_position", "probed_position",
            "g5x_offset", "g92_offset", "tool_offset",
        }
        if name in _POS_FIELDS:
            return _pos_to_tuple(data.get(name, {}))

        # dtg — position inside motion struct
        if name == "dtg":
            motion = data.get("motion", {})
            return _pos_to_tuple(motion.get("dtg", {}))

        # joint_actual_position / joint_position — arrays of 16 floats
        if name == "joint_actual_position":
            return tuple(data.get("joint_actual_position", [0.0] * 16))
        if name == "joint_position":
            return tuple(data.get("joint_position", [0.0] * 16))

        # Motion analog/digital I/O — 64-wide tuples (floats / ints)
        if name in ("ain", "aout"):
            return tuple(data.get(name) or [0.0] * 64)
        if name in ("din", "dout"):
            return tuple(data.get(name) or [0] * 64)

        # joints (count) → data["joints_count"]
        if name == "joints":
            return data.get("joints_count", 0)

        # joint (array of dicts) — data["joints"]. Expose the classic
        # linuxcnc.stat() joint-dict keys: the only mismatch is the wire's
        # snake_case joint_type vs the classic camelCase jointType, so alias it.
        if name == "joint":
            out = []
            for j in (data.get("joints") or []):
                jc = dict(j)
                jc["jointType"] = jc.get("joint_type", 1)
                out.append(jc)
            return tuple(out)

        # spindle (array of dicts) — data["spindle"]
        if name == "spindle":
            return tuple(data.get("spindle") or [])

        # axis (array of dicts) — data["axis"], indexed by axis number (0=X..8=W)
        if name == "axis":
            return tuple(data.get("axis") or [])

        # gcodes, mcodes, settings
        if name == "gcodes":
            return tuple(data.get("active_gcodes") or [])
        if name == "mcodes":
            return tuple(data.get("active_mcodes") or [])
        if name == "settings":
            return tuple(data.get("active_settings") or [])

        # homed — tuple of booleans per joint
        if name == "homed":
            return tuple(data.get("homed") or [False] * 16)

        # limit — tuple of bitmasks per joint
        if name == "limit":
            return tuple(data.get("limit") or [0] * 16)

        # tool_table — not available via NML stat (uses tooldata_get shared memory).
        # Return a minimal stub so axis.py doesn't crash.
        if name == "tool_table":
            return self._stub_tool_table()

        # Remaining scalars — (json_key, default) so we never return None
        _SCALAR_MAP = {
            "kinematics_type": ("kinematics_type", 0),
            "num_extrajoints": ("num_extrajoints", 0),
            "axis_mask": ("axis_mask", 0),
            "flood": ("flood", 0),
            "mist": ("mist", 0),
            "tool_in_spindle": ("tool_in_spindle", 0),
            "pocket_prepped": ("pocket_prepped", -1),
            "tool_from_pocket": ("tool_from_pocket", 0),
            "linear_units": ("linear_units", 1.0),
            "angular_units": ("angular_units", 1.0),
            "state": ("state", 0),
            "rotation_xy": ("rotation_xy", 0.0),
            "debug": ("debug", 0),
            "heartbeat": ("heartbeat", 0),
            # classic linuxcnc.stat().lube is the on/off flag (wire: lube_on)
            "lube": ("lube_on", 0),
        }
        if name in _SCALAR_MAP:
            key, default = _SCALAR_MAP[name]
            return data.get(key, default)

        raise AttributeError(f"Stat has no attribute {name!r}")

    def _stub_tool_table(self):
        """Fetch tool table via REST API (cached).

        Returns a list indexed by mmap index, matching the linuxcnc
        C extension's tool_table semantics. Index 0 is the spindle tool.
        The REST API returns all entries in mmap index order.

        Cached: only re-fetches when tool_in_spindle changes.
        """
        data = self._data
        current_tool_in_spindle = data.get("tool_in_spindle", 0)
        if (hasattr(self, '_tool_table_cache') and
                self._tool_table_last_spindle == current_tool_in_spindle):
            return self._tool_table_cache

        try:
            from gmi.tools import ToolTable
            tt = ToolTable()
            tools = tt.list()
        except Exception:
            return getattr(self, '_tool_table_cache', [_ToolEntry()] * 56)

        # The REST API returns entries in mmap index order.
        # Index 0 = spindle slot, same as the original C extension.
        result = [_ToolEntry.from_dict(t) for t in tools]
        self._tool_table_cache = result
        self._tool_table_last_spindle = current_tool_in_spindle
        return result

    def invalidate_tool_table(self):
        """Force re-fetch of tool table on next access (after reload_tool_table)."""
        if hasattr(self, '_tool_table_cache'):
            del self._tool_table_cache

    def stop(self):
        """Stop the background WebSocket thread."""
        if self._loop:
            self._loop.call_soon_threadsafe(self._loop.stop)
        if self._thread:
            self._thread.join(timeout=2)


def _pos_to_tuple(pos):
    """Convert a position dict to a 9-tuple (x, y, z, a, b, c, u, v, w)."""
    if isinstance(pos, dict):
        return (
            pos.get("x", 0.0), pos.get("y", 0.0), pos.get("z", 0.0),
            pos.get("a", 0.0), pos.get("b", 0.0), pos.get("c", 0.0),
            pos.get("u", 0.0), pos.get("v", 0.0), pos.get("w", 0.0),
        )
    return (0.0,) * 9


class MachineUnitsStat:
    """Opt-in read-through view over a Stat presenting linear quantities in the
    machine's configured units (e.g. inch) instead of the server's internal mm.

    gomc is mm-everywhere: the base Stat (like the rest of the gmi API) reports
    positions, offsets, limits, velocities and accelerations in millimetres.
    This view converts those on read to the machine's configured units — the
    units classic linuxcnc.stat() reports — for consumers (UIs, parity tests)
    that want them. Non-linear fields (counts, flags, codes, times, override
    ratios, and the per-joint `units` scale itself) pass through unchanged.

    On a millimetre machine (linear_units == 1.0) every conversion is a no-op.

    Usage:
        s = gmi.Stat().machine_units()     # or gmi.MachineUnitsStat(gmi.Stat())
        s.poll()
        s.actual_position                  # inch on an inch machine
    """

    # Position 9-tuple: X/Y/Z (0-2) and U/V/W (6-8) are linear; A/B/C (3-5) angular.
    _POS_LINEAR_IDX = (0, 1, 2, 6, 7, 8)
    _POS_ANGULAR_IDX = (3, 4, 5)
    _POS_FIELDS = frozenset((
        "position", "actual_position", "probed_position",
        "g5x_offset", "g92_offset", "tool_offset", "dtg",
    ))
    _JOINT_ARRAYS = frozenset(("joint_actual_position", "joint_position"))
    # Flat linear scalars (length or length/time).
    _LINEAR_SCALARS = frozenset((
        "velocity", "current_vel", "max_velocity", "distance_to_go",
        "acceleration", "max_acceleration",
    ))
    # Per-joint dict keys carrying a length/velocity in the joint's units.
    # 'units' is the scale factor itself and must NOT be converted.
    _JOINT_SCALED_KEYS = (
        "min_position_limit", "max_position_limit", "min_ferror", "max_ferror",
        "ferror_current", "ferror_highmark", "input", "output", "velocity",
        "backlash",
    )
    _AXIS_SCALED_KEYS = ("min_position_limit", "max_position_limit", "velocity")
    _AXIS_ANGULAR_IDX = frozenset((3, 4, 5))

    def __init__(self, stat):
        self._stat = stat

    def poll(self):
        return self._stat.poll()

    def stop(self):
        return self._stat.stop()

    def _lin(self):
        v = self._stat.linear_units
        return v if v else 1.0

    def _ang(self):
        v = self._stat.angular_units
        return v if v else 1.0

    def __getattr__(self, name):
        if name.startswith("_"):
            raise AttributeError(name)
        value = getattr(self._stat, name)
        lin = self._lin()
        ang = self._ang()
        if lin == 1.0 and ang == 1.0:
            return value  # mm machine — nothing to convert

        if name in self._POS_FIELDS:
            out = list(value)
            for i in self._POS_LINEAR_IDX:
                out[i] *= lin
            for i in self._POS_ANGULAR_IDX:
                out[i] *= ang
            return tuple(out)

        if name in self._LINEAR_SCALARS:
            return value * lin

        if name in self._JOINT_ARRAYS:
            joints = self._stat.joint
            out = []
            for i, v in enumerate(value):
                f = (joints[i].get("units", 1.0) or 1.0) if i < len(joints) else 1.0
                out.append(v * f)
            return tuple(out)

        if name == "joint":
            # Each joint's own units field is its mm->user scale (1.0 when
            # unconfigured, so defaults pass through).
            out = []
            for j in value:
                jc = dict(j)
                f = jc.get("units", 1.0) or 1.0
                for k in self._JOINT_SCALED_KEYS:
                    if k in jc:
                        jc[k] = jc[k] * f
                out.append(jc)
            return tuple(out)

        if name == "axis":
            out = []
            for i, a in enumerate(value):
                ac = dict(a)
                f = ang if i in self._AXIS_ANGULAR_IDX else lin
                for k in self._AXIS_SCALED_KEYS:
                    if k in ac:
                        ac[k] = ac[k] * f
                out.append(ac)
            return tuple(out)

        return value
