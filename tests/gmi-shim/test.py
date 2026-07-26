#!/usr/bin/env python3
"""gmi Python shim contract tests against stub servers (no gomc-server).

Pins the fixes from GMI_PYTHON_REVIEW_FINDINGS.md:
  GP-1  tools.put sends the {"entry": {...}} envelope (flat body zeroed the tool)
  GP-6  tools.get maps the server's zero entry to None
  GP-4  jog and abort arrive at the server in issue order (both synchronous)
  GP-7  Stat attributes are frozen between poll() calls (snapshot swap)
  GP-2/3 ErrorChannel connects late (server not up yet) and survives a server
         restart, delivering messages after reconnect
  GP-5  PositionLogger start() issues start_logger + clear_logger and stop()
         issues stop_logger
  GP-11 constants re-exported at module level, classic names present

Progress goes to stdout (compared against `expected`); shim/stub chatter stays
on stderr.
"""

import asyncio
import json
import os
import socket
import struct
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import websockets


def ok(name):
    print("PASS", name)
    sys.stdout.flush()


def fail(name, detail):
    print("FAIL", name, detail)
    sys.stdout.flush()
    sys.exit(1)


def wait_until(pred, timeout=15.0, interval=0.05):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pred():
            return True
        time.sleep(interval)
    return False


# ─── REST stub ───

ZERO_TOOL = {"toolno": 0, "pocketno": 0, "x_offset": 0.0, "z_offset": 0.0,
             "diameter": 0.0, "orientation": 0}
REAL_TOOL = {"toolno": 8, "pocketno": 3, "x_offset": 1.25, "z_offset": -2.5,
             "diameter": 6.0, "orientation": 2}


class RestHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""

    def _send(self, obj, code=200):
        data = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path.endswith("/info"):
            self.server.info_requests.append(self.path)
            if self.server.info_status != 200:
                self._send({"error": "unknown API instance"},
                           self.server.info_status)
            else:
                self._send(self.server.info_payload)
        elif self.path == "/api/v1/milltask/stat":
            if self.server.stat_status != 200:
                # Model a server that is up but cannot answer (starting,
                # unloading, wedged) — a poll failure, like a refused connect.
                self._send({"error": "unavailable"}, self.server.stat_status)
            else:
                self._send(self.server.stat_snapshot)
        elif self.path == "/api/v1/milltask/7":
            self._send(ZERO_TOOL)  # absent tool = zero entry, NOT 404
        elif self.path == "/api/v1/milltask/8":
            self._send(REAL_TOOL)
        elif self.path.startswith("/api/v1/") and self.path.endswith("/"):
            # Raw tool-slot store: /api/v1/{tooltable-instance}/
            inst = self.path[len("/api/v1/"):-1]
            self.server.slot_requests.append(inst)
            slots = self.server.slots.get(inst)
            self._send(slots if slots is not None else {},
                       200 if slots is not None else 404)
        else:
            self._send({}, 404)

    def do_POST(self):
        body = self._body()
        self.server.requests.append(
            ("POST", self.path, json.loads(body) if body else {}))
        self._send(1)

    def do_PUT(self):
        body = self._body()
        self.server.requests.append(
            ("PUT", self.path, json.loads(body) if body else {}))
        self._send({"ok": True})


def start_rest_stub():
    srv = ThreadingHTTPServer(("127.0.0.1", 0), RestHandler)
    srv.stat_snapshot = {}
    srv.stat_status = 200
    srv.requests = []
    srv.info_payload = {}
    srv.info_status = 200
    srv.info_requests = []
    srv.slots = {}
    srv.slot_requests = []
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, srv.server_address[1]


# ─── WS stub ───

class WsStub:
    """Records every frame; test pushes updates via push()."""

    def __init__(self, port=0):
        self.want_port = port
        self.port = None
        self.frames = []
        self.conns = []
        self.loop = None
        self._started = threading.Event()
        self._stop = None
        self._thread = None

    def start(self):
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        if not self._started.wait(5):
            raise RuntimeError("WS stub failed to start")
        return self

    def _run(self):
        self.loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self.loop)
        self.loop.run_until_complete(self._main())
        self.loop.close()

    async def _main(self):
        self._stop = asyncio.Event()
        server = await websockets.serve(self._handler, "127.0.0.1",
                                        self.want_port)
        self.port = server.sockets[0].getsockname()[1]
        self._started.set()
        await self._stop.wait()
        # Model a real server restart/crash: RST every client connection so the
        # peer notices immediately. A graceful WebSocket close is unreliable
        # here as a "restart" signal — on a loaded runner it can be delivered
        # late, or the handler keeps auto-ponging with the listener already
        # closed, leaving the ErrorChannel client on a dead connection past its
        # 20 s reconnect window (the CI-only errorchannel-reconnect flake). A
        # RST (SO_LINGER 0 + transport abort) is a single packet the OS
        # delivers regardless of load, so the client reconnects deterministically.
        for c in list(self.conns):
            try:
                sock = c.transport.get_extra_info("socket")
                if sock is not None:
                    sock.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER,
                                    struct.pack("ii", 1, 0))
                c.transport.abort()
            except Exception:
                pass
        server.close()
        await server.wait_closed()

    async def _handler(self, websocket):
        self.conns.append(websocket)
        try:
            async for raw in websocket:
                self.frames.append(json.loads(raw))
        except Exception:
            pass

    def push(self, obj):
        conn = self.conns[-1]
        asyncio.run_coroutine_threadsafe(
            conn.send(json.dumps(obj)), self.loop).result(5)

    def stop(self):
        self.loop.call_soon_threadsafe(self._stop.set)
        self._thread.join(timeout=5)

    def find_frames(self, **kv):
        return [f for f in self.frames
                if all(f.get(k) == v for k, v in kv.items())]


# ─── Phase 1: REST-backed contracts ───

def main():
    rest, rest_port = start_rest_stub()
    os.environ["GMC_REST_URL"] = f"http://127.0.0.1:{rest_port}"
    os.environ.pop("GMC_INSTANCE", None)

    import gmi
    from gmi.stat import Stat
    from gmi.command import Command
    from gmi.tools import ToolTable

    # GP-11: module-level constants, classic names, correct values.
    if (gmi.MODE_MDI, gmi.STATE_ON, gmi.EXEC_WAITING_FOR_SYSTEM_CMD,
            gmi.MOTION_TYPE_TRAVERSE, gmi.FLOOD_ON) != (3, 4, 9, 1, 1):
        fail("constants", "wrong module-level constant values")
    ok("constants")

    # Instance-resolution schema: a client built with no instance follows
    # GMC_INSTANCE; an explicit instance wins; unset falls back to the default.
    # This is the rule AXIS leaned on when it constructed a Command subclass
    # directly — a class hardcoding "milltask" instead posted to a nonexistent
    # instance on a multi-instance server.
    if Command()._base.rsplit("/", 1)[-1] != "milltask":
        fail("instance-resolution", f"unset default = {Command()._base}")
    os.environ["GMC_INSTANCE"] = "pnp.task"
    if Command()._base.rsplit("/", 1)[-1] != "pnp.task":
        fail("instance-resolution", "bare client ignored GMC_INSTANCE")
    if Command(instance="explicit")._base.rsplit("/", 1)[-1] != "explicit":
        fail("instance-resolution", "explicit instance did not win")
    os.environ.pop("GMC_INSTANCE", None)
    ok("instance-resolution")

    # GP-7: frozen snapshots. Constructor takes a best-effort initial poll.
    rest.stat_snapshot = {"task": {"mode": 1}, "tool_in_spindle": 3}
    s = Stat()
    if s.task_mode != 1:
        fail("stat-initial-snapshot", f"task_mode={s.task_mode}")
    rest.stat_snapshot = {"task": {"mode": 2}, "tool_in_spindle": 3,
                          "boot_id": "1785087147147137065"}
    if s.task_mode != 1:
        fail("stat-frozen", "attribute changed without poll()")
    s.poll()
    if s.task_mode != 2:
        fail("stat-poll-refresh", f"task_mode={s.task_mode}")
    ok("stat-frozen-between-polls")

    # A server that has gone away must be visible as such: poll() keeps the
    # last snapshot (drivers loop through outages) and reports connected=False.
    # That flag is the only thing telling a UI that what it shows is no longer
    # live — without it a dead controller's last state reads as current.
    # boot_id rides along: it identifies the task behind the address, so a
    # client can tell a restarted task from an uninterrupted one.
    if not s.connected:
        fail("stat-connected", "connected False after a good poll")
    if s.boot_id != "1785087147147137065":
        fail("stat-connected", f"boot_id={s.boot_id!r}")
    rest.stat_status = 503
    s.poll()
    if s.connected:
        fail("stat-connected", "connected still True with the server gone")
    if s.task_mode != 2:
        fail("stat-connected", "a failed poll discarded the cached snapshot")
    rest.stat_status = 200
    s.poll()
    if not s.connected:
        fail("stat-connected", "connected still False after the server returned")
    ok("stat-connected-tracks-server")

    # GP-4: jog and abort both synchronous, server sees issue order.
    c = Command()
    c.jog(1, False, 0, velocity=10.0)
    c.abort()
    posts = [r for r in rest.requests if r[0] == "POST"]
    order = [p[1] for p in posts]
    if order != ["/api/v1/milltask/jog", "/api/v1/milltask/abort"]:
        fail("command-order", f"order={order}")
    jog_body = posts[0][2]
    if jog_body.get("jog_type") != 1 or jog_body.get("velocity") != 10.0:
        fail("command-order", f"jog body={jog_body}")
    ok("command-jog-abort-ordered")

    # GP-1: PUT carries the entry envelope; caller's dict is not mutated.
    tt = ToolTable()
    entry = {"x_offset": 1.5, "diameter": 3.0}
    tt.put(5, entry)
    puts = [r for r in rest.requests if r[0] == "PUT"]
    if not puts:
        fail("tools-put", "no PUT recorded")
    method, path, body = puts[-1]
    if path != "/api/v1/milltask/5":
        fail("tools-put", f"path={path}")
    if "entry" not in body or "x_offset" in body:
        fail("tools-put", f"body not enveloped: {body}")
    if body["entry"].get("toolno") != 5 or body["entry"].get("x_offset") != 1.5:
        fail("tools-put", f"entry={body['entry']}")
    if "toolno" in entry:
        fail("tools-put", "caller dict mutated")
    ok("tools-put-envelope")

    # GP-6: zero entry reads back as None; a real tool as a dict.
    if tt.get(7) is not None:
        fail("tools-get", "zero entry did not map to None")
    t8 = tt.get(8)
    if not t8 or t8.get("diameter") != 6.0:
        fail("tools-get", f"real tool={t8}")
    ok("tools-get-absent-none")

    # ─── Phase 2: WS-backed contracts ───

    # GP-2: ErrorChannel constructed BEFORE the server exists must connect
    # once the server appears. Pick a port by binding/releasing it first.
    probe = WsStub().start()
    ws_port = probe.port
    probe.stop()
    os.environ["GMC_REST_URL"] = f"http://127.0.0.1:{ws_port}"

    stub_holder = {}

    def start_ws_later():
        time.sleep(1.0)
        stub_holder["ws"] = WsStub(ws_port).start()

    threading.Thread(target=start_ws_later, daemon=True).start()
    e = gmi.ErrorChannel()  # retries until the stub appears
    if not wait_until(lambda: "ws" in stub_holder and
                      stub_holder["ws"].find_frames(action="subscribe",
                                                    api="emcerror")):
        fail("errorchannel-late-server", "no subscribe after server start")
    ws = stub_holder["ws"]
    ws.push({"type": "update", "func": "get_errors",
             "data": [{"kind": 11, "text": "hello"}]})
    if not wait_until(lambda: e.poll() == (11, "hello"), timeout=5):
        fail("errorchannel-late-server", "message not delivered")
    ok("errorchannel-connects-late")

    # GP-3: server restart → reconnect + resubscribe + delivery.
    ws.stop()
    ws2 = WsStub(ws_port).start()
    if not wait_until(lambda: ws2.find_frames(action="subscribe",
                                              api="emcerror"), timeout=20):
        fail("errorchannel-reconnect", "no resubscribe after restart")
    ws2.push({"type": "update", "func": "get_errors",
              "data": [{"kind": 1, "text": "after-restart"}]})
    if not wait_until(lambda: e.poll() == (1, "after-restart"), timeout=5):
        fail("errorchannel-reconnect", "message not delivered after reconnect")
    e.stop()
    ok("errorchannel-reconnects")

    # GP-5: PositionLogger lifecycle calls.
    from gmi.positionlogger import PositionLogger
    col = (255, 0, 0, 255)
    pl = PositionLogger(None, col, col, col, col, col, col, "XYZ", 0)
    pl.start(0.01)
    if not wait_until(lambda: ws2.find_frames(action="subscribe",
                                              func="get_positions")):
        fail("positionlogger", "no get_positions subscribe")
    if not wait_until(lambda: ws2.find_frames(action="call",
                                              func="start_logger")):
        fail("positionlogger", "start_logger not sent")
    if not wait_until(lambda: ws2.find_frames(action="call",
                                              func="clear_logger")):
        fail("positionlogger", "clear_logger not sent on first connect")
    sl = ws2.find_frames(action="call", func="start_logger")[0]
    if sl.get("args", {}).get("interval_us") != 10000:
        fail("positionlogger", f"interval args={sl.get('args')}")
    # Feed one point chunk and check it lands in the local buffer.
    ws2.push({"type": "update", "func": "get_positions",
              "data": [{"t": 1, "p": [1, 2, 3, 0, 0, 0, 0, 0, 0]},
                       {"bad": "point"},
                       {"t": 2, "p": [4, 5, 6, 0, 0, 0, 0, 0, 0]}]})
    if not wait_until(lambda: pl.npts >= 2, timeout=5):
        fail("positionlogger", f"points not processed (npts={pl.npts})")
    pl.stop()
    if not wait_until(lambda: ws2.find_frames(action="call",
                                              func="stop_logger")):
        fail("positionlogger", "stop_logger not sent on stop()")
    ws2.stop()
    ok("positionlogger-lifecycle")

    # ─── GET /info: the machine description ───
    #
    # A client used to DERIVE peer names ("{instance}-preview") and hardcode
    # defaults ("tooltable"). Both are right only in a single-instance config,
    # and the tool table one made every multi-instance config 404 at 10 Hz.
    # Names now come from the server, which resolved and verified them.

    # The WS phase above repointed GMC_REST_URL at the WS stub's port; these are
    # REST contracts again.
    os.environ["GMC_REST_URL"] = f"http://127.0.0.1:{rest_port}"

    rest.info_payload = {
        "peers": {"tooltable": "pnp.tt", "preview": "pnp.task-preview",
                  "manualtoolchange": "", "pyvcp": "pnp.panel"},
        # A lathe wiring only reverse: the per-direction flags are what keep the
        # clockwise button hidden while the counter-clockwise one shows.
        "caps": {"spindle_forward": False, "spindle_reverse": True,
                 "spindle_on": False, "spindle_speed": True,
                 "spindle_brake": False, "limit_switch_override": True,
                 "coolant_mist": False, "coolant_flood": True},
    }
    gmi.reset_info()
    if (gmi.preview_instance(), gmi.pyvcp_instance(),
            gmi.tooltable_instance()) != ("pnp.task-preview", "pnp.panel", "pnp.tt"):
        fail("info-peers", "peer names not taken from /info")
    caps = gmi.info().caps
    if not (caps.spindle_reverse and caps.spindle_speed
            and caps.limit_switch_override and caps.coolant_flood):
        fail("info-peers", "capability flags not decoded")
    if caps.spindle_forward or caps.spindle_on or caps.spindle_brake or caps.coolant_mist:
        fail("info-peers", "unwired pins reported as wired")
    # An unset peer falls back to the module's own default instance name, which
    # is what keeps a single-instance config working without naming anything —
    # EXCEPT pyvcp, whose panel is optional and gated on the server reporting
    # one. mtc keeps a default (it is probe-gated, so a wrong default just 404s
    # and disables the feature); pyvcp must return "" so AXIS shows no panel
    # rather than requesting a fabricated default that isn't loaded.
    if gmi.mtc_instance() != "manualtoolchange":
        fail("info-peers", f"unset mtc fallback = {gmi.mtc_instance()}")
    rest.info_payload["peers"]["pyvcp"] = ""
    gmi.reset_info()
    if gmi.pyvcp_instance() != "":
        fail("info-peers", f"absent pyvcp fabricated {gmi.pyvcp_instance()!r}")
    ok("info-peers")

    # The answer is fixed for the life of the task, so it is fetched once no
    # matter how many callers ask.
    before = len(rest.info_requests)
    for _i in range(20):
        gmi.preview_instance()
        gmi.tooltable_instance()
    if len(rest.info_requests) != before:
        fail("info-cached", f"{len(rest.info_requests) - before} extra requests")
    ok("info-cached")

    os.environ["GMC_PREVIEW_INSTANCE"] = "override-preview"
    if gmi.preview_instance() != "override-preview":
        fail("info-env-override", "env var did not win over /info")
    del os.environ["GMC_PREVIEW_INSTANCE"]
    ok("info-env-override")

    # A server that cannot answer must fail loudly ONCE, not be retried: the
    # causes (server older than the client, wrong GMC_INSTANCE, task never
    # started) do not heal, and retrying is how the 10 Hz storm happened.
    gmi.reset_info()
    rest.info_status = 404
    before = len(rest.info_requests)
    for _i in range(5):
        try:
            gmi.info()
            fail("info-failure-cached", "missing /info did not raise")
        except gmi.InfoUnavailable:
            pass
    if len(rest.info_requests) - before != 1:
        fail("info-failure-cached",
             f"{len(rest.info_requests) - before} requests, want exactly 1")
    rest.info_status = 200
    gmi.reset_info()
    ok("info-failure-cached")

    # stat.tool_table reads the raw slot store, whose instance is its own module
    # — the one thing a client must never guess, because axis.py reads
    # tool_table[0] once per display cycle.
    rest.slots["pnp.tt"] = [{"idx": 0, "toolno": 4, "z_offset": -1.5}]
    rest.stat_snapshot = {"tool_in_spindle": 4}
    s3 = Stat()
    s3.poll()
    if s3.tool_table[0].id != 4:
        fail("stat-tooltable-from-info", f"tool_table[0]={s3.tool_table[0][0]}")
    if "pnp.tt" not in rest.slot_requests:
        fail("stat-tooltable-from-info", "slot store not addressed as pnp.tt")
    ok("stat-tooltable-from-info")

    # And a slot store that IS unreachable must not be re-fetched on every
    # access: a failed fetch is not cached (the table must not freeze), so
    # without a retry floor one wrong name is ten requests a second.
    gmi.reset_info()
    rest.info_payload["peers"]["tooltable"] = "missing.tt"
    s4 = Stat()
    s4.poll()
    before = len(rest.slot_requests)
    for _i in range(10):
        s4.tool_table
    tries = len(rest.slot_requests) - before
    if tries > 1:
        fail("stat-tooltable-retry-floor", f"{tries} fetches for 10 reads")
    if s4.tool_table[0].id != -1:
        fail("stat-tooltable-retry-floor", "unreachable store did not yield an empty slot")
    ok("stat-tooltable-retry-floor")

    print("ALL OK")


if __name__ == "__main__":
    main()
