"""PyVCP widgets for REST/WebSocket mode — widget-centric protocol.

Each widget receives a PyVCPClient instance. Server state is pushed
via on_change callbacks; user interactions send widget events to server.

No update() method. No polling loop. Server is source of truth.
Server owns clamping, quantization, and pin derivation.
"""

import math
import sys
import tkinter as Tkinter
from tkinter import *

import bwidget

from pyvcp_client import PyVCPClient, EV_PRESS, EV_RELEASE, EV_TOGGLE, \
    EV_SELECT, EV_SET, EV_INCREMENT


# ============================================================
# Display widgets (IN pins only — server → widget, no events sent)
# ============================================================

class pyvcp_led(Canvas):
    n = 0

    def __init__(self, master, client, halpin=None, size=20, on_color="green",
                 off_color="red", disable_pin=False, **kw):
        Canvas.__init__(self, master, width=size, height=size, **kw)
        self.on_color = on_color
        self.off_color = off_color
        pad = max(1, int(size * 0.1))
        self.led = self.create_oval(pad, pad, size - pad, size - pad)
        self.itemconfig(self.led, fill=off_color)
        if halpin is None:
            halpin = "led." + str(pyvcp_led.n)
            pyvcp_led.n += 1
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        if state.get("state"):
            self.itemconfig(self.led, fill=self.on_color)
        else:
            self.itemconfig(self.led, fill=self.off_color)


class pyvcp_rectled(Canvas):
    n = 0

    def __init__(self, master, client, halpin=None, width=30, height=20,
                 on_color="green", off_color="red", disable_pin=False, **kw):
        Canvas.__init__(self, master, width=width, height=height, **kw)
        self.on_color = on_color
        self.off_color = off_color
        self.led = self.create_rectangle(1, 1, width - 1, height - 1)
        self.itemconfig(self.led, fill=off_color)
        if halpin is None:
            halpin = "led." + str(pyvcp_led.n)
            pyvcp_led.n += 1
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        if state.get("state"):
            self.itemconfig(self.led, fill=self.on_color)
        else:
            self.itemconfig(self.led, fill=self.off_color)


class pyvcp_number(Label):
    n = 0

    def __init__(self, master, client, halpin=None, format="2.1f", **kw):
        self.v = StringVar()
        Label.__init__(self, master, textvariable=self.v, **kw)
        if halpin is None:
            halpin = "number." + str(pyvcp_number.n)
            pyvcp_number.n += 1
        self.format_str = "%" + format
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        val = state.get("value", 0)
        try:
            self.v.set(self.format_str % val)
        except (TypeError, ValueError):
            self.v.set(str(val))


class pyvcp_u32(Label):
    n = 0

    def __init__(self, master, client, halpin=None, format="d", **kw):
        self.v = StringVar()
        Label.__init__(self, master, textvariable=self.v, **kw)
        if halpin is None:
            halpin = "number." + str(pyvcp_number.n)
            pyvcp_number.n += 1
        self.format_str = "%" + format
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        val = state.get("value")
        if val is None:
            return
        self.v.set(self.format_str % int(val))


class pyvcp_s32(Label):
    n = 0

    def __init__(self, master, client, halpin=None, format="d", **kw):
        self.v = StringVar()
        Label.__init__(self, master, textvariable=self.v, **kw)
        if halpin is None:
            halpin = "number." + str(pyvcp_number.n)
            pyvcp_number.n += 1
        self.format_str = "%" + format
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        val = state.get("value")
        if val is None:
            return
        self.v.set(self.format_str % int(val))


class pyvcp_timer(Label):
    n = 0

    def __init__(self, master, client, halpin=None, **kw):
        self.v = StringVar()
        Label.__init__(self, master, textvariable=self.v, **kw)
        if halpin is None:
            halpin = "timer." + str(pyvcp_timer.n)
            pyvcp_timer.n += 1
        self.v.set("00:00:00")
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        # Server is authoritative: `value` is the elapsed time in seconds.
        # `state` (run) and `reset` are advisory only. Just format the value.
        total = state.get("value")
        if total is None:
            return
        hr = int(total / 3600)
        remainder = total - hr * 3600
        mn = int(remainder / 60)
        sec = int(remainder - mn * 60)
        self.v.set("%02d:%02d:%02d" % (hr, mn, sec))


class pyvcp_bar(Canvas):
    n = 0

    def __init__(self, master, client, halpin=None, min_=0, max_=100,
                 fillcolor="green", bgcolor="grey", range1=None, range2=None,
                 range3=None, format="3.1f", **kw):
        height = kw.pop('height', 30)
        width = kw.pop('width', 200)
        pad = 25
        cw = width + 2 * pad
        ch = height + 20
        Canvas.__init__(self, master, width=cw, height=ch, **kw)
        self.startval = min_
        self.endval = max_
        self.pad = pad
        self.bw = width
        self.bh = height
        self.format = "%" + format
        self.fillcolor = fillcolor
        self.ranges = []
        for r in (range1, range2, range3):
            if r is not None:
                self.ranges.append(r)

        # border / background
        self.create_rectangle(pad, 1, pad + width, height, fill=bgcolor)
        # the bar
        self.bar = self.create_rectangle(pad, 2, pad, height - 1, fill=fillcolor)
        # start/end labels
        self.create_text(pad, height + 10, text=str(min_))
        self.create_text(pad + width, height + 10, text=str(max_))
        # value text
        self.val_text = self.create_text(pad + width / 2, height / 2, text="")

        self.value = min_
        if halpin is None:
            halpin = "bar." + str(pyvcp_bar.n)
            pyvcp_bar.n += 1
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        self.value = state.get("value", self.startval)
        # determine fill color from ranges
        color = self.fillcolor
        for r in self.ranges:
            if r[0] <= self.value <= r[1]:
                color = r[2]
                break
        # compute bar end pixel (guard a zero span, e.g. min_ == max_)
        span = self.endval - self.startval
        scale = self.bw / span if span else 0
        bar_end = self.pad + scale * (self.value - self.startval)
        bar_end = max(self.pad, min(self.pad + self.bw, bar_end))
        self.coords(self.bar, self.pad, 2, bar_end, self.bh - 1)
        self.itemconfig(self.bar, fill=color)
        self.itemconfig(self.val_text, text=self.format % self.value)


class pyvcp_meter(Canvas):
    n = 0

    def __init__(self, master, client, halpin=None, size=200, text=None,
                 subtext=None, min_=0, max_=100, majorscale=None,
                 minorscale=None, region1=None, region2=None, region3=None, **kw):
        self.size = size
        self.pad = 10
        Canvas.__init__(self, master, width=size, height=size)
        self.min_ = min_
        self.max_ = max_
        range_ = 2.5
        self.min_alfa = -math.pi / 2 - range_
        self.max_alfa = -math.pi / 2 + range_
        self.circle = self.create_oval(self.pad, self.pad, size - self.pad, size - self.pad, width=2)
        self.itemconfig(self.circle, fill="white")
        self.mid = size / 2
        self.r = (size - 2 * self.pad) / 2
        if minorscale is None:
            self.minorscale = 0
        else:
            self.minorscale = minorscale
        if majorscale is None:
            self.majorscale = float((self.max_ - self.min_) / 10)
        else:
            self.majorscale = majorscale
        if text is not None:
            self.create_text([self.mid, self.mid - size / 12],
                             font="Arial %d bold" % (size / 10), text=text)
        if subtext is not None:
            self.create_text([self.mid, self.mid + size / 12],
                             font="Arial %d" % (size / 30 + 5), text=subtext)
        if region1 is not None:
            self._draw_region(region1)
        if region2 is not None:
            self._draw_region(region2)
        if region3 is not None:
            self._draw_region(region3)
        self._draw_ticks()
        self.line = self.create_line(
            [self.mid, self.mid, self.mid + self.r, self.mid],
            fill="red", arrow="last", arrowshape=(0.9 * self.r, self.r, self.r / 20))
        self.itemconfig(self.line, width=3)

        if halpin is None:
            halpin = "meter." + str(pyvcp_meter.n)
            pyvcp_meter.n += 1
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        val = state.get("value")
        if val is None:
            return
        alfa = self._value2angle(val)
        x = self.mid + 0.8 * self.r * math.cos(alfa)
        y = self.mid + 0.8 * self.r * math.sin(alfa)
        self.coords(self.line, self.mid, self.mid, x, y)

    def _value2angle(self, value):
        scale = (self.max_ - self.min_) / (self.max_alfa - self.min_alfa)
        alfa = self.min_alfa + (value - self.min_) / scale
        return max(self.min_alfa, min(self.max_alfa, alfa))

    def _draw_region(self, region):
        start, end, color = region
        start_a = -math.degrees(self._value2angle(start))
        end_a = -math.degrees(self._value2angle(end))
        extent = end_a - start_a
        halfwidth = math.floor(0.1 * self.r / 2) + 1
        xy = (self.pad + halfwidth, self.pad + halfwidth,
              self.size - self.pad - halfwidth, self.size - self.pad - halfwidth)
        self.create_arc(xy, start=start_a, extent=extent, outline=color,
                        width=(halfwidth - 1) * 2, style="arc")

    def _draw_ticks(self):
        value = self.min_
        while value <= self.max_:
            alfa = self._value2angle(value)
            x1, y1 = self.mid + self.r * math.cos(alfa), self.mid + self.r * math.sin(alfa)
            x2, y2 = self.mid + 0.85 * self.r * math.cos(alfa), self.mid + 0.85 * self.r * math.sin(alfa)
            xt, yt = self.mid + 0.75 * self.r * math.cos(alfa), self.mid + 0.75 * self.r * math.sin(alfa)
            self.create_text(xt, yt, font="Arial %d" % (self.size / 30 + 5), text="%g" % value)
            self.create_line(x1, y1, x2, y2, width=2)
            value += self.majorscale
        if self.minorscale > 0:
            value = self.min_
            while value <= self.max_:
                if (value % self.majorscale) != 0:
                    alfa = self._value2angle(value)
                    x1, y1 = self.mid + self.r * math.cos(alfa), self.mid + self.r * math.sin(alfa)
                    x2, y2 = self.mid + 0.9 * self.r * math.cos(alfa), self.mid + 0.9 * self.r * math.sin(alfa)
                    self.create_line(x1, y1, x2, y2)
                value += self.minorscale


class pyvcp_multilabel(Label):
    n = 0

    def __init__(self, master, client, halpin=None, disable_pin=False,
                 legends=None, initval=0, **kw):
        Label.__init__(self, master, **kw)
        if halpin is None:
            halpin = "multilabel." + str(pyvcp_multilabel.n)
            pyvcp_multilabel.n += 1
        self.legends = legends if legends is not None else []
        self.wid = halpin
        if 0 <= initval < len(self.legends):
            Label.config(self, text=self.legends[initval])
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        idx = int(state.get("index", 0))
        if 0 <= idx < len(self.legends):
            Label.config(self, text=self.legends[idx])


class pyvcp_label(Label):
    n = 0

    def __init__(self, master, client, halpin=None, disable_pin=False, **kw):
        Label.__init__(self, master, **kw)
        if disable_pin:
            if halpin is None:
                halpin = "label." + str(pyvcp_label.n)
                pyvcp_label.n += 1
            self.wid = halpin
            client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        disabled = state.get("disabled", False)
        Label.config(self, state=DISABLED if disabled else NORMAL)


class _pyvcp_image(Label):
    def __init__(self, master, client, images, halpin=None, **kw):
        Label.__init__(self, master, **kw)
        if isinstance(images, str):
            images = images.split()
        self.images = images
        if halpin is None:
            halpin = "number." + str(pyvcp_number.n)
            pyvcp_number.n += 1
        self.wid = halpin
        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        idx = state.get("index")
        if idx is None:
            return
        idx = int(idx)
        try:
            self.configure(image=self.images[idx])
        except (IndexError, KeyError):
            print("Unknown image #%d on %s" % (idx, self.wid), file=sys.stderr)


class pyvcp_image_bit(_pyvcp_image):
    pass


class pyvcp_image_u32(_pyvcp_image):
    pass


# ============================================================
# Control widgets (events sent to server on user interaction)
# ============================================================

class pyvcp_button(Button):
    n = 0

    def __init__(self, master, client, halpin=None, disable_pin=False, **kw):
        Button.__init__(self, master, **kw)
        if halpin is None:
            halpin = "button." + str(pyvcp_button.n)
            pyvcp_button.n += 1
        self.wid = halpin
        self.client = client
        self._disabled = False
        self.bind("<ButtonPress>", self._pressed)
        self.bind("<ButtonRelease>", self._released)
        if disable_pin:
            client.on_change(halpin, self._on_state)

    def _pressed(self, event):
        if not self._disabled:
            self.client.send_event(self.wid, EV_PRESS)

    def _released(self, event):
        if not self._disabled:
            self.client.send_event(self.wid, EV_RELEASE)

    def _on_state(self, state):
        self._disabled = state.get("disabled", False)
        Button.config(self, state=DISABLED if self._disabled else NORMAL)


class pyvcp_checkbutton(Checkbutton):
    n = 0

    def __init__(self, master, client, halpin=None, initval=0, **kw):
        self.v = BooleanVar(master)
        Checkbutton.__init__(self, master, variable=self.v, onvalue=1,
                             offvalue=0, command=self._on_toggle, **kw)
        if halpin is None:
            halpin = "checkbutton." + str(pyvcp_checkbutton.n)
            pyvcp_checkbutton.n += 1
        self.wid = halpin
        self.client = client
        self._from_server = False
        client.on_change(halpin, self._on_state)

    def _on_toggle(self):
        """User clicked the checkbox."""
        if not self._from_server:
            self.client.send_event(self.wid, EV_TOGGLE)

    def _on_state(self, state):
        """Server pushed the widget state."""
        self._from_server = True
        self.v.set(state.get("state", False))
        self._from_server = False


class pyvcp_radiobutton(Frame):
    n = 0

    def __init__(self, master, client, halpin=None, initval=0, orient=None,
                 choices=None, **kw):
        Frame.__init__(self, master, bd=2, relief=GROOVE)
        self.client = client
        self.v = IntVar()
        self.v.set(1)
        self.choices = choices if choices is not None else []
        choices = self.choices
        side = 'left' if orient else 'top'

        if halpin is None:
            halpin = "radiobutton." + str(pyvcp_radiobutton.n)
            pyvcp_radiobutton.n += 1

        self.wid = halpin
        self._from_server = False
        for i, c in enumerate(choices):
            b = Radiobutton(self, text=str(c), variable=self.v,
                            value=pow(2, i), command=self._on_select)
            b.pack(side=side)
            if i == initval:
                b.select()

        client.on_change(halpin, self._on_state)

    def _on_select(self):
        """User clicked a radio button."""
        if self._from_server:
            return
        index = int(math.log(self.v.get(), 2))
        self.client.send_event(self.wid, EV_SELECT, index=index)

    def _on_state(self, state):
        """Server pushed the widget state."""
        idx = int(state.get("index", 0))
        if 0 <= idx < len(self.choices):
            self._from_server = True
            self.v.set(pow(2, idx))
            self._from_server = False


class pyvcp_scale(Scale):
    n = 0

    def __init__(self, master, client, resolution=1, halpin=None,
                 halparam=None, min_=0, max_=10, initval=0, param_pin=0, **kw):
        self.resolution = resolution
        Scale.__init__(self, master, resolution=self.resolution,
                       from_=min_, to=max_, command=self._on_change, **kw)
        if halpin is None:
            halpin = "scale." + str(pyvcp_scale.n)
        pyvcp_scale.n += 1
        self.wid = halpin
        self.client = client
        self._from_server = False

        # Listen for server state.
        client.on_change(halpin, self._on_state)

        self.bind('<Button-4>', self._wheel_up)
        self.bind('<Button-5>', self._wheel_down)

    def _on_change(self, value):
        """User dragged the slider."""
        if self._from_server:
            return
        self.client.send_event(self.wid, EV_SET, value=float(value))

    def _on_state(self, state):
        """Server pushed the widget state."""
        val = state.get("value")
        if val is not None and not math.isnan(val):
            self._from_server = True
            self.set(val)
            self._from_server = False

    def _wheel_up(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=1)

    def _wheel_down(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=-1)


class pyvcp_spinbox(Spinbox):
    n = 0

    def __init__(self, master, client, halpin=None, halparam=None, param_pin=0,
                 min_=0, max_=100, initval=0, resolution=1, format="2.1f", **kw):
        self.v = DoubleVar()
        self._resolution = resolution
        if 'increment' not in kw:
            kw['increment'] = resolution
        if 'from' not in kw:
            kw['from'] = min_
        if 'to' not in kw:
            kw['to'] = max_
        if 'format' not in kw:
            kw['format'] = "%" + format
        Spinbox.__init__(self, master, textvariable=self.v,
                         command=self._on_change, **kw)

        if halpin is None:
            halpin = "spinbox." + str(pyvcp_spinbox.n)
        pyvcp_spinbox.n += 1
        self.wid = halpin
        self.client = client
        self._from_server = False

        client.on_change(halpin, self._on_state)

        self.bind('<Button-4>', self._wheel_up)
        self.bind('<Button-5>', self._wheel_down)
        self.bind('<Return>', self._on_return)

    def _on_change(self):
        if self._from_server:
            return
        try:
            value = self.v.get()
        except (ValueError, TclError):
            # Non-numeric entry text — ignore, send nothing.
            return
        self.client.send_event(self.wid, EV_SET, value=value)

    def _on_return(self, event):
        if self._from_server:
            return
        try:
            value = self.v.get()
        except (ValueError, TclError):
            return
        self.client.send_event(self.wid, EV_SET, value=value)

    def _on_state(self, state):
        val = state.get("value")
        if val is not None and not math.isnan(val):
            self._from_server = True
            # Round to resolution to avoid floating-point display artifacts.
            if self._resolution > 0:
                val = round(val / self._resolution) * self._resolution
            self.v.set(round(val, 10))
            self._from_server = False

    def _wheel_up(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=1)

    def _wheel_down(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=-1)


class pyvcp_dial(Canvas):
    n = 0

    def __init__(self, master, client, halpin=None, halparam=None, param_pin=0,
                 size=200, cpr=40, dialcolor="", edgecolor="", dotcolor="grey",
                 min_=None, max_=None, text=None, initval=0, resolution=0.1,
                 **kw):
        pad = size / 10
        self.funit = resolution
        Canvas.__init__(self, master, width=size, height=size)
        pad2 = pad - size / 15
        self.circle2 = self.create_oval(pad2, pad2, size - pad2, size - pad2, width=3)
        self.itemconfig(self.circle2, fill=edgecolor, activefill=edgecolor)
        self.circle = self.create_oval(pad, pad, size - pad, size - pad)
        self.itemconfig(self.circle, fill=dialcolor, activefill=dialcolor)
        self.mid = size / 2
        self.r = (size - 2 * pad) / 2
        self.alfa = 0
        self.d_alfa = 2 * math.pi / cpr
        self.size = size
        self.dotcolor = dotcolor
        self.cpr = cpr
        # Client-side scale multiplier (double-click to change).
        self._scale_mult = 1

        self.dot = self.create_oval(self._dot_coords())
        self.itemconfig(self.dot, fill=dotcolor, activefill="black")
        self.line = self.create_line(
            self.mid + self.r * math.cos(self.alfa),
            self.mid + self.r * math.sin(self.alfa),
            self.mid + self.r * 1.1 * math.cos(self.alfa),
            self.mid + self.r * 1.1 * math.sin(self.alfa))
        self.itemconfig(self.line, arrow="last", arrowshape=(10, 10, 10), width=10)

        self.txtroom = int(size / 6)
        if text is not None:
            self.create_text([self.mid, self.mid - self.txtroom],
                             text=text, font=('Arial', -self.txtroom))
        self.dro = self.create_text([self.mid, self.mid], font=('Arial', -self.txtroom))
        self.delta_text = self.create_text([self.mid, self.mid + self.txtroom],
                                           text='x ' + str(self.funit),
                                           font=('Arial', -self.txtroom))

        self.bind('<Button-4>', self._wheel_up)
        self.bind('<Button-5>', self._wheel_down)
        self.bind('<Button1-Motion>', self._motion)
        self.bind('<ButtonPress>', self._bdown)
        self.bind('<Double-1>', self._scale_dn)
        self.bind('<Double-2>', self._scale_reset)
        self.bind('<Double-3>', self._scale_up)
        self._draw_ticks(cpr)
        self.dragstart = 0

        if halpin is None:
            halpin = "dial." + str(pyvcp_dial.n)
        pyvcp_dial.n += 1
        self.wid = halpin
        self.client = client
        self.current_value = initval

        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        """Server pushed the widget state."""
        val = state.get("value")
        if val is not None and not math.isnan(val):
            self.current_value = val
            # Update angle from value.
            if self.funit > 0:
                counts = int(round(val / self.funit))
                self.alfa = counts * self.d_alfa
            self._update_dot()
            self._update_dro()

    def _wheel_up(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=self._scale_mult)

    def _wheel_down(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=-self._scale_mult)

    def _bdown(self, event):
        self.dragstart = math.atan2(event.y - self.mid, event.x - self.mid)
        self.itemconfig(self.dot, fill="black", activefill="black")

    def _motion(self, event):
        dragstop = math.atan2(event.y - self.mid, event.x - self.mid)
        delta = dragstop - self.dragstart
        if delta >= self.d_alfa:
            self.client.send_event(self.wid, EV_INCREMENT, increment=self._scale_mult)
            self.dragstart = math.atan2(event.y - self.mid, event.x - self.mid)
        elif delta <= -self.d_alfa:
            self.client.send_event(self.wid, EV_INCREMENT, increment=-self._scale_mult)
            self.dragstart = math.atan2(event.y - self.mid, event.x - self.mid)
        self.itemconfig(self.dot, fill="black", activefill="black")

    def _scale_dn(self, event):
        self._scale_mult *= 10
        self._update_scale_text()

    def _scale_up(self, event):
        self._scale_mult = max(1, self._scale_mult // 10)
        self._update_scale_text()

    def _scale_reset(self, event):
        self._scale_mult = 1
        self._update_scale_text()

    def _update_scale_text(self):
        effective = self.funit * self._scale_mult
        self.itemconfig(self.delta_text, text='x ' + str(effective))

    def _update_dro(self):
        decimals = max(0, -int(math.floor(math.log10(self.funit)))) if self.funit > 0 else 4
        valtext = "{:.{}f}".format(self.current_value, decimals)
        self.itemconfig(self.dro, text=valtext)

    def _update_dot(self):
        self.coords(self.dot, self._dot_coords())
        self.coords(self.line,
                    self.mid + self.r * math.cos(self.alfa),
                    self.mid + self.r * math.sin(self.alfa),
                    self.mid + self.r * 1.1 * math.cos(self.alfa),
                    self.mid + self.r * 1.1 * math.sin(self.alfa))

    def _dot_coords(self):
        DOTR = 0.04 * self.size
        DOTPOS = 0.85
        midx = self.mid + DOTPOS * self.r * math.cos(self.alfa)
        midy = self.mid + DOTPOS * self.r * math.sin(self.alfa)
        return midx - DOTR, midy - DOTR, midx + DOTR, midy + DOTR

    def _draw_ticks(self, cpr):
        for n in range(0, cpr, 2):
            for i in range(2):
                startx = self.mid + self.r * math.cos((n + i) * self.d_alfa)
                starty = self.mid + self.r * math.sin((n + i) * self.d_alfa)
                length = 1.15 if i == 0 else 1.1
                width = 2 if i == 0 else 1
                stopx = self.mid + length * self.r * math.cos((n + i) * self.d_alfa)
                stopy = self.mid + length * self.r * math.sin((n + i) * self.d_alfa)
                self.create_line(startx, starty, stopx, stopy, width=width)


class pyvcp_jogwheel(Canvas):
    n = 0

    def __init__(self, master, client, halpin=None, text=None, clear_pin=0,
                 scale_pin=0, fillcolor="lightgrey", bgcolor="lightgrey",
                 size=200, cpr=40, **kw):
        pad = size / 10
        self.count = 0
        Canvas.__init__(self, master, width=size, height=size)
        pad2 = pad - size / 15
        self.create_oval(pad2, pad2, size - pad2, size - pad2, width=3)
        self.circle = self.create_oval(pad, pad, size - pad, size - pad)
        self.itemconfig(self.circle, fill=bgcolor, activefill=fillcolor)
        self.mid = size / 2
        self.r = (size - 2 * pad) / 2
        self.alfa = 0
        self.d_alfa = 2 * math.pi / cpr
        self.size = size
        self.client = client

        self.dot = self.create_oval(self._dot_coords())
        self.itemconfig(self.dot, fill="black")
        self.line = self.create_line(
            self.mid + self.r * math.cos(self.alfa),
            self.mid + self.r * math.sin(self.alfa),
            self.mid + self.r * 1.1 * math.cos(self.alfa),
            self.mid + self.r * 1.1 * math.sin(self.alfa))
        self.itemconfig(self.line, arrow="last", arrowshape=(10, 10, 10), width=8)

        self.bind('<Button-4>', self._wheel_up)
        self.bind('<Button-5>', self._wheel_down)
        self.bind('<Button1-Motion>', self._motion)
        self.bind('<ButtonPress>', self._bdown)
        self._draw_ticks(cpr)
        self.dragstart = 0

        if halpin is None:
            halpin = "jogwheel." + str(pyvcp_jogwheel.n)
        pyvcp_jogwheel.n += 1
        self.wid = halpin

        client.on_change(halpin, self._on_state)

    def _on_state(self, state):
        """Server pushed the count value."""
        val = state.get("value")
        if val is not None and not math.isnan(val):
            self.count = int(val)
            # Update dial visual position.
            self.alfa = self.count * self.d_alfa
            self.coords(self.dot, self._dot_coords())
            self.coords(self.line,
                        self.mid + self.r * math.cos(self.alfa),
                        self.mid + self.r * math.sin(self.alfa),
                        self.mid + self.r * 1.1 * math.cos(self.alfa),
                        self.mid + self.r * 1.1 * math.sin(self.alfa))

    def _wheel_up(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=1)

    def _wheel_down(self, event):
        self.client.send_event(self.wid, EV_INCREMENT, increment=-1)

    def _bdown(self, event):
        self.dragstart = math.atan2(event.y - self.mid, event.x - self.mid)

    def _motion(self, event):
        dragstop = math.atan2(event.y - self.mid, event.x - self.mid)
        delta = dragstop - self.dragstart
        if delta >= self.d_alfa:
            self.client.send_event(self.wid, EV_INCREMENT, increment=1)
            self.dragstart = math.atan2(event.y - self.mid, event.x - self.mid)
        elif delta <= -self.d_alfa:
            self.client.send_event(self.wid, EV_INCREMENT, increment=-1)
            self.dragstart = math.atan2(event.y - self.mid, event.x - self.mid)

    def _dot_coords(self):
        DOTR = 0.04 * self.size
        DOTPOS = 0.85
        midx = self.mid + DOTPOS * self.r * math.cos(self.alfa)
        midy = self.mid + DOTPOS * self.r * math.sin(self.alfa)
        return midx - DOTR, midy - DOTR, midx + DOTR, midy + DOTR

    def _draw_ticks(self, cpr):
        for n in range(0, cpr, 2):
            for i in range(2):
                startx = self.mid + self.r * math.cos((n + i) * self.d_alfa)
                starty = self.mid + self.r * math.sin((n + i) * self.d_alfa)
                length = 1.15 if i == 0 else 1.1
                width = 2 if i == 0 else 1
                stopx = self.mid + length * self.r * math.cos((n + i) * self.d_alfa)
                stopy = self.mid + length * self.r * math.sin((n + i) * self.d_alfa)
                self.create_line(startx, starty, stopx, stopy, width=width)


# ============================================================
# Layout widgets (no HAL interaction)
# ============================================================

class pyvcp_vbox(Frame):
    def __init__(self, master, client, bd=0, relief=FLAT, **kw):
        Frame.__init__(self, master, bd=bd, relief=relief)
        self.fill = 'x'
        self.side = 'top'
        self.anchor = 'center'
        self.expand = 'yes'

    def add(self, container, widget):
        if isinstance(widget, pyvcp_boxexpand):
            self.expand = widget.expand
            return
        if isinstance(widget, pyvcp_boxfill):
            self.fill = widget.fill
            return
        if isinstance(widget, pyvcp_boxanchor):
            self.anchor = widget.anchor
            return
        widget.pack(side=self.side, anchor=self.anchor, fill=self.fill,
                    expand=self.expand)


class pyvcp_hbox(Frame):
    def __init__(self, master, client, bd=0, relief=FLAT, **kw):
        Frame.__init__(self, master, bd=bd, relief=relief)
        self.fill = 'y'
        self.side = 'left'
        self.anchor = 'center'
        self.expand = 'yes'

    def add(self, container, widget):
        if isinstance(widget, pyvcp_boxexpand):
            self.expand = widget.expand
            return
        if isinstance(widget, pyvcp_boxfill):
            self.fill = widget.fill
            return
        if isinstance(widget, pyvcp_boxanchor):
            self.anchor = widget.anchor
            return
        widget.pack(side=self.side, anchor=self.anchor, fill=self.fill)


class pyvcp_boxfill:
    def __init__(self, master=None, client=None, fill="x", **kw):
        self.fill = fill


class pyvcp_boxanchor:
    def __init__(self, master=None, client=None, anchor="center", **kw):
        self.anchor = anchor


class pyvcp_boxexpand:
    def __init__(self, master=None, client=None, expand="yes", **kw):
        self.expand = expand


class pyvcp_labelframe(LabelFrame):
    def __init__(self, master, client, **kw):
        LabelFrame.__init__(self, master, **kw)
        self.pack(expand=1, fill=BOTH)

    def add(self, container, widget):
        widget.pack(side="top", fill="both", expand="yes")


class pyvcp_tabs(bwidget.NoteBook):
    def __init__(self, master, client, names=None, **kw):
        self.names = names if names is not None else []
        self.idx = 0
        self._require(master)
        Widget.__init__(self, master, "NoteBook", kw)

    def add(self, container, child):
        child.pack(side="top", fill="both", anchor="ne")
        if self.idx == 1:
            self.raise_page(self.names[0])

    def getcontainer(self):
        if len(self.names) <= self.idx:
            self.names.append("Tab-%d" % self.idx)
        name = self.names[self.idx]
        self.idx += 1
        return self.insert("end", name, text=name)


class pyvcp_table(Frame):
    def __init__(self, master, client, flexible_rows=None, flexible_columns=None,
                 uniform_columns="", uniform_rows="", **kw):
        Frame.__init__(self, master, **kw)
        flexible_rows = flexible_rows if flexible_rows is not None else []
        flexible_columns = flexible_columns if flexible_columns is not None else []
        for r in flexible_rows:
            self.grid_rowconfigure(r, weight=1)
        for c in flexible_columns:
            self.grid_columnconfigure(c, weight=1)
        for i, r in enumerate(uniform_rows):
            self.grid_rowconfigure(i + 1, uniform=r)
        for i, c in enumerate(uniform_columns):
            self.grid_columnconfigure(i + 1, uniform=c)
        self._r = self._c = 0
        self.occupied = {}
        self.span = (1, 1)
        self.sticky = "ne"

    def add(self, container, child):
        if isinstance(child, pyvcp_tablerow):
            self._r += 1
            self._c = 1
            return
        elif isinstance(child, pyvcp_tablespan):
            self.span = child.span
            return
        elif isinstance(child, pyvcp_tablesticky):
            self.sticky = child.sticky
            return
        r, c = self._r, self._c
        while (r, c) in self.occupied:
            c = c + 1
        rs, cs = self.span
        child.grid(row=r, column=c, rowspan=rs, columnspan=cs,
                   sticky=self.sticky)
        for ri in range(r, r + rs):
            for ci in range(c, c + cs):
                self.occupied[ri, ci] = True
        self.span = 1, 1
        self._c = c + cs


class pyvcp_tablerow:
    def __init__(self, master=None, client=None, **kw):
        pass


class pyvcp_tablespan:
    def __init__(self, master=None, client=None, rows=1, columns=1, **kw):
        self.span = (rows, columns)


class pyvcp_tablesticky:
    def __init__(self, master=None, client=None, sticky="ne", **kw):
        self.sticky = sticky


class pyvcp_include(Frame):
    def __init__(self, master, client, **kw):
        Frame.__init__(self, master, **kw)

    def add(self, container, widget):
        widget.pack(side="top", fill="both", expand="yes")


# ============================================================
# Display-only / dummy widgets (no HAL interaction, no packing)
# ============================================================

class _pyvcp_dummy:
    def add(self, container, widget):
        pass

    def pack(self, *args, **kw):
        pass


class pyvcp_title(_pyvcp_dummy):
    def __init__(self, master, client, title, iconname=None):
        master.wm_title(title)
        if iconname:
            master.wm_iconname(iconname)


class pyvcp_axisoptions(_pyvcp_dummy):
    def __init__(self, master, client):
        import rs274.options
        rs274.options.install(master)


class pyvcp_option(_pyvcp_dummy):
    def __init__(self, master, client, pattern, value, priority=None):
        master.option_add(pattern, value, priority)


class pyvcp_image(_pyvcp_dummy):
    all_images = {}

    def __init__(self, master, client, name, **kw):
        self.all_images[name] = PhotoImage(name, kw, master)


# Build elements list for vcpparse.
elements = []
__all__ = []
for _key in list(globals().keys()):
    if _key.startswith("pyvcp_"):
        elements.append(_key[6:])
        __all__.append(_key)
