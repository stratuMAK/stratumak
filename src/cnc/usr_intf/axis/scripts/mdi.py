#!/usr/bin/env python3
#    This is a component of AXIS, a front-end for LinuxCNC
#    Copyright 2004, 2005, 2006 Jeff Epler <jepler@unpythonic.net>
#    Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de> — GMI port
#
#    This program is free software; you can redistribute it and/or modify
#    it under the terms of the GNU General Public License as published by
#    the Free Software Foundation; either version 2 of the License, or
#    (at your option) any later version.
#
#    This program is distributed in the hope that it will be useful,
#    but WITHOUT ANY WARRANTY; without even the implied warranty of
#    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
#    GNU General Public License for more details.
#
#    You should have received a copy of the GNU General Public License
#    along with this program; if not, write to the Free Software
#    Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.

'''Manual Data Input - issue a single line of G-code to the running system

mdi.py may be specified on the commandline, e.g.,
        bin/mdi g0 x0

With no arguments it prompts for MDI lines until EOF; an empty line prints the
current position instead of sending anything.

Exit status is 1 if the line could not be issued or the machine reported an
error executing it.

The machine is addressed over the stratuMAK REST API: GMC_REST_URL selects the
server, GMC_INSTANCE the task instance.
'''
import sys
import threading
import time
import urllib.error

import gmi
from gmi.constants import MODE_MDI, NML_ERROR, OPERATOR_ERROR, RCS_ERROR

_ERROR_KINDS = (NML_ERROR, OPERATOR_ERROR)


class Notifier:
    """Prints the operator messages a command produced.

    stratuMAK answers POST /mdi with an RCS status only; the reason for a refusal —
    and any `(MSG,…)`/`(DEBUG,…)` output — travels on the server's message
    list, the same one a running GUI renders. This reads that list and never
    acknowledges anything, so nothing is taken away from the GUI. An
    unreachable watch channel degrades to the RCS status alone — no reason
    text, and a queued line that fails later goes unnoticed.
    """

    def __init__(self):
        self._list = None
        self._seen = 0
        first = threading.Event()
        try:
            self._list = gmi.MessageList(on_update=lambda msgs: first.set())
        except Exception as e:
            print("mdi: operator messages unavailable: %s" % e, file=sys.stderr)
            return
        # The server pushes the whole list the moment we subscribe. Wait for
        # that snapshot before taking the baseline: subscribing does not mean
        # it has arrived, and a baseline taken too early is empty — which would
        # report every message a GUI left unacknowledged as ours.
        if not first.wait(2.0):
            print("mdi: no message list yet; already-queued operator messages "
                  "may be reported", file=sys.stderr)
        # Ignore whatever is already queued; only what this session provokes
        # is ours to report.
        self._advance(self._list.get_list())

    def _advance(self, msgs):
        for m in msgs:
            self._seen = max(self._seen, m.get("id", 0))

    def report(self, timeout):
        """Print messages that arrived since the last call; True if any was an
        error.

        The list is pushed to us at the watch rate, so a message published
        while the POST was in flight needs a moment to land; `timeout` bounds
        that wait. Messages a *later* part of the program emits (a `(MSG,…)`
        the sequencer only reaches once the move runs) arrive after we are
        gone — this is the terminal, not a GUI.
        """
        if self._list is None:
            return False
        deadline = time.monotonic() + timeout
        while True:
            fresh = [m for m in self._list.get_list()
                     if m.get("id", 0) > self._seen]
            if fresh or time.monotonic() >= deadline:
                break
            time.sleep(0.02)
        self._advance(fresh)
        for m in fresh:
            print(m.get("text", ""), file=sys.stderr)
        return any(m.get("kind") in _ERROR_KINDS for m in fresh)

    def stop(self):
        if self._list is not None:
            self._list.stop()


def send(c, notifier, line):
    """Issue one MDI line. Returns True if the machine accepted it."""
    try:
        c.mode(MODE_MDI)
        status = c.mdi(line)
    except urllib.error.HTTPError:
        # gmi.Command already printed the server's reason (wrong state,
        # unhomed, a busy interpreter refusing the queue).
        notifier.report(0.2)
        return False
    except (urllib.error.URLError, OSError) as e:
        print("mdi: cannot reach %s: %s" % (gmi.rest_url(), e), file=sys.stderr)
        return False
    # An interpreter error on the line comes back as RCS_ERROR in a 200 body,
    # with the text on the message list — wait a little longer for it, because
    # that text is the only thing that says what was wrong.
    #
    # The status alone is not enough: a line issued while the interpreter is
    # still busy is *queued*, so the POST answers RCS_DONE and the line only
    # fails later, when the queue drains. An error message is therefore as
    # much a verdict as the status is.
    failed = status == RCS_ERROR
    return not (notifier.report(1.0 if failed else 0.2) or failed)


def main():
    c = gmi.Command()
    notifier = Notifier()
    try:
        if len(sys.argv) > 1:
            return 0 if send(c, notifier, " ".join(sys.argv[1:])) else 1

        # Positions in the machine's configured units, as classic
        # linuxcnc.stat() reported them; the gmi wire format is mm. Built only
        # for the interactive session — the one-shot path must not pay for it.
        s = gmi.Stat().machine_units()
        status = 0
        while True:
            try:
                line = input("MDI> ").strip()
            except (EOFError, KeyboardInterrupt):
                print(file=sys.stderr)
                return status
            if not line:
                s.poll()
                print(s.position)
            elif not send(c, notifier, line):
                # A refused line ends the command, not the session: the prompt
                # stays up so the operator can fix it and retry.
                status = 1
    finally:
        notifier.stop()


if __name__ == "__main__":
    sys.exit(main())

# vim:sw=4:sts=4:et:
