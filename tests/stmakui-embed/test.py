#!/usr/bin/env python3
"""stmakui embedded into a Tk container frame, the way AXIS embeds webapps.

A Tk window plays AXIS: a container frame for the page, an entry for "one of
AXIS's own widgets", AXIS_FORWARD_EVENTS_TO set to its window, and the focus
reclaim Tcl taken verbatim from axis.py. Input comes from XTEST, so it travels
the same X paths a real keyboard and mouse do.

Covers:
  - the page loads once the server answers (it is started late), with the
    URL built from --path and --instance
  - a key the page uses stays in the page
  - Escape (AXIS abort) still reaches Tk while a field in the page has focus
  - a key the page does not use reaches Tk as press AND release, also for a
    tap too fast for WebKit to answer before the release arrives
  - a forwarded key held while the page loses the keyboard is released
  - a click into a Tk widget that does not take the focus itself (the
    preview, a button) takes the keyboard back from the page

A press without its release would keep an AXIS keyboard jog running, which
is why the pairing is checked from several directions.
"""

import functools
import http.server
import os
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import tkinter as tk

from Xlib import X, XK, display
from Xlib.ext import xtest

HERE = os.path.dirname(os.path.abspath(__file__))
AXIS_PY = os.path.join(HERE, "..", "..", "src", "cnc", "usr_intf", "axis",
                       "scripts", "axis.py")
TIMEOUT = 30.0      # generous: WebKit start-up on a loaded CI runner


def ok(name):
    print("PASS", name)
    sys.stdout.flush()


def fail(name, why):
    print("FAIL", name, "-", why)
    sys.stdout.flush()
    sys.exit(1)


# --- the web server ---------------------------------------------------------

requests = []       # paths below /app/ that were fetched
page_keys = []      # (key, target) the page reported


class Handler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def do_GET(self):
        if self.path.startswith("/log?"):
            m = re.match(r"/log\?key=([^&]*)&target=(\w*)", self.path)
            if m:
                from urllib.parse import unquote
                page_keys.append((unquote(m.group(1)), m.group(2)))
            self.send_response(204)
            self.end_headers()
            return
        if self.path.startswith("/app/"):
            requests.append(self.path)
        super().do_GET()


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def start_server(port):
    srv = http.server.ThreadingHTTPServer(
        ("127.0.0.1", port),
        functools.partial(Handler, directory=os.path.join(HERE, "www")))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


# --- the Tk side ------------------------------------------------------------

def focus_tcl():
    """The focus reclaim AXIS installs, read from axis.py itself."""
    src = open(AXIS_PY).read()
    m = re.search(r'_WEBAPP_FOCUS_TCL = r"""(.*?)"""', src, re.S)
    if not m:
        fail("setup", "no _WEBAPP_FOCUS_TCL in " + AXIS_PY)
    return m.group(1)


root = tk.Tk()
root.geometry("600x450+0+0")
entry = tk.Entry(root)
entry.pack(side="bottom", fill="x")
# Like the AXIS preview or a button: clicking it does not move the Tk focus,
# so only the focus reclaim can take the keyboard back on such a click.
label = tk.Label(root, text="Tk", height=2)
label.pack(side="bottom", fill="x")
container = tk.Frame(root, container=1, borderwidth=0, highlightthickness=0)
container.pack(fill="both", expand=1)
root.update()

tk_keys = []        # ("press"|"release", keysym) as Tk received them
root.bind_all("<KeyPress>", lambda e: tk_keys.append(("press", e.keysym)))
root.bind_all("<KeyRelease>", lambda e: tk_keys.append(("release", e.keysym)))
root.tk.eval(focus_tcl())

dpy = display.Display()
# Autorepeat would hand Tk a press/release pair of its own for a held key once
# Tk has the keyboard, and so hide a missing release from stmakui.
dpy.change_keyboard_control(auto_repeat_mode=X.AutoRepeatModeOff)
dpy.sync()


def pump(seconds):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        root.update()
        time.sleep(0.01)


def wait_for(cond, timeout=TIMEOUT):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        root.update()
        if cond():
            return True
        time.sleep(0.01)
    return False


def click(x, y):
    """Click at (x, y) inside the Tk window."""
    xtest.fake_input(dpy, X.MotionNotify, x=root.winfo_rootx() + x,
                     y=root.winfo_rooty() + y)
    xtest.fake_input(dpy, X.ButtonPress, 1)
    xtest.fake_input(dpy, X.ButtonRelease, 1)
    dpy.sync()
    pump(0.5)


def keycode(name):
    return dpy.keysym_to_keycode(XK.string_to_keysym(name))


def key(name, press=True, release=True):
    if press:
        xtest.fake_input(dpy, X.KeyPress, keycode(name))
    if release:
        xtest.fake_input(dpy, X.KeyRelease, keycode(name))
    dpy.sync()


def plug_has_x_focus():
    focus = dpy.get_input_focus().focus
    kids = dpy.create_resource_object("window", container.winfo_id()).query_tree().children
    return any(getattr(focus, "id", None) == k.id for k in kids)


def tk_got(kind, name):
    return (kind, name) in tk_keys


def page_got(name, target=None):
    return any(k == name and (target is None or t == target) for k, t in page_keys)


# A window manager gives the application window the focus; there is none here.
root.focus_force()
pump(0.2)

# --- start: page requested before the server is up --------------------------

stmakui = shutil.which("stmakui")
if not stmakui:
    fail("setup", "stmakui not on PATH")
port = free_port()
env = dict(os.environ,
           STMAK_REST_URL="http://127.0.0.1:%d/" % port,
           AXIS_FORWARD_EVENTS_TO=str(root.winfo_id()))
viewer = subprocess.Popen([stmakui, "--xid", str(container.winfo_id()),
                           "--path", "/app/embed/", "--instance", "testinst"],
                          env=env)
try:
    pump(1.5)       # connection refused meanwhile: stmakui must retry
    start_server(port)

    if not wait_for(lambda: requests):
        fail("page-loads-once-server-answers", "no request within %ds" % TIMEOUT)
    ok("page-loads-once-server-answers")

    if requests[0] != "/app/embed/?instance=testinst":
        fail("url-carries-path-and-instance", "requested %r" % requests[0])
    ok("url-carries-path-and-instance")

    # The page needs its script running before key reports mean anything.
    pump(1.0)

    # --- a key the page uses stays in the page ------------------------------
    click(60, 20)       # the input field
    if not wait_for(plug_has_x_focus, 5):
        fail("key-used-by-page-stays-in-page", "click did not move the keyboard into the page")
    del tk_keys[:]
    key("b")
    if not wait_for(lambda: page_got("b", "INPUT")):
        fail("key-used-by-page-stays-in-page", "page did not see b")
    pump(0.5)
    if tk_got("press", "b") or tk_got("release", "b"):
        fail("key-used-by-page-stays-in-page", "Tk saw b too: %r" % tk_keys)
    ok("key-used-by-page-stays-in-page")

    # --- Escape is AXIS's abort: it must get through even from a field ------
    del tk_keys[:]
    key("Escape")
    if not wait_for(lambda: tk_got("press", "Escape") and tk_got("release", "Escape"), 10):
        fail("escape-reaches-tk-from-a-field", "Tk saw %r" % tk_keys)
    ok("escape-reaches-tk-from-a-field")

    # --- an unused key reaches Tk, press while held, release on release -----
    click(300, 250)     # the page body
    del tk_keys[:]
    key("F5", release=False)
    if not wait_for(lambda: tk_got("press", "F5"), 10):
        fail("unused-key-reaches-tk-with-press-and-release", "no press: %r" % tk_keys)
    pump(0.3)
    if tk_got("release", "F5"):
        fail("unused-key-reaches-tk-with-press-and-release", "released while held: %r" % tk_keys)
    key("F5", press=False)
    if not wait_for(lambda: tk_got("release", "F5"), 10):
        fail("unused-key-reaches-tk-with-press-and-release", "no release: %r" % tk_keys)
    ok("unused-key-reaches-tk-with-press-and-release")

    # --- a tap faster than WebKit's answer still pairs ----------------------
    # Press and release go out back to back, so the release reaches stmakui
    # before WebKit has handed the press back as unused.
    del tk_keys[:]
    key("F6")
    if not wait_for(lambda: tk_got("press", "F6") and tk_got("release", "F6"), 10):
        fail("fast-tap-reaches-tk-with-press-and-release", "Tk saw %r" % tk_keys)
    if tk_keys.index(("press", "F6")) > tk_keys.index(("release", "F6")):
        fail("fast-tap-reaches-tk-with-press-and-release", "release before press: %r" % tk_keys)
    ok("fast-tap-reaches-tk-with-press-and-release")

    # --- held key, then the page loses the keyboard -------------------------
    del tk_keys[:]
    key("F7", release=False)
    if not wait_for(lambda: tk_got("press", "F7"), 10):
        fail("held-key-released-when-page-loses-keyboard", "no press: %r" % tk_keys)
    click(entry.winfo_x() + 10, entry.winfo_y() + 5)
    if not wait_for(lambda: tk_got("release", "F7"), 10):
        fail("held-key-released-when-page-loses-keyboard", "no release: %r" % tk_keys)
    key("F7", press=False)
    ok("held-key-released-when-page-loses-keyboard")

    # --- a click into Tk takes the keyboard back ----------------------------
    click(300, 250)     # the page has the keyboard again
    if not wait_for(plug_has_x_focus, 5):
        fail("click-into-tk-takes-keyboard-back", "click did not move the keyboard into the page")
    click(label.winfo_x() + 10, label.winfo_y() + 5)
    if plug_has_x_focus():
        fail("click-into-tk-takes-keyboard-back", "page still has the X focus")
    del tk_keys[:]
    del page_keys[:]
    key("c")
    if not wait_for(lambda: tk_got("press", "c") and tk_got("release", "c"), 10):
        fail("click-into-tk-takes-keyboard-back", "Tk saw %r" % tk_keys)
    pump(0.5)
    if page_got("c"):
        fail("click-into-tk-takes-keyboard-back", "page saw c too")
    ok("click-into-tk-takes-keyboard-back")
finally:
    viewer.terminate()
    try:
        viewer.wait(5)
    except subprocess.TimeoutExpired:
        viewer.kill()
