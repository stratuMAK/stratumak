"""GMI client package — generated REST and WebSocket clients for LinuxCNC."""

import os

# Classic parity: the linuxcnc module exposed all constants at module level
# (linuxcnc.MODE_MDI). Re-export them so gmi.MODE_MDI works the same
# (constants.py holds only UPPER_CASE names — no collision with this module).
from gmi.constants import *  # noqa: F401,F403

# Raised by info() when the server cannot describe the machine. Re-exported so
# callers catch gmi.InfoUnavailable without reaching into the submodule.
from gmi.taskinfo import InfoUnavailable  # noqa: F401

_DEFAULT_REST_URL = "http://127.0.0.1:5080"
_ENV_VAR = "GMC_REST_URL"
_INSTANCE_ENV_VAR = "GMC_INSTANCE"
_DEFAULT_INSTANCE = "milltask"

# Version string (matches linuxcnc.version).
version = os.environ.get("LINUXCNCVERSION", "unknown")


def instance() -> str:
    """Return the target instance name (from GMC_INSTANCE or default 'milltask')."""
    return os.environ.get(_INSTANCE_ENV_VAR, _DEFAULT_INSTANCE)


def resolve_instance(name=None) -> str:
    """The instance a client should address: the explicit name if one is given,
    otherwise the session default from GMC_INSTANCE (instance()).

    Every gmi client class takes ``instance=None`` and passes it through here, so
    the ONE rule — explicit wins, unset follows the environment — holds whether a
    client is built via the gmi.Stat()/gmi.Command() factories or constructed
    directly (as AXIS does with a Command subclass). A class that hardcoded
    "milltask" as its default instead posted to a nonexistent instance on a
    multi-instance server; this is that trap closed at the source.
    """
    return name or instance()


def info():
    """Return the machine description from milltask's GET /info (cached).

    This is how a client learns the names of the other modules it talks to.
    Raises gmi.InfoUnavailable if the server cannot answer — see gmi.taskinfo for
    why that is not retried.
    """
    from gmi import taskinfo
    return taskinfo.fetch(rest_url(), instance())


def reset_info():
    """Drop the cached machine description (tests; reconnect to a new server)."""
    from gmi import taskinfo
    taskinfo.reset()


def _peer(env_var: str, peer_attr: str, legacy_default: str) -> str:
    """Resolve a peer instance name: environment override, then /info, then the
    peer module's own default instance name.

    The last step is what keeps single-instance configs working without naming
    anything: `load ngcpreview` with no arguments registers as "ngcpreview", so
    that IS the instance. What is deliberately gone is the old middle step of
    deriving a name from this one (f"{instance}-preview") — a guess that happens
    to be right in the configs it was written against and wrong everywhere else.
    A multi-instance config must name its peers on the milltask load line, which
    milltask then verifies at startup.
    """
    if env_var in os.environ:
        return os.environ[env_var]
    name = getattr(info().peers, peer_attr, "")
    return name if name else legacy_default


def preview_instance() -> str:
    """Return the ngcpreview instance name serving this task."""
    return _peer("GMC_PREVIEW_INSTANCE", "preview", "ngcpreview")


def mtc_instance() -> str:
    """Return the manual-tool-change instance name for this task."""
    return _peer("GMC_MTC_INSTANCE", "manualtoolchange", "manualtoolchange")


def pyvcp_instance() -> str:
    """Return the pyvcp panel instance for this task, or "" if none is loaded.

    Unlike preview/mtc this has NO default-name fallback. A pyvcp panel is
    optional and its presence is decided by the server: a client shows one only
    when /info reports a peer. Falling back to a "pyvcp" default would be the
    very guess that fabricates a request to a panel that isn't there — the INI
    key [DISPLAY]PYVCP used to gate this, and a panel it named but HAL never
    loaded 404'd. An empty answer here means "no panel", and the caller must
    treat it as the gate.
    """
    return _peer("GMC_PYVCP_INSTANCE", "pyvcp", "")


def tooltable_instance() -> str:
    """Return the raw tool-table slot store instance backing stat.tool_table.

    Unlike the peers above this has no fallback: milltask always reports a
    resolved name (its own default included), so an empty answer here means the
    server did not answer at all, and guessing "tooltable" is what produced the
    404 storm in the first place.
    """
    if "GMC_TOOLTABLE_INSTANCE" in os.environ:
        return os.environ["GMC_TOOLTABLE_INSTANCE"]
    return info().peers.tooltable


def has_api(api_name: str, instance: str | None = None,
            refresh: bool = False) -> bool:
    """Report whether the server serves `api_name` — optionally under exactly
    `instance`.

    This is the gate for an optional module's UI. Naming the instance matters
    when the thing being offered addresses one by name: the classicladder web
    app talks to the instance called "classicladder", so a differently-named
    one is a module that is loaded and an app that cannot reach it.

    False when the server cannot be asked at all — see gmi.registry for why
    that is an answer rather than an error.

    refresh=True bypasses the per-process registry cache and asks the server
    again — for callers gating on a module that can be loaded at runtime.
    """
    from gmi import registry
    for name, inst in registry.entries(rest_url(), refresh=refresh):
        if name == api_name and (instance is None or inst == instance):
            return True
    return False


def reset_registry() -> None:
    """Drop the cached API registry (tests; reconnect to a new server)."""
    from gmi import registry
    registry.reset()


def rest_url() -> str:
    """Return the REST base URL (from GMC_REST_URL or default)."""
    return os.environ.get(_ENV_VAR, _DEFAULT_REST_URL).rstrip("/")


def ws_url() -> str:
    """Return the WebSocket watch URL derived from the REST URL."""
    base = rest_url()
    base = base.replace("https://", "wss://").replace("http://", "ws://")
    return base + "/api/v1/watch"


# Re-export wrapper classes for convenience.
# These are lazy-imported to avoid pulling in websockets at module load
# (not all callers need stat/error channels).
def Stat():
    """Create a gmi.Stat instance (drop-in for linuxcnc.stat())."""
    from gmi.stat import Stat as _Stat
    return _Stat(instance=instance())


def Command():
    """Create a gmi.Command instance (drop-in for linuxcnc.command())."""
    from gmi.command import Command as _Command
    return _Command(instance=instance())


def ErrorChannel():
    """Create a gmi.ErrorChannel instance (drop-in for linuxcnc.error_channel())."""
    from gmi.error import ErrorChannel as _ErrorChannel
    return _ErrorChannel(instance=instance())


def MessageList(on_update=None):
    """Create a gmi.MessageList instance for the server-side message list."""
    from gmi.messages import MessageList as _MessageList
    return _MessageList(instance=instance(), on_update=on_update)


def positionlogger(stat_unused, c0, c1, c2, c3, c4, c5, geometry, is_xyuv=0):
    """Create a gmi.PositionLogger (drop-in for linuxcnc.positionlogger())."""
    from gmi.positionlogger import PositionLogger
    return PositionLogger(stat_unused, c0, c1, c2, c3, c4, c5, geometry, is_xyuv)


def ToolTable():
    """Create a gmi.ToolTable instance for REST tool table access."""
    from gmi.tools import ToolTable as _ToolTable
    return _ToolTable(instance=instance())


def ToolSlots():
    """Create a gmi.ToolSlots client for the raw slot store behind this task.

    The store's instance name is milltask's tooltable_instance, resolved from
    /info by tooltable_instance() — the same lookup every other peer gets, so
    a UI never has to know or guess the name.
    """
    from gmi.tools import ToolSlots as _ToolSlots
    return _ToolSlots(instance=tooltable_instance())


def component_exists(name: str) -> bool:
    """Check if a HAL component exists via the halcmd REST API.

    Returns False on ANY failure, including an unreachable server — this is
    an existence probe, not a health check (review finding GP-29).
    """
    import json
    import urllib.parse
    import urllib.request
    url = (rest_url() + "/api/v1/halcmd/components?pattern="
           + urllib.parse.quote(name, safe=""))
    try:
        with urllib.request.urlopen(url, timeout=2) as resp:
            data = json.loads(resp.read())
            return len(data) > 0
    except Exception:
        return False


def pin_has_writer(name: str) -> bool:
    """Check if a HAL pin's signal has any writers via the halcmd REST API.

    Returns False on ANY failure, including an unreachable server (GP-29).
    """
    import json
    import urllib.parse
    import urllib.request
    url = (rest_url() + "/api/v1/halcmd/pins?pattern="
           + urllib.parse.quote(name, safe=""))
    try:
        with urllib.request.urlopen(url, timeout=2) as resp:
            data = json.loads(resp.read())
            if data:
                return data[0].get("has_writer", False)
            return False
    except Exception:
        return False


class IniFile:
    """Drop-in replacement for linuxcnc.ini() that fetches values via REST.

    Matches the linuxcnc.ini API:
      - find(section, key) -> str | None
      - findall(section, key) -> list[str]

    When GMC_INSTANCE is set (multi-instance), namespace-prefixed sections
    (e.g. [mill2:KINS]) are resolved automatically via the server.
    """

    def __init__(self):
        from gmi.ini_client import IniClient, IniQueryItem
        self._client = IniClient(rest_url())
        self._cache = {}  # (section, key) -> str or None (find)
        self._cache_all = {}  # (section, key) -> list[str] (findall)
        # Use namespace only when GMC_INSTANCE is explicitly set.
        ns = os.environ.get(_INSTANCE_ENV_VAR)
        self._namespace = ns if ns else None

    def find(self, section, key, num=None):
        """Return the first value for section/key, or None if not found.

        ``num`` selects the num'th occurrence (1-based), matching classic
        linuxcnc.ini().find(section, option, num).
        """
        if num is not None and num != 1:
            vals = self.findall(section, key)
            return vals[num - 1] if 0 < num <= len(vals) else None
        cache_key = (section, key)
        if cache_key in self._cache:
            return self._cache[cache_key]
        from gmi.ini_client import IniQueryItem
        results = self._client.query([IniQueryItem(section=section, key=key, namespace=self._namespace).to_dict()])
        if results and len(results) == 1:
            val = results[0].value
            self._cache[cache_key] = val
            return val
        self._cache[cache_key] = None
        return None

    def findall(self, section, key):
        """Return all values for section/key as a list."""
        cache_key = (section, key)
        if cache_key in self._cache_all:
            return self._cache_all[cache_key]
        from gmi.ini_client import IniQueryItem
        results = self._client.query([IniQueryItem(section=section, key=key, all=True, namespace=self._namespace).to_dict()])
        if results and len(results) == 1:
            vals = results[0].values or []
            self._cache_all[cache_key] = vals
            return vals
        self._cache_all[cache_key] = []
        return []


def fetch_parameter_file():
    """Fetch the RS274NGC parameter file content from the REST service."""
    from gmi.ini_client import IniClient
    ns = os.environ.get(_INSTANCE_ENV_VAR)
    client = IniClient(rest_url())
    return client.get_parameter_file(namespace=ns if ns else None)
