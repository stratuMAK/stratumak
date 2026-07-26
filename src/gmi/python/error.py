"""gmi.error — Drop-in replacement for linuxcnc.error_channel().

Subscribes to the emcerror WebSocket watch channel via the shared resilient
WatchClient (connect retry, automatic reconnect — a server restart must not
silently end error delivery for the session; review finding GP-2/GP-3).
Queues incoming error/info messages. poll() returns (kind, text) or None,
matching the linuxcnc.error_channel() API.

Usage:
    e = gmi.ErrorChannel()
    msg = e.poll()  # returns (kind, text) or None
    if msg:
        kind, text = msg
"""

from __future__ import annotations

import collections
import threading
from typing import Optional, Tuple

from gmi import resolve_instance
from gmi._watch import WatchClient


class ErrorChannel:
    """Drop-in replacement for linuxcnc.error_channel().

    Connects to the emcerror watch channel. Messages are queued by the
    background thread and returned one at a time by poll().
    """

    def __init__(self, instance: str = None):
        instance = resolve_instance(instance)
        self._queue = collections.deque()
        self._lock = threading.Lock()
        self._client = WatchClient(
            "ErrorChannel",
            {"api": "emcerror", "instance": instance,
             "func": "get_errors", "rate_ms": 200},
            self._handle_update)
        self._client.start(wait=5.0)

    def _handle_update(self, func, data):
        if func != "get_errors" or not data:
            return
        with self._lock:
            for err in data:
                self._queue.append((err.get("kind", 0), err.get("text", "")))

    def poll(self) -> Optional[Tuple[int, str]]:
        """Return the next queued message as (kind, text), or None.

        Matches linuxcnc.error_channel().poll() return type.
        """
        with self._lock:
            if self._queue:
                return self._queue.popleft()
        return None

    def stop(self):
        """Stop the background WebSocket thread."""
        self._client.stop()
