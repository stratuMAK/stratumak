#!/usr/bin/env python3
"""Multi-instance client contract, against a real two-task gomc-server.

The bug this exists to catch: a UI client that names its peers by convention or
by hardcoded default. Those names are right whenever there is exactly one of
everything, so every other test in this suite passes with them, and a
multi-instance config answers stat.tool_table with a 404 ten times a second.

Everything here is asserted per instance and cross-checked, because the failure
mode is not "no answer" — it is "the OTHER machine's answer".
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__)))

import gmi  # noqa: E402


def ok(name):
    print("PASS", name)
    sys.stdout.flush()


def fail(name, detail):
    print("FAIL", name, detail)
    sys.stdout.flush()
    sys.exit(1)


def use(instance):
    """Point the client at one of the two tasks."""
    os.environ["GMC_INSTANCE"] = instance
    # The description is cached per instance; drop it so each side is fetched
    # fresh rather than inheriting the previous side's answer.
    gmi.reset_info()


# What multi.hal wires on each side, and nothing else. Asymmetric on purpose:
# if a capability were read from the wrong instance, or from unqualified pin
# names that match nothing, these would not both hold.
WIRED = {
    "left.task": {
        "spindle_forward": True, "spindle_reverse": False,
        "spindle_on": False, "spindle_speed": True, "spindle_brake": False,
        "limit_switch_override": False,
        "coolant_mist": True, "coolant_flood": False,
    },
    "right.task": {
        "spindle_forward": False, "spindle_reverse": True,
        "spindle_on": False, "spindle_speed": False, "spindle_brake": False,
        "limit_switch_override": True,
        "coolant_mist": False, "coolant_flood": True,
    },
}


def check_peers(side):
    use(side)
    peers = gmi.info().peers
    # Note the preview name is NOT f"{side}-preview" — see multi.hal.
    want = {"tooltable": f"{side.split('.')[0]}.tt",
            "preview": f"{side.split('.')[0]}.preview",
            "manualtoolchange": "", "pyvcp": ""}
    got = {k: getattr(peers, k) for k in want}
    if got != want:
        fail(f"peers-{side}", f"{got} != {want}")
    # And the resolver a client actually calls agrees with the raw answer.
    if gmi.preview_instance() != want["preview"]:
        fail(f"peers-{side}", f"preview_instance()={gmi.preview_instance()}")
    if gmi.tooltable_instance() != want["tooltable"]:
        fail(f"peers-{side}", f"tooltable_instance()={gmi.tooltable_instance()}")
    # No pyvcp is loaded here, so pyvcp_instance() must be "" — NOT a fabricated
    # "pyvcp" default. That empty string is what AXIS gates the panel on; a
    # default would request a panel HAL never loaded and 404.
    if gmi.pyvcp_instance() != "":
        fail(f"peers-{side}", f"pyvcp_instance()={gmi.pyvcp_instance()!r}, want ''")
    ok(f"peers-{side}")


def check_caps(side):
    use(side)
    caps = gmi.info().caps
    got = {k: getattr(caps, k) for k in WIRED[side]}
    if got != WIRED[side]:
        wrong = {k: (got[k], WIRED[side][k]) for k in got if got[k] != WIRED[side][k]}
        fail(f"caps-{side}", f"got/want {wrong}")
    ok(f"caps-{side}")


def check_tool_table(side, toolno, diameter):
    """Write a tool through this task, read it back through the slot store whose
    name only /info knows. This is the exact path that 404'd."""
    use(side)
    gmi.ToolTable().put(toolno, {
        "pocketno": 1, "diameter": diameter, "z_offset": -1.0,
        "x_offset": 0.0, "y_offset": 0.0, "a_offset": 0.0, "b_offset": 0.0,
        "c_offset": 0.0, "u_offset": 0.0, "v_offset": 0.0, "w_offset": 0.0,
        "frontangle": 0.0, "backangle": 0.0, "orientation": 0, "comment": side,
    })

    s = gmi.Stat()
    s.poll()
    found = [e for e in s.tool_table if e.id == toolno]
    if not found:
        fail(f"tool-table-{side}",
             f"tool {toolno} absent from stat.tool_table "
             f"(ids={[e.id for e in s.tool_table]})")
    if abs(found[0].diameter - diameter) > 1e-9:
        fail(f"tool-table-{side}", f"diameter={found[0].diameter}, want {diameter}")
    ok(f"tool-table-{side}")


def check_command_reaches_instance(side):
    """A command must post to THIS task, not to the "milltask" default baked
    into gmi.command.Command. AXIS builds its command object from that class
    directly (a subclass), bypassing the gmi.Command() factory that applies the
    instance — so a command posted to the wrong instance 404s on a server that
    has no "milltask". The stat path followed the instance and hid this; only
    issuing a command exposes it.
    """
    use(side)
    from gmi.command import Command
    c = Command(instance=gmi.instance())
    # state() is accepted in any mode; a wrong instance 404s before mode matters.
    try:
        c.state(gmi.STATE_ESTOP)
    except Exception as e:
        fail(f"command-{side}", f"state() to {side} failed: {e}")
    ok(f"command-{side}")


def check_tables_are_separate():
    """Each task has its own store: neither side may see the other's tool."""
    use("left.task")
    left_ids = {e.id for e in gmi.Stat().tool_table}
    use("right.task")
    right_ids = {e.id for e in gmi.Stat().tool_table}
    if 11 not in left_ids or 22 not in right_ids:
        fail("tool-tables-separate", f"left={left_ids} right={right_ids}")
    if 22 in left_ids or 11 in right_ids:
        fail("tool-tables-separate",
             f"tool tables bled across instances: left={left_ids} right={right_ids}")
    ok("tool-tables-separate")


def check_config_is_a_reproducer():
    """Guard the guard: this config must actually be able to expose the bug.

    If a "tooltable" instance existed here, a client that hardcoded that name
    would pass anyway and the whole test would be theatre.
    """
    import urllib.error
    import urllib.request
    for name in ("tooltable", "milltask", "ngcpreview"):
        url = f"{gmi.rest_url()}/api/v1/{name}/"
        try:
            urllib.request.urlopen(url, timeout=5)
        except urllib.error.HTTPError as e:
            if e.code == 404:
                continue
            fail("config-is-a-reproducer", f"{name}: HTTP {e.code}")
        except Exception as e:
            fail("config-is-a-reproducer", f"{name}: {e}")
        else:
            fail("config-is-a-reproducer",
                 f"a single-instance default name ({name}) resolves here, so "
                 f"this config cannot catch a client that hardcodes it")
    ok("config-is-a-reproducer")


def main():
    check_config_is_a_reproducer()
    for side in ("left.task", "right.task"):
        check_peers(side)
        check_caps(side)
        check_command_reaches_instance(side)
    check_tool_table("left.task", 11, 6.0)
    check_tool_table("right.task", 22, 12.0)
    check_tables_are_separate()
    print("ALL OK")


if __name__ == "__main__":
    main()
