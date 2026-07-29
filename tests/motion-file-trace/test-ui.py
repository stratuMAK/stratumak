#!/usr/bin/env python3
"""End-to-end trace of (motion_file, motion_line) through an o-word call.

An o<sub> call into a separate file makes the interpreter restart line
numbering at 0 (interp_o_word.cc control_back_to), so a line number alone
does not identify a program location. main.ngc and trace_sub.ngc both have
moves on lines 2 and 3: the status must say which file it means, or a UI
highlights an unrelated line of whatever it happens to be showing.

This runs the real thing — server, interpreter, canon, motion — and records
every (file, line) the status reports while the program executes. It exists
because the defect this caught last time survived both code review and unit
tests: the naive-CAM chain flushes while a LATER line is executing, so
sampling the interpreter's current file at flush time filed every move at a
call boundary under the wrong side of it. Only a running machine showed it.
"""

import os
import sys
import time

import gmi
from gmi.constants import *
import gomc_test

# Each move is 5 mm at 600 mm/min = 0.5 s, in G61 exact stop so segments do
# not blend or chain — every one of them is the executing segment for long
# enough that a 20 ms poll cannot miss it.
POLL = 0.02

s = gmi.Stat()
c = gomc_test.Command()

gomc_test.wait_for_startup(s)

c.state(STATE_ESTOP_RESET)
c.state(STATE_ON)
c.mode(MODE_AUTO)
c.program_open('main.ngc')
c.wait_complete()

trace = []          # (basename, line) transitions, in order
absolute = True     # every non-empty motion_file arrives as an absolute path
main_paths = set()  # full motion_file values seen for the main program

c.auto(AUTO_RUN, 0)

deadline = time.time() + 60 * gomc_test.scale()
started = False
while time.time() < deadline:
    s.poll()
    f = s.motion_file
    if f:
        if not os.path.isabs(f):
            absolute = False
        key = (os.path.basename(f), s.motion_line)
        if not trace or trace[-1] != key:
            trace.append(key)
        if key[0] == 'main.ngc':
            main_paths.add(f)
        started = True
    if started and s.interp_state == INTERP_IDLE and not s.motion_line:
        break
    time.sleep(POLL)

print("trace:")
for name, line in trace:
    print("%s:%d" % (name, line))

if absolute:
    print("PASS motion_file-absolute")
else:
    print("FAIL motion_file-absolute: a relative path reached the client")

# Guard against the trace becoming vacuous: if the two files ever stop
# sharing line numbers, this test would pass without testing anything.
main_lines = {ln for name, ln in trace if name == 'main.ngc'}
sub_lines = {ln for name, ln in trace if name == 'trace_sub.ngc'}
if main_lines & sub_lines:
    print("PASS distinct-files-share-line-numbers")
else:
    print("FAIL distinct-files-share-line-numbers: main=%s sub=%s — the "
          "trace no longer exercises a collision" % (sorted(main_lines), sorted(sub_lines)))


# --- the three surfaces must name the loaded program identically ------------

# A client decides whether the line it is about to highlight belongs to the
# program it is showing by comparing these. If they disagree by so much as a
# spelling, that test is quietly wrong — so it is asserted, not assumed.

s.poll()
loaded = s.file

if main_paths == {loaded}:
    print("PASS motion_file-matches-stat-file")
else:
    print("FAIL motion_file-matches-stat-file: stat.file=%r but the main "
          "program's motion_file was %r" % (loaded, sorted(main_paths)))

from gmi.ngcpreview import NgcpreviewClient
preview = NgcpreviewClient(gmi.rest_url(), instance=gmi.preview_instance())
res = preview.gen_preview(filename=loaded, initcodes="", unitcode="g21")
if res.files and res.files[0] == loaded:
    print("PASS preview-file-table-matches-stat-file")
else:
    print("FAIL preview-file-table-matches-stat-file: stat.file=%r, "
          "preview files=%r" % (loaded, list(res.files or [])))

# The same file reached by a different spelling must come back as one
# identity: the interpreter composes sub-file paths from SUBROUTINE_PATH and
# can hand back anything from a relative name to a symlinked one.
link = os.path.join(os.path.dirname(loaded), 'link')
try:
    if not os.path.islink(link):
        os.symlink('.', link)
    c.program_open(os.path.join(link, 'main.ngc'))
    c.wait_complete()
    s.poll()
    if s.file == loaded:
        print("PASS one-identity-per-file")
    else:
        print("FAIL one-identity-per-file: opened through a symlink and got "
              "%r, want %r" % (s.file, loaded))
finally:
    if os.path.islink(link):
        os.remove(link)

sys.exit(0)
