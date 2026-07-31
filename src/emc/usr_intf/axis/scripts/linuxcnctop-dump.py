#!/usr/bin/env python3
#    Headless status dump — the text half of the former Tk linuxcnctop.
#
#    The GUI ("Show LinuxCNC Status" in the AXIS Machine menu) is now the
#    linuxcnctop web app; this keeps `linuxcnctop -t` available for scripts,
#    bug reports and machines without a display.
#
#    Two output shapes, because they answer different questions:
#      (default) the classic 2.9-named flat listing, via the gmi.Stat shim —
#                the same names linuxcnc.stat() always exposed;
#      --json    the raw emcstat StatFull snapshot off the REST API, whose
#                field paths are the ones the web app displays.
#
#    This is a component of AXIS, a front-end for linuxcnc
#    Copyright 2004, 2005, 2006 Jeff Epler <jepler@unpythonic.net>
#                         and Chris Radek <chris@timeguy.com>
#    Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
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

import argparse
import json
import sys
import time
import urllib.request

import gmi
from gmi.constants import *
from gmi.stat import Stat

# Bound in main() once the target instance is known.
s = None


def show_spindles(l):
    ct = 0; out = ""
    for d in l:
        for key in d:
            out = out + "%d %20s %s\n" % (ct, key, d[key])
        ct = ct + 1
    return out


def show_mcodes(l):
    return " ".join(["M%g" % i for i in l[1:] if i != -1])


def show_gcodes(l):
    return " ".join(["G%g" % (i / 10.) for i in l[1:] if i != -1])


def show_position(p):
    return " ".join(["%-8.4f" % n for i, n in enumerate(p) if s.axis_mask & (1 << i)])


joint_position = None
def show_joint_position(p):
    global joint_position
    if joint_position is None:
        joint_position = " ".join(["%-8.4f"] * s.joints) if s.joints else ""
    return joint_position % p[:s.joints] if s.joints else ""


perjoint = None
def show_perjoint(p):
    global perjoint
    if perjoint is None:
        perjoint = " ".join(["%s"] * s.joints) if s.joints else ""
    return perjoint % p[:s.joints] if s.joints else ""


def show_float(p): return "%-8.4f" % p


def show_floats(v): return " ".join(show_float(p) for p in v)


def show_ints(v): return " ".join(str(bool(p)) for p in v)


# Per-attribute renderers. A None entry suppresses the attribute entirely:
# those are gmi.Stat's methods and helpers, not machine state (rendering a
# bound method is how the old GUI grew a garbage "stop" row).
maps = {
'exec_state': {TASK_EXEC_ERROR: 'error',
                TASK_EXEC_DONE: 'done',
                TASK_EXEC_WAITING_FOR_MOTION: 'motion',
                TASK_EXEC_WAITING_FOR_MOTION_QUEUE: 'motion queue',
                TASK_EXEC_WAITING_FOR_IO: 'io',
                TASK_EXEC_WAITING_FOR_MOTION_AND_IO: 'motion and io',
                TASK_EXEC_WAITING_FOR_DELAY: 'delay',
                TASK_EXEC_WAITING_FOR_MCODE_HANDLER: 'M-code handler',
                TASK_EXEC_WAITING_FOR_SPINDLE_ORIENTED: 'spindle orient'},
'motion_mode':{TRAJ_MODE_FREE: 'free', TRAJ_MODE_COORD: 'coord',
                TRAJ_MODE_TELEOP: 'teleop'},
'interp_state':{INTERP_IDLE: 'idle', INTERP_PAUSED: 'paused',
                INTERP_READING: 'reading', INTERP_WAITING: 'waiting'},
'task_state':  {STATE_ESTOP: 'estop', STATE_ESTOP_RESET: 'estop reset',
                STATE_ON: 'on', STATE_OFF: 'off'},
'task_mode':   {MODE_AUTO: 'auto', MODE_MDI: 'mdi',
                MODE_MANUAL: 'manual'},
'state':       {1: 'rcs_done', 2: 'rcs_exec', 3: 'rcs_error'},
'motion_type': {0: 'none', 1: 'traverse', 2: 'feed', 3: 'arc', 4: 'toolchange', 5: 'probing'},
'program_units': {1: 'inch', 2: 'mm'},
'kinematics_type': {KINEMATICS_IDENTITY: 'identity', KINEMATICS_FORWARD_ONLY: 'forward_only',
                    KINEMATICS_INVERSE_ONLY: 'inverse_only', KINEMATICS_BOTH: 'both'},
'mcodes': show_mcodes, 'gcodes': show_gcodes, 'poll': None, 'tool_table': None,
'stop': None, 'machine_units': None, 'invalidate_tool_table': None,
'spindle':show_spindles,
'axis': None, 'joint': None, 'gettaskfile': None,
'actual_position': show_position,
'position': show_position,
'dtg': show_position,
'origin': show_position,
'rotation_xy': show_float,
'probed_position': show_position,
'tool_offset': show_position,
'g5x_offset': show_position,
'g92_offset': show_position,
'linear_units': show_float,
'max_acceleration': show_float,
'max_velocity': show_float,
'angular_units': show_float,
'distance_to_go': show_float,
'current_vel': show_float,
'ain': show_floats,
'aout': show_floats,
'din': show_ints,
'dout': show_ints,
'settings': show_floats,
'limit': show_perjoint,
'homed': show_perjoint,
'joint_position': show_joint_position,
'joint_actual_position': show_joint_position,
}


class Unreachable(Exception):
    """The server could not be reached or refused the request."""


def init_maps():
    # On an identity-kinematics machine the joint positions duplicate the
    # world positions, so the GUI never showed them; keep that here.
    if s.kinematics_type == KINEMATICS_IDENTITY:
        maps['joint_position'] = None
        maps['joint_actual_position'] = None


def rows():
    for k in dir(s):
        if k.startswith("_"):
            continue
        if k in maps and maps[k] is None:
            continue
        v = getattr(s, k)
        if k in maps:
            m = maps[k]
            v = m(v) if callable(m) else m.get(v, v)
        yield k, v


def dump_text(truncate):
    # poll() never raises — on a failed fetch it keeps the previous snapshot and
    # clears .connected. A dump must not print a stale (or all-zero) machine as
    # if it were live, so check before rendering.
    s.poll()
    if not s.connected:
        raise Unreachable("cannot reach %s" % gmi.rest_url())
    init_maps()
    for k, v in rows():
        if truncate:
            print("%-20s %-.58s" % (k, v))
        else:
            print("%-20s %s" % (k, v))


def dump_json(instance):
    # The raw StatFull snapshot, exactly what the web app receives on its watch
    # channel. Fetched directly rather than through gmi.Stat, whose whole job is
    # to reshape this into the flat 2.9 names the text dump prints.
    url = "%s/api/v1/%s/stat" % (gmi.rest_url(), instance)
    try:
        with urllib.request.urlopen(url, timeout=5) as resp:
            data = json.loads(resp.read())
    except Exception as e:
        raise Unreachable("GET %s: %s" % (url, e))
    print(json.dumps(data, indent=2))


def main():
    global s

    p = argparse.ArgumentParser(
        description="Print the LinuxCNC machine status once, or repeatedly.")
    p.add_argument("--json", action="store_true",
                   help="dump the raw emcstat StatFull snapshot (web-app field paths)")
    p.add_argument("-n", "--interval", type=float, metavar="SEC",
                   help="repeat every SEC seconds until interrupted")
    p.add_argument("--full", action="store_true",
                   help="do not truncate values to 58 columns (text mode)")
    p.add_argument("--instance", default=None,
                   help="task instance to query (default: $GMC_INSTANCE or milltask)")
    # `-t` was the GUI's text-mode switch; accept it so old invocations and
    # scripts keep working, but it is the default here.
    p.add_argument("-t", action="store_true", help=argparse.SUPPRESS)
    # -ini FILE was passed by AXIS; harmless and ignored.
    p.add_argument("-ini", metavar="FILE", help=argparse.SUPPRESS)
    args = p.parse_args()

    instance = gmi.resolve_instance(args.instance)
    s = Stat(instance=instance)

    def once():
        if args.json:
            dump_json(instance)
        else:
            dump_text(not args.full)

    try:
        if args.interval:
            # A repeating dump is a monitor: an outage is reported and waited
            # out, not a reason to exit — the machine may still be starting.
            while True:
                try:
                    once()
                except Unreachable as e:
                    print("linuxcnctop-dump: %s" % e, file=sys.stderr)
                sys.stdout.flush()
                time.sleep(args.interval)
        else:
            once()
    except KeyboardInterrupt:
        pass
    except Unreachable as e:
        print("linuxcnctop-dump: %s" % e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
