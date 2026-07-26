"""gmi.taskinfo — the machine description served by milltask's GET /info.

Usually reached through gmi.info(), gmi.InfoUnavailable and gmi.reset_info();
the module is named for the TaskInfo it carries rather than for the accessor, so
that importing it cannot shadow the function.

This exists so a client never has to GUESS the name of another module. Deriving
one by convention (``f"{instance}-preview"``) or hardcoding a default
(``"tooltable"``) works in a single-instance config and breaks in every
multi-instance one — a multi-instance config answered every tool table read with
a 404, ten times a second, for exactly that reason.

It also carries the capability flags milltask computes from live HAL wiring, so
a UI stops probing pin names it cannot correctly qualify: the real pin on a
multi-instance machine is ``pnp.mot.spindle.0.forward``, and only milltask knows
the ``pnp.mot`` part.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request


class TaskPeers:
    """REST instance names of the modules this client talks to directly.

    An empty string means the feature is not configured. ``tooltable`` is always
    set: milltask resolves it (default included) before reporting it.
    """

    __slots__ = ("tooltable", "preview", "manualtoolchange", "pyvcp")

    def __init__(self, d: dict):
        self.tooltable = d.get("tooltable", "")
        self.preview = d.get("preview", "")
        self.manualtoolchange = d.get("manualtoolchange", "")
        self.pyvcp = d.get("pyvcp", "")


class TaskCaps:
    """What the operator UI should offer, per what is actually wired in HAL."""

    __slots__ = ("spindle_forward", "spindle_reverse", "spindle_on",
                 "spindle_speed", "spindle_brake", "limit_switch_override",
                 "coolant_mist", "coolant_flood")

    def __init__(self, d: dict):
        self.spindle_forward = bool(d.get("spindle_forward", False))
        self.spindle_reverse = bool(d.get("spindle_reverse", False))
        self.spindle_on = bool(d.get("spindle_on", False))
        self.spindle_speed = bool(d.get("spindle_speed", False))
        self.spindle_brake = bool(d.get("spindle_brake", False))
        self.limit_switch_override = bool(d.get("limit_switch_override", False))
        self.coolant_mist = bool(d.get("coolant_mist", False))
        self.coolant_flood = bool(d.get("coolant_flood", False))


class TaskInfo:
    __slots__ = ("peers", "caps")

    def __init__(self, d: dict):
        self.peers = TaskPeers(d.get("peers") or {})
        self.caps = TaskCaps(d.get("caps") or {})


class InfoUnavailable(Exception):
    """GET /info failed.

    Only a DEFINITIVE answer is latched and re-raised on every later call: an
    HTTP 404 from a live server (no such instance, or a server too old to
    serve /info — neither heals on a retry, and retrying would reproduce the
    10 Hz 404 storm this endpoint was added to remove).

    A transport failure or any other HTTP status is transient — connection
    refused while the server is still binding, the timeout, a 503 while a
    runtime load settles — and is NOT latched: a client is entitled to
    construct its Stat before the server answers, and its next poll must be
    allowed to succeed. Latching those froze Stat.tool_table empty for the
    whole session after one startup blip.
    """


# Cached across the process: the answer is fixed for the life of the task
# (peer names come from module parameters, which do not change while it runs).
# Only DEFINITIVE failures (404) are cached — see InfoUnavailable.
_cache: dict[str, TaskInfo] = {}
_failure: dict[str, InfoUnavailable] = {}


def fetch(rest_url: str, instance: str) -> TaskInfo:
    """Return the machine description for ``instance``, fetching it once."""
    if instance in _cache:
        return _cache[instance]
    if instance in _failure:
        raise _failure[instance]

    url = f"{rest_url}/api/v1/{instance}/info"
    try:
        with urllib.request.urlopen(url, timeout=10) as resp:
            info = TaskInfo(json.loads(resp.read()))
    except urllib.error.HTTPError as e:
        detail = f"HTTP {e.code}"
        if e.code == 404:
            detail += (" — no such instance, or a server too old to serve"
                       " /info (check GMC_INSTANCE)")
            _failure[instance] = InfoUnavailable(f"GET {url} failed: {detail}")
            raise _failure[instance] from e
        # Any other status (a 503 while a runtime load settles, a proxy 502)
        # is transient: raise, do not latch.
        raise InfoUnavailable(f"GET {url} failed: {detail}") from e
    except Exception as e:
        # Transport failure (connection refused at startup, the timeout).
        # Transient by nature: raise, do not latch.
        raise InfoUnavailable(f"GET {url} failed: {e}") from e

    _cache[instance] = info
    return info


def reset():
    """Drop the cache. For tests, and for a client that reconnects to a
    restarted server."""
    _cache.clear()
    _failure.clear()
