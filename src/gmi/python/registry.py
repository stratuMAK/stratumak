"""gmi.registry — which APIs the server is actually serving.

Usually reached through gmi.has_api(). It answers the one question a UI has
about an optional module: is it there to talk to? A menu entry that launches a
window onto a module nobody loaded is worse than no entry — it looks like the
feature is broken rather than absent.

Unlike gmi.info(), which a client cannot work without, this is advisory: the
answer to "is X loaded" when the server cannot be reached is "no", not an
exception. Raising here would turn an optional feature into a startup failure
for every caller that asked about one, which is the opposite of the point.
"""

from __future__ import annotations

import http.client
import json
import urllib.error
import urllib.request

# One fetch per process, keyed by base URL. Modules are usually configured
# before the GUI starts, so the callers are start-up gates and re-asking would
# only add latency — but modules CAN be loaded at runtime, so a caller that
# cares (a menu about to be shown) passes refresh=True to ask again.
_cache: dict[str, list[tuple[str, str]]] = {}


def entries(rest_url: str, timeout: float = 2.0,
            refresh: bool = False) -> list[tuple[str, str]]:
    """Return the (api_name, instance) pairs the server serves, or [] if it
    cannot be asked. refresh=True drops the cached answer and asks again."""
    if refresh:
        _cache.pop(rest_url, None)
    if rest_url in _cache:
        return _cache[rest_url]

    url = f"{rest_url}/api/v1/_registry"
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            data = json.loads(resp.read())
    except (urllib.error.URLError, http.client.HTTPException, OSError,
            ValueError):
        # No server, no route, something that does not speak HTTP (a port
        # squatter's BadStatusLine, a dying server's IncompleteRead), or
        # something that is not JSON. All of them mean
        # the same thing to a caller asking whether a module is loaded, and
        # none of them is worth propagating. Not cached: a UI that started
        # before the server may ask again and get a real answer.
        return []

    if not isinstance(data, list):
        return []
    found = [
        (d.get("api_name", ""), d.get("instance", ""))
        for d in data
        if isinstance(d, dict)
    ]
    _cache[rest_url] = found
    return found


def reset() -> None:
    """Drop the cached registry (tests; reconnect to a new server)."""
    _cache.clear()
