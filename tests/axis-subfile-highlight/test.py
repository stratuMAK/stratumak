#!/usr/bin/env python3
"""Preview geometry must be keyed on (file, line), not line alone.

An o-word call into a separate file restarts the interpreter's line
numbering, so the sub-file's line 2 and the main program's line 2 are
different places. Before file tracking, selecting one lit up both.

Covers the client half:
  - GLCanon keeps a source-file index aligned with every geometry list
  - highlight() matches the file as well as the line number
  - gcode.parse's replay carries file_idx from the wire into the canon

Runs headless: the GL entry points highlight() calls are stubbed out, so
what is under test is the selection logic, not the drawing.
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "lib", "python"))

import rs274.glcanon as glcanon
import gcode


def ok(name):
    print("PASS", name)
    sys.stdout.flush()


def fail(name, detail):
    print("FAIL", name, detail)
    sys.stdout.flush()
    sys.exit(1)


COLORS = dict.fromkeys(
    ("traverse", "straight_feed", "arc_feed", "dwell", "m1xx", "selected"),
    (1.0, 1.0, 1.0),
)


class _StubState:
    def __init__(self, seq, file_index=0):
        self.sequence_number = seq
        self.file_index = file_index
        self.plane = 170
        self.gcodes = ()
        self.mcodes = ()


def make_canon():
    c = glcanon.GLCanon(COLORS, "XYZ")
    c.first_move = False   # the first move is suppressed by design
    return c


def feed(canon, lineno, fileno, x):
    canon.next_line(_StubState(lineno, fileno))
    canon.straight_feed(x, 0, 0, 0, 0, 0, 0, 0, 0)


# --- 1. the parallel file index stays aligned with the geometry ------------

c = make_canon()
feed(c, 2, 0, 1.0)     # main.ngc line 2
feed(c, 3, 0, 2.0)     # main.ngc line 3
feed(c, 2, 1, 3.0)     # sub.ngc  line 2  <- collides with the first
feed(c, 3, 1, 4.0)     # sub.ngc  line 3
feed(c, 5, 0, 5.0)     # main.ngc line 5

if len(c.feed) != 5:
    fail("record-count", "recorded %d feed segments, want 5" % len(c.feed))
if len(c.feed_files) != len(c.feed):
    fail("record-alignment",
         "file index list has %d entries for %d segments" % (len(c.feed_files), len(c.feed)))
if c.feed_files != [0, 0, 1, 1, 0]:
    fail("record-attribution", "file indices %r, want [0, 0, 1, 1, 0]" % (c.feed_files,))
if [seg[0] for seg in c.feed] != [2, 3, 2, 3, 5]:
    fail("record-linenos", "line numbers %r, want [2, 3, 2, 3, 5]"
         % ([seg[0] for seg in c.feed],))
ok("segments-carry-their-source-file")


# --- 2. highlight() selects by (file, line) --------------------------------

# Stub the GL calls highlight() makes, and capture what it would draw.
drawn = []


def _record_line9(geometry, p1, p2):
    drawn.append(round(p2[0], 6))


class _StubGlHelpers:
    line9 = staticmethod(_record_line9)


glcanon._glhelpers = _StubGlHelpers
for name in ("glLineWidth", "glColor3f", "glBegin", "glEnd"):
    setattr(glcanon, name, lambda *a, **kw: None)
glcanon.GL_LINES = 0

# Same canon, so the colliding line 2 exists in both files.
del drawn[:]
c.highlight(2, "XYZ", fileno=0)
if drawn != [1.0]:
    fail("highlight-main", "line 2 of the main program drew %r, want [1.0] "
         "(3.0 is the sub-file's line 2)" % (drawn,))
ok("highlight-matches-the-named-file")

del drawn[:]
c.highlight(2, "XYZ", fileno=1)
if drawn != [3.0]:
    fail("highlight-sub", "line 2 of the sub-file drew %r, want [3.0]" % (drawn,))
ok("highlight-distinguishes-the-colliding-line")

# The default comes from the file the UI says it is displaying.
del drawn[:]
c.highlight_fileno = 1
c.highlight(3, "XYZ")
if drawn != [4.0]:
    fail("highlight-default", "with highlight_fileno=1, line 3 drew %r, want [4.0]"
         % (drawn,))
ok("highlight-defaults-to-the-displayed-file")

# A line that exists only in the other file selects nothing — better than
# lighting up an unrelated segment.
del drawn[:]
c.highlight_fileno = 1
c.highlight(5, "XYZ")
if drawn != []:
    fail("highlight-absent", "line 5 (main only) drew %r while showing the sub-file"
         % (drawn,))
ok("highlight-empty-when-the-line-is-another-files")


# --- 3. the wire's file_idx reaches the canon ------------------------------

class _WireSeg:
    def __init__(self, line_no, file_idx, x, seq=0):
        self.type = gcode.SegmentType.FEED
        self.line_no = line_no
        self.file_idx = file_idx
        self.seq = seq
        self.feedrate = 100.0
        self.start = _Pos(0)
        self.end = _Pos(x)
        self.center_x = self.center_y = 0.0
        self.rotation = 0
        self.plane = 1


class _WireDwell:
    def __init__(self, line_no, file_idx, seq, seconds=0.5):
        self.line_no = line_no
        self.file_idx = file_idx
        self.seq = seq
        self.seconds = seconds
        self.plane = 1
        self.pos = _Pos(0)


class _Pos:
    def __init__(self, x):
        self.x, self.y, self.z = x, 0.0, 0.0
        self.a = self.b = self.c = 0.0
        self.u = self.v = self.w = 0.0


class _WireResult:
    error = ""
    max_line = 5
    g5x_index = 1
    g5x_offset = None
    g92_offset = None
    xy_rotation = 0.0
    plane = 1
    tool_changes = []

    def __init__(self, segments=None, dwells=None):
        self.files = ["/programs/main.ngc", "/programs/subs/mysub.ngc"]
        self.segments = segments if segments is not None else [
            _WireSeg(2, 0, 1.0, seq=0), _WireSeg(2, 1, 3.0, seq=1)]
        self.dwells = dwells or []


WIRE_RESULT = None


class _StubClient:
    def __init__(self, *a, **kw):
        pass

    def gen_preview(self, **kw):
        return WIRE_RESULT


def replay(result):
    """Run gcode.parse against a canned wire result, with no server."""
    global WIRE_RESULT
    import gmi
    WIRE_RESULT = result
    canon = make_canon()
    real_client, real_peer = gcode.NgcpreviewClient, gmi.preview_instance
    gcode.NgcpreviewClient = _StubClient
    # parse() resolves the preview peer name from the server's /info before it
    # builds the client; stub it too, or this test only runs when a controller
    # happens to be up.
    gmi.preview_instance = lambda: "ngcpreview"
    try:
        return canon, gcode.parse("/programs/main.ngc", canon)
    finally:
        gcode.NgcpreviewClient, gmi.preview_instance = real_client, real_peer


c2, (result, seq) = replay(_WireResult())

if result != 0:
    fail("replay-result", "parse returned %r" % (result,))
if c2.feed_files != [0, 1]:
    fail("replay-attribution",
         "replayed file indices %r, want [0, 1]" % (c2.feed_files,))
if tuple(c2.source_files) != ("/programs/main.ngc", "/programs/subs/mysub.ngc"):
    fail("replay-file-table", "canon file table %r" % (c2.source_files,))
ok("replay-carries-file-idx-from-the-wire")


# --- 4. replay follows the recorder's emission order, not line numbers -----

# A dwell recorded BETWEEN two moves, carrying a line number that does not sit
# between theirs — what an O-word loop (or a called file, which restarts
# numbering) produces. Ordering by line number puts it at the end of the
# program, which is the N-7 symptom: every dwell drawn at the final position.
c3, (result3, _seq3) = replay(_WireResult(
    segments=[_WireSeg(5, 0, 1.0, seq=0), _WireSeg(6, 0, 2.0, seq=2)],
    dwells=[_WireDwell(99, 0, seq=1)]))

if result3 != 0:
    fail("order-result", "parse returned %r" % (result3,))
if len(c3.dwells) != 1:
    fail("order-dwell-count", "recorded %d dwells, want 1" % len(c3.dwells))
# dwells are (lineno, color, x, y, z, plane) — x is the position the dwell
# happened at.
dwell_x = round(c3.dwells[0][2], 6)
if dwell_x != 1.0:
    fail("order-dwell-position",
         "dwell drawn at x=%r, want 1.0 — at 2.0 it was replayed after both "
         "moves instead of between them" % (dwell_x,))
ok("replay-honours-emission-order")

# And the ordering is not an accident of list order: reversing the wire lists
# must not change the result.
c4, _ = replay(_WireResult(
    segments=[_WireSeg(6, 0, 2.0, seq=2), _WireSeg(5, 0, 1.0, seq=0)],
    dwells=[_WireDwell(99, 0, seq=1)]))
if [round(seg[2][0], 6) for seg in c4.feed] != [1.0, 2.0]:
    fail("order-segments", "segments replayed in %r, want the seq order [1.0, 2.0]"
         % ([round(seg[2][0], 6) for seg in c4.feed],))
if round(c4.dwells[0][2], 6) != 1.0:
    fail("order-dwell-reordered", "dwell at x=%r after reordering the wire lists"
         % (round(c4.dwells[0][2], 6),))
ok("replay-order-independent-of-wire-list-order")
