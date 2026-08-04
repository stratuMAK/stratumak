"""PyVCP WebSocket client — widget-centric, server-authoritative state model.

Connects to stmakd via WebSocket, receives widget state updates,
and provides a clean event-driven interface for pyvcp widgets.

Architecture:
  - Server owns all HAL pins, constraints, and derived pins.
  - On connect, the first watch_state update delivers the full state snapshot
    as a map of widget_id → {value, state, index, disabled, reset}.
  - Subsequent updates are delta-encoded: only changed widgets are sent.
  - The client merges deltas into its local state and fires callbacks only
    for widgets whose state actually changed.
  - The client sends widget_event commands (press/release/increment/set/toggle/
    select) — never raw pin values.
  - Server clamps, quantizes, derives, and pushes truth back.
"""

from __future__ import annotations

import asyncio
import json
import math
import queue
import threading
import traceback
import urllib.request
from typing import Any, Callable, Optional

import websockets

import gmi


# Event type constants (matching pyvcp.gmi EventType enum).
EV_PRESS = 1
EV_RELEASE = 2
EV_TOGGLE = 3
EV_SELECT = 4
EV_SET = 5
EV_INCREMENT = 6


def fetch_panel_info(panel_name: str) -> dict:
    """Fetch panel info (XML + widget defs) from the REST endpoint."""
    url = gmi.rest_url() + "/api/v1/pyvcp/panel/" + panel_name
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read())


class PyVCPClient:
    """WebSocket client for a pyvcp panel — widget-centric protocol.

    Provides:
      - get(widget_id) → current WidgetState dict
      - send_event(widget_id, event_type, **kwargs) → send to server
      - on_change(widget_id, callback) → register for server state changes
    """

    def __init__(self, name: str, widgets: list[dict], tk_root=None):
        self._name = name
        # Widget definitions keyed by ID.
        self._widget_defs = {w["id"]: w for w in widgets}
        # Current state: widget_id → {value, state, index, disabled, reset}
        self._state: dict[str, dict] = {}
        self._callbacks: dict[str, list[Callable]] = {}
        self._lock = threading.Lock()
        self._thread: Optional[threading.Thread] = None
        self._loop: Optional[asyncio.AbstractEventLoop] = None
        self._started = threading.Event()
        self._connected = threading.Event()
        self._stopping = False
        self._ws = None
        self._tk_root = tk_root
        self._tk_queue: queue.Queue = queue.Queue()
        if tk_root:
            self._poll_tk_queue()

    @property
    def name(self) -> str:
        return self._name

    def widget_def(self, widget_id: str) -> Optional[dict]:
        """Return the widget definition (type, min, max, resolution, etc.)."""
        return self._widget_defs.get(widget_id)

    def get(self, widget_id: str) -> dict:
        """Read the current server-reported state of a widget."""
        with self._lock:
            return self._state.get(widget_id, {})

    def get_value(self, widget_id: str) -> float:
        """Read the current numeric value of a widget (NaN if not applicable)."""
        s = self.get(widget_id)
        v = s.get("value")
        if v is None:
            return float("nan")
        return float(v)

    def get_state(self, widget_id: str) -> bool:
        """Read the current bool state of a widget."""
        return bool(self.get(widget_id).get("state", False))

    def get_index(self, widget_id: str) -> int:
        """Read the current index of a widget (-1 if not applicable)."""
        return int(self.get(widget_id).get("index", -1))

    def get_disabled(self, widget_id: str) -> bool:
        """Read the current disabled state of a widget."""
        return bool(self.get(widget_id).get("disabled", False))

    def get_reset(self, widget_id: str) -> bool:
        """Read the current reset state (timer only)."""
        return bool(self.get(widget_id).get("reset", False))

    def send_event(self, widget_id: str, event_type: int,
                   value: float = float("nan"), increment: int = 0, index: int = -1):
        """Send a widget event to the server.

        Only call this from user interaction handlers. Will silently
        do nothing if the connection isn't established yet.
        """
        if not self._connected.is_set():
            return
        ev = {
            "widget": widget_id,
            "event": event_type,
            "value": value if not math.isnan(value) else 0.0,
            "increment": increment,
            "index": index,
        }
        if self._loop:
            self._loop.call_soon_threadsafe(
                self._loop.create_task, self._send_widget_event(ev)
            )

    def on_change(self, widget_id: str, callback: Callable[[dict], None]):
        """Register a callback for when a widget's state changes from the server.

        Callback receives the full widget state dict: {value, state, index, disabled, reset}.
        Callbacks are dispatched on the Tk main thread (if tk_root was given).
        """
        with self._lock:
            self._callbacks.setdefault(widget_id, []).append(callback)

    def _poll_tk_queue(self):
        """Drain the callback queue on the Tk main thread. Runs every 20ms.

        A raising callback must never stop the drain/reschedule — otherwise
        the whole UI would freeze — so each callback is isolated and the
        reschedule lives in a finally.
        """
        try:
            while True:
                try:
                    cb, val = self._tk_queue.get_nowait()
                except queue.Empty:
                    break
                try:
                    cb(val)
                except Exception:
                    traceback.print_exc()
        finally:
            self._tk_root.after(20, self._poll_tk_queue)

    def start(self):
        """Start the WebSocket connection thread."""
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        self._started.wait(timeout=5)

    def stop(self):
        """Stop the WebSocket connection and the reconnect loop."""
        if self._loop:
            try:
                self._loop.call_soon_threadsafe(self._request_stop)
            except RuntimeError:
                # Loop already closed.
                pass
        if self._thread:
            self._thread.join(timeout=2)

    def _request_stop(self):
        """Runs on the event-loop thread: cancel the supervisor and exit."""
        self._stopping = True
        task = getattr(self, "_supervisor_task", None)
        if task is not None:
            task.cancel()

    def wait_connected(self, timeout: float = 5.0) -> bool:
        """Wait until the first full state update has been received."""
        return self._connected.wait(timeout=timeout)

    # --- Internal ---

    # Delay between reconnect attempts (seconds).
    RECONNECT_DELAY = 1.0

    def _run(self):
        self._loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self._loop)
        self._supervisor_task = self._loop.create_task(self._supervise())
        self._started.set()
        try:
            self._loop.run_until_complete(self._supervisor_task)
        except asyncio.CancelledError:
            pass
        except Exception as e:
            import sys
            print(f"pyvcp_client: connection loop failed: {e}", file=sys.stderr)
        finally:
            try:
                self._loop.run_until_complete(self._close())
            except Exception:
                pass
            self._loop.close()

    async def _supervise(self):
        """Connect, then reconnect with backoff until stop() is called.

        On every (re)connect the server sends a fresh full snapshot for the new
        subscription, so clearing _connected here makes _process_update treat the
        next update as a first update and re-sync all widgets.
        """
        import sys
        while not self._stopping:
            try:
                await self._connect()
                # Runs until the socket closes (server drop) or is cancelled.
                await self._recv_loop()
            except asyncio.CancelledError:
                break
            except Exception as e:
                print(f"pyvcp_client: connection error: {e}", file=sys.stderr)
            finally:
                # Mark down so send_event stops feeding a dead socket.
                self._connected.clear()
                self._ws = None
            if self._stopping:
                break
            try:
                await asyncio.sleep(self.RECONNECT_DELAY)
            except asyncio.CancelledError:
                break

    async def _connect(self):
        url = gmi.ws_url()
        self._ws = await websockets.connect(url)
        msg = {
            "action": "subscribe",
            "api": "pyvcp",
            "instance": self._name,
            "func": "watch_state",
            "rate_ms": 100,
        }
        await self._ws.send(json.dumps(msg))

    async def _close(self):
        if self._ws is not None:
            try:
                await self._ws.close()
            except Exception:
                pass
            self._ws = None

    async def _recv_loop(self):
        # CancelledError is allowed to propagate so a stop() cleanly ends the
        # supervisor; only a normal socket close falls through to reconnect.
        try:
            async for raw in self._ws:
                msg = json.loads(raw)
                if msg.get("type") == "update" and msg.get("func") == "watch_state":
                    data = msg.get("data")
                    if isinstance(data, dict):
                        self._process_update(data)
        except websockets.ConnectionClosed:
            pass

    def _process_update(self, widget_states: dict[str, dict]):
        """Process a server state update (full or delta). Fire callbacks for changed widgets."""
        first_update = not self._connected.is_set()
        to_notify: list[tuple[str, dict, list[Callable]]] = []

        with self._lock:
            for wid, new_state in widget_states.items():
                old_state = self._state.get(wid)
                self._state[wid] = new_state
                # On first update, notify all widgets. After that, only changed.
                if first_update or old_state != new_state:
                    cbs = self._callbacks.get(wid)
                    if cbs:
                        to_notify.append((wid, new_state, list(cbs)))

            if first_update:
                self._connected.set()

        # Dispatch callbacks. A raising callback must not kill the recv loop.
        for wid, state, cbs in to_notify:
            for cb in cbs:
                if self._tk_root:
                    self._tk_queue.put((cb, state))
                else:
                    try:
                        cb(state)
                    except Exception:
                        traceback.print_exc()

    async def _send_widget_event(self, event: dict):
        if not hasattr(self, "_ws") or not self._ws:
            return
        msg = {
            "action": "call",
            "api": "pyvcp",
            "instance": self._name,
            "func": "widget_event",
            "id": 0,
            "args": {"event": event},
        }
        try:
            await self._ws.send(json.dumps(msg))
        except websockets.ConnectionClosed:
            pass
