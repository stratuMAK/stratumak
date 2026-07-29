#!/usr/bin/env python3
"""The controller converts [FILTER] source files into the G-code it runs.

A .png, a DXF, a script is not G-code. Classic left the conversion to each
GUI, so a UI that never implemented filtering could not open those programs,
and the controller's own [DISPLAY]OPEN_FILE handed the raw source to the
interpreter. The controller owns the loaded program, so it owns the
conversion — and every client sees the result through the ordinary status
and file-read APIs.

Runs the real thing: server, filter subprocess, interpreter. What it pins:

  - program_open returns while the conversion is still running. A real
    converter takes seconds to minutes; blocking would freeze every client.
  - the conversion is visible in the status while it happens.
  - `file` is the controller's output and `source_file` is what was opened,
    and the output is readable and previewable through the same APIs as any
    other program.
  - the converter is handed the source it was asked to convert.
  - a failure leaves the previously loaded program alone and says why, in
    the converter's own words.
"""

import os
import sys
import time

import gmi
from gmi.constants import *
from gmi.ngcpreview import NgcpreviewClient
import gomc_test

POLL = 0.05

s = gmi.Stat()
c = gomc_test.Command()
e = gmi.ErrorChannel()

gomc_test.wait_for_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_AUTO)
c.wait_complete()


def ok(name):
    print("PASS", name)


def fail(name, detail):
    print("FAIL", name, detail)


def wait_filter_done(timeout=60):
    deadline = time.time() + timeout * gomc_test.scale()
    saw_filtering = False
    saw_progress = False
    while time.time() < deadline:
        s.poll()
        if s.filtering:
            saw_filtering = True
            if s.filter_progress:
                saw_progress = True
        elif saw_filtering:
            return saw_filtering, saw_progress
        time.sleep(POLL)
    return saw_filtering, saw_progress


# --- converting a source file -----------------------------------------------

source = os.path.abspath("shape.tst")
start = time.time()
c.program_open(source)
elapsed = time.time() - start
saw_filtering, saw_progress = wait_filter_done()

# Non-blocking means program_open returned while the conversion was still
# running. The wall-clock bound alone would flake on a loaded runner — the
# REST round-trip sits in the numerator while the converter's ~1.2s floor
# (fixture sleeps, which do NOT scale with GOMC_TEST_TIMEOUT_SCALE) sits in
# the denominator — so a slow-but-correct open is rescued by the direct
# observation: stat.filtering was seen true after program_open had returned.
# An inline (blocking) open fails both arms: it takes at least the converter's
# runtime AND completes with filtering already false.
if elapsed < 1.0 or saw_filtering:
    ok("program-open-does-not-block")
else:
    fail("program-open-does-not-block",
         "program_open took %.2fs and the conversion was never seen running; it ran inline" % elapsed)
if saw_filtering:
    ok("conversion-visible-in-status")
else:
    fail("conversion-visible-in-status",
         "stat.filtering was never true; no client could show that a conversion is running")
if saw_progress:
    ok("conversion-reports-progress")
else:
    fail("conversion-reports-progress",
         "the converter's FILTER_PROGRESS never reached stat.filter_progress")

s.poll()
if s.source_file == source:
    ok("source-file-is-what-was-opened")
else:
    fail("source-file-is-what-was-opened",
         "source_file=%r, opened %r" % (s.source_file, source))

if s.file and s.file != s.source_file and s.file.endswith(".ngc"):
    ok("file-is-the-converted-program")
else:
    fail("file-is-the-converted-program",
         "file=%r source_file=%r" % (s.file, s.source_file))

# Every client reads the program through get_file — including one that never
# issued the open, and one running on another machine.
preview = NgcpreviewClient(gmi.rest_url(), instance=gmi.preview_instance())
res = preview.get_file(s.file)
text = "\n".join(res.lines or [])
if not res.error and text:
    ok("converted-program-readable")
else:
    fail("converted-program-readable", "get_file error=%r lines=%d" % (res.error, len(res.lines or [])))

# The converter was handed the file it was asked to convert: shape.tst holds
# 12.5, and the program it produced moves there. A converter given the wrong
# path could still emit valid G-code, so this checks the content, not the exit
# code.
if "X12.5" in text:
    ok("converter-was-given-the-source")
else:
    fail("converter-was-given-the-source",
         "the converted program does not carry the source's value: %r" % text)

prev = preview.gen_preview(filename=s.file, initcodes="", unitcode="g21")
if not prev.error and prev.segments:
    ok("converted-program-previewable")
else:
    fail("converted-program-previewable",
         "gen_preview error=%r segments=%d" % (prev.error, len(prev.segments or [])))

converted = s.file


# --- a conversion that fails ------------------------------------------------

# Drain anything already queued so the assertion below is about this failure.
while e.poll():
    pass

c.program_open(os.path.abspath("broken.bad"))

# badfilter.sh fails in milliseconds, so the transient stat.filtering edge can
# fall between two 20 Hz polls — waiting on that edge would burn the whole
# timeout on a coin flip. The converter's diagnosis arriving on the error
# channel IS the completion signal of a failed conversion, so wait on that.
diag = ""
deadline = time.time() + 15 * gomc_test.scale()
while time.time() < deadline:
    msg = e.poll()
    if msg is None:
        time.sleep(POLL)
        continue
    if "bad magic number" in str(msg):
        diag = str(msg)
        break

s.poll()
# "Keeps the loaded program" means more than the stat string still spelling
# the old path: the FILE has to still exist and be servable — a failed
# conversion that deleted it would leave get_file 404ing and a re-run
# failing while the status looks perfectly healthy.
res = preview.get_file(converted)
if s.file == converted and not res.error and res.lines:
    ok("failure-keeps-the-loaded-program")
else:
    fail("failure-keeps-the-loaded-program",
         "file=%r (want %r), get_file error=%r lines=%d after a failed conversion"
         % (s.file, converted, res.error, len(res.lines or [])))
if not s.filtering:
    ok("failure-clears-filtering")
else:
    fail("failure-clears-filtering", "still reporting a conversion in progress")

# The converter's own diagnosis is the only thing that tells an operator why
# their file will not convert, so it has to reach the error channel rather
# than being flattened into an exit code.
if diag:
    ok("failure-reports-the-converters-words")
else:
    fail("failure-reports-the-converters-words",
         "the converter's stderr never reached the error channel")

# Nothing openable may be left behind from the half-written attempt: the
# converter wrote a G21 before failing, and the preview would happily open it.
# The loaded program must be the ONLY thing in the output directory — no
# published partial, no .part temp file.
leftovers = [f for f in os.listdir(os.path.dirname(converted))
             if f != os.path.basename(converted)]
if not leftovers:
    ok("failure-leaves-no-partial-program")
else:
    fail("failure-leaves-no-partial-program",
         "%r survive a failed conversion" % leftovers)


# --- an ordinary program still opens the ordinary way -----------------------

c.program_open(os.path.abspath("plain.ngc"))
c.wait_complete()
s.poll()
if s.file == s.source_file == os.path.abspath("plain.ngc") and not s.filtering:
    ok("unfiltered-program-unaffected")
else:
    fail("unfiltered-program-unaffected",
         "file=%r source_file=%r filtering=%r" % (s.file, s.source_file, s.filtering))

sys.exit(0)
