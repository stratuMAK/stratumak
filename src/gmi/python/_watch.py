"""gmi._watch — shared resilient WebSocket watch client.

One daemon thread per client running a private asyncio loop. Provides what the
per-module WS code used to lack (review findings GP-2/GP-3): connect retry at
startup (the server may not be accepting yet), automatic reconnect + resubscribe
with backoff after a drop, per-message decode/callback tolerance (a malformed
frame or a raising consumer callback must not kill the channel), fire-and-forget
calls whose failures are logged instead of vanishing in a discarded future, and
a stop() that actually tears down the task, socket and loop.

Used by ErrorChannel, MessageList and PositionLogger. Stat is REST-only and has
no WebSocket (frozen-snapshot poll semantics, finding GP-7).
"""

from __future__ import annotations

import asyncio
import json
import sys
import threading
from typing import Any, Callable, Optional

try:
    import websockets
except ImportError:
    websockets = None

from gmi import ws_url


class WatchClient:
    """Resilient subscribe-and-receive WebSocket client.

    ``subscribe`` is the subscription message without the ``action`` key; it is
    re-sent after every (re)connect. ``on_update(func, data)`` runs on the loop
    thread for every update frame; exceptions in it are logged, never fatal.
    ``on_connect(client)``, if set, is awaited after every successful
    subscribe — use it to re-issue server-side setup calls (e.g. start_logger).
    """

    _RECONNECT_MAX_DELAY = 5.0

    # Keepalive: detect a dead or half-open connection quickly. websockets'
    # defaults are ping_interval=ping_timeout=20 s, so a server that dies
    # ungracefully — or a close frame that never arrives — is not noticed for
    # 20–40 s, during which a controller HMI silently shows stale data and the
    # reconnect never starts. Ping every 5 s / fail after 5 s bounds detection
    # to ~10 s. open/close timeouts keep a stalled connect or teardown from
    # blocking the reconnect loop for the library's 10 s defaults.
    _PING_INTERVAL = 5.0
    _PING_TIMEOUT = 5.0
    _OPEN_TIMEOUT = 5.0
    _CLOSE_TIMEOUT = 2.0

    def __init__(self, name: str, subscribe: dict,
                 on_update: Callable[[Optional[str], Any], None]):
        self._name = name
        self._subscribe = dict(subscribe, action="subscribe")
        self._on_update = on_update
        self.on_connect = None  # optional async callable(client)
        self._loop = None
        self._ws = None
        self._connected = threading.Event()
        self._stopping = False
        self._announced_down = False
        self._next_id = 1
        self._thread = None

    def start(self, wait: float = 5.0):
        """Start the background thread; block up to ``wait`` for the first
        subscribe (returns regardless — the client keeps retrying behind the
        scenes, so a slow server degrades to late delivery, not a dead channel)."""
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        self._connected.wait(timeout=wait)

    @property
    def connected(self) -> bool:
        return self._connected.is_set() and self._ws is not None

    def _run(self):
        self._loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self._loop)
        try:
            self._loop.run_until_complete(self._main())
        except Exception as e:
            print(f"gmi.{self._name}: watch thread died: {e}", file=sys.stderr)
        finally:
            self._loop.close()

    async def _main(self):
        backoff = 0.25
        while not self._stopping:
            try:
                self._ws = await websockets.connect(
                    ws_url(),
                    ping_interval=self._PING_INTERVAL,
                    ping_timeout=self._PING_TIMEOUT,
                    open_timeout=self._OPEN_TIMEOUT,
                    close_timeout=self._CLOSE_TIMEOUT,
                )
                await self._ws.send(json.dumps(self._subscribe))
                if self.on_connect is not None:
                    await self.on_connect(self)
                if self._announced_down:
                    print(f"gmi.{self._name}: reconnected", file=sys.stderr)
                self._announced_down = False
                self._connected.set()
                backoff = 0.25
                await self._recv_loop()
                # recv loop only returns on server-closed socket; fall through
                # to reconnect.
                raise ConnectionError("connection closed by server")
            except asyncio.CancelledError:
                return
            except Exception as e:
                if not self._stopping and not self._announced_down:
                    print(f"gmi.{self._name}: connection lost/unavailable "
                          f"({e}); retrying", file=sys.stderr)
                    self._announced_down = True
            finally:
                ws, self._ws = self._ws, None
                if ws is not None:
                    try:
                        await ws.close()
                    except Exception:
                        pass
            if self._stopping:
                return
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, self._RECONNECT_MAX_DELAY)

    async def _recv_loop(self):
        async for raw in self._ws:
            try:
                msg = json.loads(raw)
            except ValueError:
                print(f"gmi.{self._name}: dropping undecodable frame",
                      file=sys.stderr)
                continue
            mtype = msg.get("type")
            if mtype == "update":
                try:
                    self._on_update(msg.get("func"), msg.get("data"))
                except Exception as e:
                    # A consumer callback must never kill the channel.
                    print(f"gmi.{self._name}: update handler error: {e}",
                          file=sys.stderr)
            elif mtype == "error":
                print(f"gmi.{self._name}: server error: {msg}", file=sys.stderr)
            # "result" frames of fire-and-forget calls carry no payload the
            # callers wait for; errors arrive as type "error" above.

    # ─── Fire-and-forget calls ───

    async def acall(self, api: str, instance: str, func: str, args=None):
        """Send a call frame on the current socket (loop thread only)."""
        if self._ws is None:
            raise ConnectionError("not connected")
        call_id = self._next_id
        self._next_id += 1
        msg = {"action": "call", "api": api, "instance": instance,
               "func": func, "id": call_id}
        if args:
            msg["args"] = args
        await self._ws.send(json.dumps(msg))

    def call(self, api: str, instance: str, func: str, args=None):
        """Queue a call from any thread. Failures are logged, not raised —
        but never silently discarded (finding GP-3/C-11)."""
        loop = self._loop
        if loop is None or not loop.is_running():
            print(f"gmi.{self._name}: dropped call {func} (channel not running)",
                  file=sys.stderr)
            return
        fut = asyncio.run_coroutine_threadsafe(
            self.acall(api, instance, func, args), loop)
        fut.add_done_callback(
            lambda f, func=func: self._log_call_result(func, f))

    def _log_call_result(self, func, fut):
        try:
            fut.result()
        except Exception as e:
            print(f"gmi.{self._name}: call {func} failed: {e}", file=sys.stderr)

    # ─── Teardown ───

    def stop(self, final_call: Optional[tuple] = None, timeout: float = 2.0):
        """Stop the client. ``final_call`` = (api, instance, func, args) is
        delivered best-effort before the socket closes (e.g. stop_logger)."""
        self._stopping = True
        loop = self._loop
        if loop is not None and loop.is_running():
            fut = asyncio.run_coroutine_threadsafe(
                self._shutdown(final_call), loop)
            try:
                fut.result(timeout)
            except Exception:
                pass
        if self._thread is not None:
            self._thread.join(timeout=2)

    async def _shutdown(self, final_call):
        if final_call is not None and self._ws is not None:
            try:
                await self.acall(*final_call)
            except Exception as e:
                print(f"gmi.{self._name}: final call failed: {e}",
                      file=sys.stderr)
        ws, self._ws = self._ws, None
        if ws is not None:
            try:
                await ws.close()
            except Exception:
                pass
