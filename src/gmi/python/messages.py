"""gmi.messages — WebSocket client for the server-side message list.

Subscribes to the messages/get_list watch channel via the shared resilient
WatchClient (connect retry, automatic reconnect + resubscribe; a raising
on_update callback or a dropped connection must not silently disable the
operator-notification system for the session — review findings GP-2/GP-3).
Maintains a local copy of the current message list. Provides methods to
acknowledge messages; call failures are logged, never silently discarded.

Usage:
    ml = gmi.MessageList()
    msgs = ml.get_list()  # returns list of {"id": int, "kind": int, "text": str}
    ml.ack_message(msg_id)
    ml.ack_all()
"""

from __future__ import annotations

import threading
from typing import Callable, List, Optional

from gmi import resolve_instance
from gmi._watch import WatchClient

# Error kind constants (matching emcerror.ErrorKind).
NML_ERROR = 1
NML_TEXT = 2
NML_DISPLAY = 3
OPERATOR_ERROR = 11
OPERATOR_TEXT = 12
OPERATOR_DISPLAY = 13


class MessageList:
    """WebSocket client for the server-side message list.

    Subscribes to messages/get_list. The on_update callback is called
    whenever the list changes (new message added or message acknowledged).
    """

    def __init__(self, instance: str = None, on_update: Optional[Callable] = None):
        instance = resolve_instance(instance)
        self._instance = instance
        self._on_update = on_update
        self._messages: List[dict] = []
        self._lock = threading.Lock()
        self._client = WatchClient(
            "MessageList",
            {"api": "messages", "instance": instance,
             "func": "get_list", "rate_ms": 100},
            self._handle_update)
        self._client.start(wait=5.0)

    def _handle_update(self, func, data):
        if func != "get_list":
            return
        data = data or []
        with self._lock:
            self._messages = data
        if self._on_update:
            # WatchClient guards this call — a raising consumer callback is
            # logged and the channel keeps running.
            self._on_update(data)

    def get_list(self) -> List[dict]:
        """Return the current message list snapshot."""
        with self._lock:
            return list(self._messages)

    def ack_message(self, msg_id: int):
        """Acknowledge (remove) a single message by ID."""
        self._call("ack_message", {"id": msg_id})

    def ack_all(self):
        """Acknowledge all messages."""
        self._call("ack_all", {})

    def ack_error(self):
        """Acknowledge all error messages."""
        self._call("ack_error", {})

    def ack_text(self):
        """Acknowledge all text messages."""
        self._call("ack_text", {})

    def ack_display(self):
        """Acknowledge all display messages."""
        self._call("ack_display", {})

    def publish(self, kind: int, text: str):
        """Publish a new message to the server-side list."""
        self._call("publish", {"kind": kind, "text": text})

    def _call(self, func_name: str, args: dict):
        """Fire-and-forget command call (failures logged by WatchClient)."""
        self._client.call("messages", self._instance, func_name, args)

    def stop(self):
        """Stop the background WebSocket thread."""
        self._client.stop()
