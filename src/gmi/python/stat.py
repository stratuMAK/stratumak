"""gmi.stat — Drop-in replacement for linuxcnc.stat().

poll() fetches a stat snapshot synchronously over REST and swaps it in
wholesale; attribute access (stat.task_mode) reads from that snapshot.
Attributes are therefore FROZEN between poll() calls, exactly like the
classic NML peek (which memcpy'd EMC_STAT on poll): a multi-attribute
predicate always observes one coherent snapshot. An earlier version kept
a WebSocket watch mutating the cache live between polls — that made
cross-attribute reads mix epochs (review finding GP-7) while adding
nothing once poll() became a fresh GET, so it was removed.

Usage:
    s = gmi.Stat()
    s.poll()
    print(s.task_mode, s.task_state)
"""

from __future__ import annotations

import http.client
import json
import math
import sys
import threading
import time
import urllib.parse
from typing import Any, Optional

from gmi import rest_url, resolve_instance


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


def _empty_tool_entry():
    """An unoccupied tool table slot: 2.9's tooldata_entry_init, whose -1 tool
    number is what distinguishes an empty slot from a real T0 entry."""
    return _ToolEntry(toolno=-1)


# Floor on how often a FAILED tool table fetch is retried (seconds).
_TOOL_TABLE_RETRY_S = 1.0


class Stat:
    """Drop-in replacement for linuxcnc.stat().

    Connects to the emcstat watch channel and caches the latest StatFull.
    All attributes from the stat struct are accessible as properties.
    """

    def __init__(self, instance: str = None,
                 tooltable_instance: str = None):
        self._instance = resolve_instance(instance)
        # tool_table is read from the raw slot store, which is its own module
        # instance (a multi-instance config binds each milltask to its own).
        # Resolved lazily from GET /info on first use, not here: a driver is
        # entitled to construct a Stat before the server is up and poll until it
        # answers, and hardcoding a default here is what made every multi-
        # instance config 404 on the tool table ten times a second.
        self._tooltable_instance = tooltable_instance
        self._tool_table_retry_after = 0.0
        self._data = {}
        self._poll_conn = None
        self._poll_lock = threading.Lock()
        # Liveness of the cached snapshot. poll() keeps the last data on a
        # failed fetch (drivers loop through outages), so without this flag a
        # consumer cannot tell a live reading from a frozen one — a UI would
        # keep presenting a dead server's last state as current.
        self._connected = False
        self._announced_down = False
        # Best-effort initial snapshot: consumers read attributes right after
        # construction (classic stat() was usable immediately). A server that
        # is not up yet is not an error here — drivers run their own
        # poll-until-ready loops.
        try:
            self._data = self._poll_fetch()
            self._connected = True
        except Exception:
            pass

    @property
    def connected(self) -> bool:
        """True while the last poll() reached the server.

        False means the cached snapshot is stale — the server is down, still
        starting, or unreachable. A display must not present stale values as
        the machine's state; see the AXIS offline handling.
        """
        return self._connected

    def poll(self):
        """Fetch a fresh stat snapshot synchronously (like linuxcnc.stat.poll())
        and swap it in wholesale — attributes are frozen until the next poll.
        On fetch failure the previous snapshot is kept rather than raising:
        drivers poll in loops, and classic-style except-and-retry code keeps
        working through a transient server outage. ``connected`` reports which
        of the two you are looking at.
        """
        try:
            data = self._poll_fetch()
        except Exception as e:
            self._connected = False
            # Report the transition, not every failed poll: a UI polling at
            # 10 Hz through a 30 s restart wrote 300 identical lines, which
            # buried the one message that mattered.
            if not self._announced_down:
                self._announced_down = True
                print(f"gmi.Stat: poll failed ({e}), keeping cached data",
                      file=sys.stderr)
            return
        if self._announced_down:
            self._announced_down = False
            print("gmi.Stat: reconnected", file=sys.stderr)
        self._connected = True
        self._data = data

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
        "file", "source_file", "filtering", "filter_progress",
        "command", "motion_line", "motion_file", "current_line",
        "read_line",
        "queued_mdi_commands", "optional_stop", "block_delete",
        "auto_inhibit", "mdi_inhibit",
        "task_paused", "g5x_index",
        # Motion
        "motion_mode", "enabled", "inpos", "paused", "feedrate",
        "rapidrate", "max_velocity", "velocity", "distance_to_go",
        "current_vel", "motion_id", "motion_type", "queue", "queue_full",
        # Positions
        "position", "actual_position", "probed_position",
        "g5x_offset", "g92_offset", "tool_offset", "dtg",
        "relative_position",
        "joint_actual_position", "rotation_xy",
        # Collections
        "joints", "joint", "spindle", "spindles", "axis",
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
        # Server identity / liveness
        "boot_id", "preview_seq", "heartbeat", "connected",
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
        "g5x_offset", "g92_offset", "tool_offset", "relative_position",
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

        # One reference read — the snapshot is replaced atomically by poll()
        # and never mutated, so all lookups below see one coherent epoch.
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
            "source_file": ("source_file", ""),
            "filtering": ("filtering", False),
            "filter_progress": ("filter_progress", 0),
            "command": ("command", ""),
            "motion_line": ("motion_line", 0),
            # File the executing segment came from; motion_line/current_line
            # are numbered within it, not within the loaded program.
            "motion_file": ("motion_file", ""),
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
            # Interlocks refusing AUTO / MDI before anything starts. Default
            # False so a UI written against these stays usable against a
            # controller that predates them.
            "auto_inhibit": ("auto_inhibit", False),
            "mdi_inhibit": ("mdi_inhibit", False),
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

        # relative_position — the commanded position in the coordinate system
        # G-code is currently working in. See the property below for why this
        # lives in one place.
        if name == "relative_position":
            return _relative_position(
                _pos_to_tuple(data.get("position", {})),
                _pos_to_tuple(data.get("g5x_offset", {})),
                _pos_to_tuple(data.get("g92_offset", {})),
                _pos_to_tuple(data.get("tool_offset", {})),
                data.get("rotation_xy", 0.0))

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

        # spindles (count) — classic linuxcnc.stat().spindles
        if name == "spindles":
            return len(data.get("spindle") or [])

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

        # tool_table — not part of the stat snapshot (2.9 read it straight out
        # of the tooldata mmap on every access); fetched from the tool table
        # service instead.
        if name == "tool_table":
            return self._tool_table()

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

    def _tool_table(self):
        """Fetch the tool table as a list indexed by SLOT (cached).

        This reproduces the C extension's Stat_tool_table exactly: a sequence
        of 14-field entries subscripted by tooldata index, 0 through the
        highest occupied slot inclusive, where index 0 is the spindle and
        `stat.pocket_prepped` is an index into it. Unoccupied slots in the
        middle are present and empty (toolno -1), because the list is an
        array, not a set of the tools that happen to exist: axis.py reads
        tool_table[0], touchy and gscreen read tool_table[pocket_prepped].

        Slot 0 always exists server-side, so the list is never empty and
        tool_table[0] never raises — a config with no tools at all used to
        return [] here and crash AXIS on startup (issue #272).

        Cached: re-fetches when tool_in_spindle OR the applied tool offset
        changes. The offset in the key catches tool touch-off (G10 L10/L11 +
        G43), which edits the current tool's geometry without a tool change —
        keyed on tool_in_spindle alone the display stayed stale until the
        next M6 (finding GP-8; classic read the live mmap on every access).
        """
        data = self._data
        current_key = (data.get("tool_in_spindle", 0),
                       _pos_to_tuple(data.get("tool_offset", {})))
        if (hasattr(self, '_tool_table_cache') and
                self._tool_table_key == current_key):
            return self._tool_table_cache

        # A failed fetch is not cached (the table must not freeze on a transient
        # blip), so without a floor on the retry interval every read reaches the
        # server: axis.py reads tool_table[0] once per display cycle, which
        # turned one wrong instance name into ten failed requests a second.
        if time.monotonic() < self._tool_table_retry_after:
            return getattr(self, '_tool_table_cache', [_empty_tool_entry()])

        try:
            from gmi.tools import ToolSlots
            if self._tooltable_instance is None:
                import gmi
                self._tooltable_instance = gmi.tooltable_instance()
            slots = ToolSlots(self._tooltable_instance).list()
        except Exception:
            # A failed fetch must still hand back a subscriptable table: an
            # exception here is a display glitch, an IndexError in the caller
            # is a dead GUI.
            self._tool_table_retry_after = time.monotonic() + _TOOL_TABLE_RETRY_S
            return getattr(self, '_tool_table_cache', [_empty_tool_entry()])

        by_idx = {}
        for slot in slots:
            try:
                idx = int(slot.get("idx", -1))
            except (TypeError, ValueError):
                continue
            if idx >= 0:
                by_idx[idx] = slot
        # Slot 0 exists server-side; falling back to 0 keeps the list
        # subscriptable even if a future store ever answers without it.
        last = max(by_idx) if by_idx else 0
        result = [_ToolEntry.from_dict(by_idx[i]) if i in by_idx else _empty_tool_entry()
                  for i in range(last + 1)]
        self._tool_table_cache = result
        self._tool_table_key = current_key
        return result

    def invalidate_tool_table(self):
        """Force re-fetch of tool table on next access (after reload_tool_table)."""
        if hasattr(self, '_tool_table_cache'):
            del self._tool_table_cache

    def stop(self):
        """Release the keep-alive poll connection (kept for API compat with
        the earlier WebSocket-backed Stat; there is no background thread)."""
        with self._poll_lock:
            if self._poll_conn is not None:
                self._poll_conn.close()
                self._poll_conn = None


def _relative_position(position, g5x, g92, tool, rotation_xy):
    """The DRO's "relative" position: where the machine is in the coordinate
    system G-code is currently working in.

    This is the one calculation every UI re-implements, and in this tree no two
    copies agreed — AXIS (rs274/glcanon.py:1699) and touchy
    (emc_interface.py:328) do it in full, 2.9's emcsh `emc_rel_act_pos` dropped
    the rotation, and pyngcgui dropped both the rotation and G92 (while citing
    touchy as its source). Ported here from glcanon so there is one answer.

    **The order matters**: G5x and the tool offset come off first, then XY is
    rotated by -rotation_xy, and only then does G92 come off. Subtracting G92
    before the rotation gives a wrong number on any machine that uses a rotated
    coordinate system (G10 L2 P<n> R<angle>) with a G92 active.

    Millimetres in, millimetres out — `Stat.machine_units()` converts it like
    any other position tuple (rotation is uniform in XY, so scaling before or
    after is the same value).
    """
    out = [p - g - t for p, g, t in zip(position, g5x, tool)]
    if rotation_xy:
        t = math.radians(-rotation_xy)
        x, y = out[0], out[1]
        out[0] = x * math.cos(t) - y * math.sin(t)
        out[1] = x * math.sin(t) + y * math.cos(t)
    return tuple(o - g for o, g in zip(out, g92))


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

    stratuMAK is mm-everywhere: the base Stat (like the rest of the gmi API) reports
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
        # relative_position is a position 9-tuple with the same axis layout, so
        # it converts the same way. Rotation is uniform in XY (both scaled by
        # `lin`), so computing it in mm and scaling here is the same value.
        "relative_position",
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

        if name == "tool_table":
            # REST tool entries are mm (server contract); classic tool_table
            # was machine units. Convert the linear fields — XYZ/UVW offsets
            # (indices 1-3, 7-9) and diameter (10) by lin, ABC offsets (4-6)
            # by ang; toolno/front/backangle/orientation pass through.
            out = []
            for e in value:
                f = list(e._fields)
                for i in (1, 2, 3, 7, 8, 9, 10):
                    f[i] = f[i] * lin
                for i in (4, 5, 6):
                    f[i] = f[i] * ang
                out.append(_ToolEntry(*f))
            return out

        return value
