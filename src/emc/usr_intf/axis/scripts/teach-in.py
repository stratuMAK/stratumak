#!/usr/bin/env python3
"""Usage:
    teach-in [outputfile]
If outputfile is not specified, writes to standard output.

Each press of "Learn" records one line:
    line-no  position...  flood mist lube spindle

You must ". scripts/rip-environment" before running this script, if you use
run-in-place.

The machine is addressed over the gomc REST API: GMC_REST_URL selects the
server, GMC_INSTANCE the task instance.
"""
#    Copyright 2007 Jeff Epler <jepler@unpythonic.net>
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

import gmi
import sys
import tkinter

linenumber = 1;

if len(sys.argv) > 1:
    outfile = sys.argv[1]
    sys.stdout = open(outfile, 'w')

# Positions in the machine's configured units, as classic linuxcnc.stat()
# reported them and as the recorded lines are meant to be read back as
# coordinates; the gmi wire format is mm.
s = gmi.Stat().machine_units()

def get_cart():
    s.poll()
    position = ""
    for i,a in enumerate("XYZABCUVW"):
        if s.axis_mask & (1<<i):
            position = position + "%-8.4f " % (s.position[i])
    return position[:-1] # remove the final space char

def get_joint():
    s.poll()
    position = " ".join(["%-8.4f"] * s.joints)
    return position % s.joint_actual_position[:s.joints]

def get_position():
    return get_cart() if world.get() else get_joint()

def log():
    global linenumber;
    # A frozen snapshot must not be recorded as a taught position: poll() keeps
    # the last reading when the server is unreachable, so without this check a
    # dead connection silently teaches the same coordinates over and over.
    p = get_position()
    if not s.connected:
        label1.configure(text='Learned:  (not connected)')
        return
    spindle = s.spindle[0]['enabled'] if s.spindle else 0
    label1.configure(text='Learned:  %s' % p)
    # The flags come off the wire as JSON booleans; the recorded line has
    # always been "line-no position... flood mist lube spindle" with 0/1, and
    # something out there parses it.
    print(linenumber, p, int(s.flood), int(s.mist), int(s.lube), int(spindle));
    sys.stdout.flush()
    linenumber += 1;

def show():
    p = get_position()
    label2.configure(text='Position: %s' % p if s.connected
                     else 'Position: (not connected)')
    app.after(100, show)

app = tkinter.Tk(); app.wm_title('LinuxCNC Teach-In')

world = tkinter.IntVar(app)

button = tkinter.Button(app, command=log, text='Learn', font=("helvetica", 14))
button.pack(side='left')

label2 = tkinter.Label(app, width=60, font='fixed', anchor="w")
label2.pack(side='top')

label1 = tkinter.Label(app, width=60, font='fixed', text="Learned:  (nothing yet)", anchor="w")
label1.pack(side='top')

r1 = tkinter.Radiobutton(app, text="Joint", variable=world, value=0)
r1.pack(side='left')
r2 = tkinter.Radiobutton(app, text="World", variable=world, value=1)
r2.pack(side='left')

show()
app.mainloop()
