"""
LinuxCNC User Interface helper functions
"""

# Under stratuMAK, linuxcnc.so is a deprecation stub: linuxcnc.command/stat/
# error_channel raise and point callers at gmi. This module used to `import
# linuxcnc` and read its constants off that stub, which only worked because
# every caller first monkey-patched gmi's constants onto the shared module
# object. That made the scaffolding load-bearing: a test that dropped the patch
# broke this file at a distance. Take the constants from gmi directly and stand
# on our own feet.
import gmi
from gmi.constants import (
    EXEC_DONE, INTERP_IDLE, INTERP_PAUSED, INTERP_READING, INTERP_WAITING,
    JOG_CONTINUOUS, JOG_STOP, RCS_DONE, STATE_ESTOP,
)

import sys
import time
import math

import stmak_test


class LinuxCNC_Exception(Exception):
    pass


# How far an axis may drift and still count as "did not move". Sized to swallow
# float noise from sampling a live machine (and stratuMAK's mm -> machine-unit
# scaling) while being far below any real commanded motion.
IDLE_AXIS_EPSILON = 1e-6


class LinuxCNC:
    """
    This class implements the main interface to LinuxCNC.
    """

    def __init__(self, command=None, status=None, error=None):
        """init docs"""
        self.command = command
        self.status = status
        self.error = error

        if not self.command:
            self.command = gmi.Command()

        if not self.status:
            self.status = gmi.Stat()

        if not self.error:
            self.error = gmi.ErrorChannel()


    def _wait(self, pred, desc, timeout, detail=None):
        """Poll pred() until it holds, against a deadline that honours
        STMAK_TEST_TIMEOUT_SCALE; raise LinuxCNC_Exception naming what was
        awaited (and `detail`) when it expires."""
        try:
            return stmak_test.wait_until(pred, desc, timeout=timeout,
                                         interval=0.02, detail=detail)
        except stmak_test.Timeout as e:
            raise LinuxCNC_Exception(str(e))

    def _at_rest(self):
        """Poll status; True when motion reports nothing moving: in position
        (no jog, homing or free-planner move active) with an empty planner
        queue. This is the controller's own answer. Two equal position polls
        were used before, and a status stall of one poll interval on a loaded
        machine made a moving axis look stopped."""
        self.status.poll()
        return self.status.inpos and self.status.queue == 0

    def _pos(self):
        return "position=%s inpos=%s queue=%s" % (
            str(self.status.position), self.status.inpos, self.status.queue)

    def wait_for_linuxcnc_startup(self, timeout=10.0):
        """Poll the Status buffer waiting for it to look initialized,
        rather than just allocated (all-zero).  Returns on success, raises
        LinuxCNC_Exception on timeout."""

        def _ready():
            self.status.poll()
            return not ((self.status.angular_units == 0.0)
                or (self.status.axis_mask == 0)
                or (self.status.cycle_time == 0.0)
                or (self.status.exec_state != EXEC_DONE)
                or (self.status.interp_state != INTERP_IDLE)
                or (self.status.inpos == False)
                or (self.status.linear_units == 0.0)
                or (self.status.max_acceleration == 0.0)
                or (self.status.max_velocity == 0.0)
                or (self.status.program_units == 0.0)
                or (self.status.rapidrate == 0.0)
                or (self.status.state != RCS_DONE)
                or (self.status.task_state != STATE_ESTOP))
        self._wait(_ready, "the status buffer to look initialized", timeout)

    def all_joints_homed(self, joints):
        """Arguments:

            'joints' is an array of booleans, indicating the joints to
            check for homed-ness.

        Returns True if all the specified joints are homed, False if
        any are unhomed."""

        self.status.poll()
        for i in range(0,9):
            if joints[i] and not self.status.homed[i]:
                return False
        return True


    def wait_for_home(self, joints, timeout=10.0):
        """Arguments:

            'joints' is an array of booleans, indicating the joints that
            are homing.

            'timeout' is a float, the number of seconds to wait for
            homing before giving up.

        Returns once all the specified joints are homed and the machine is
        at rest, raises LinuxCNC_Exception if the timeout expired first.
        The `homed` flag flips a few servo cycles before the final move
        converges on the home coordinate, so homed alone could hand a caller
        an axis still creeping to its home value."""

        self._wait(lambda: self.all_joints_homed(joints) and self._at_rest(),
                   "homing to complete", timeout,
                   detail=lambda: "homed=%s %s" % (str(self.status.homed), self._pos()))

    def wait_for_all_axes_to_stop(self, timeout=10.0):
        """Wait until motion reports the machine at rest (see _at_rest).
        Returns on success, raises LinuxCNC_Exception if the timeout expires
        while something is still moving."""

        self._wait(self._at_rest, "the machine to come to rest", timeout,
                   detail=self._pos)

    def wait_for_axis_to_stop(self, axis_letter, timeout=10.0):
        """Arguments:
            'axis_letter' is a single character from the set [xyzabcuvw]
            indicating the axis to wait for stillness on.

            'timeout' is a float, the number of seconds to wait for the
            axis to stop before giving up.

        Returns once motion reports the machine at rest (see _at_rest): the
        controller does not report stillness per axis, and nothing else may
        be moving for the callers' checks anyway.  Raises LinuxCNC_Exception
        if the timeout expires first.
        """

        print("waiting for axis", axis_letter.lower(), "to stop")
        self._wait(self._at_rest, "axis %s to stop" % axis_letter.lower(),
                   timeout, detail=self._pos)

    def jog_axis(self, axis_letter, target, vel=5.0, timeout=10.0):
        """Arguments:

            'axis_letter' is a single character from the set [xyzabcuvw]
            indicating an axis.

            'target' is a float indicating the location to jog to.

            'vel' is a float indicating the jog speed.

            'timeout' is a float, the number of seconds to wait for the
            jog to reach the target before giving up.

        This function uses the linuxcnc.command.jog() function to do
        a continuous jog of the specified axis towards the target, at
        the specified speed.  Stops the jog when the target is reached.
        Will overshoot some.  The function returns after the axis has
        stopped moving.

        If the axis does not reach the target before the timeout, or if
        any other axis moved, raises LinuxCNC_Exeption.
        """

        self.status.poll()
        axis_letter = axis_letter.lower()
        axis_index = 'xyzabcuvw'.index(axis_letter)
        start_pos = self.status.position

        print("jogging axis %s from %.3f to %.3f" % (axis_letter, start_pos[axis_index], target))

        if self.status.position[axis_index] < target:
            vel = abs(vel)
            done = lambda pos: pos > target
        else:
            vel = -1.0 * abs(vel)
            done = lambda pos: pos < target

        # the 0 here means "jog axis, not joint"
        self.command.jog(JOG_CONTINUOUS, 0, axis_index, vel)

        start = time.time()
        timeout *= stmak_test.scale()
        while not done(self.status.position[axis_index]) and ((time.time() - start) < timeout):
            time.sleep(0.1)
            # stratuMAK dead-mans continuous jogs not refreshed within its jog
            # watchdog interval (runaway protection for disconnected
            # clients) — keep the jog alive while we wait.
            self.command.jog(JOG_CONTINUOUS, 0, axis_index, vel)
            self.status.poll()

        # the 0 here means "jog axis, not joint"
        self.command.jog(JOG_STOP, 0, axis_index)

        if not done(self.status.position[axis_index]):
            raise LinuxCNC_Exception("failed to jog axis %s to %.3f\n" % (axis_letter, target) + "timed out at %.3f after %.3f seconds" % (self.status.position[axis_index], timeout))

        print("    jogged axis %d past target %.3f" % (axis_index, target))

        self.wait_for_axis_to_stop(axis_letter)

        success = True
        for i in range(0, 9):
            if i == axis_index:
                continue;
            # Tolerance, not exact equality. "This axis did not move" cannot be
            # an == on a live status feed: the values are floats sampled from a
            # running machine, and under stratuMAK they are also scaled (mm -> machine
            # units) on the way out, so a 1-ULP wobble in the underlying mm value
            # produces a different float here. That made the check fire on axes
            # that had not moved at all -- the failure printed "moved from 0.000
            # to 0.000", which is the tell. A real unwanted move is orders of
            # magnitude larger than this bound.
            if abs(start_pos[i] - self.status.position[i]) > IDLE_AXIS_EPSILON:
                raise LinuxCNC_Exception("axis %s moved from %.6f to %.6f but should not have!" % ('xyzabcuvw'[i], start_pos[i], self.status.position[i]))


    def wait_for_axis_to_stop_at(self, axis_letter, target, timeout=10.0, tolerance = 0.0001):
        """
        Arguments:
            'axis_letter' is a single character from the set [xyzabcuvw]
            indicating the axis to wait for stillness on.

            'target' is a float, where the specified axis is trying to
            move to.

            'timeout' is a float, the number of seconds to wait for the
            axis to stop before giving up.

            'tolerance' is a float, how close to the target the axis
            has to be before we consider it there.

        Waits until the machine is at rest (see _at_rest) with the specified
        axis within `tolerance` of `target`.  If this does not happen before
        the timeout, raises LinuxCNC_Exception.
        """

        axis_index = 'xyzabcuvw'.index(axis_letter.lower())

        def _there():
            return (self._at_rest() and
                    math.fabs(self.status.position[axis_index] - target) < tolerance)
        self._wait(_there, "axis %s to stop at %.3f" % (axis_letter.lower(), target),
                   timeout, detail=self._pos)
        print("axis %s stopped at %.3f" % (axis_letter.lower(), target))

    def wait_for_interp_state(self, target_state, timeout=10.0):
        """
        Arguments:
            'target_state' is the Interpreter state to wait for,
            one of INTERP_IDLE, INTERP_PAUSED,
            INTERP_READING, or INTERP_WAITING.

            'timeout' is a float, the number of seconds to wait for the
            Interpreter to reach the target state.

        This function polls the LinuxCNC Status buffer waiting for the
        specified Interpreter state.  If the timeout expires before
        the Interpreter reaches the specified status, it raises
        LinuxCNC_Exception.
        """

        def _reached():
            self.status.poll()
            return self.status.interp_state == target_state
        self._wait(_reached, "interpreter state %d" % target_state, timeout,
                   detail=lambda: "interp_state=%d" % self.status.interp_state)

    def wait_for_tool_in_spindle(self, expected_tool, timeout=10.0):
        """
        Arguments:
            'expected_tool', integer, the tool we're waiting to find in
            the spindle.

            'timeout', float, the number of seconds to wait for the
            specified tool to show up in the spindle.

        This function polls the LinuxCNC Status buffer, waiting for the
        specified tool to appear in the spindle.  If the timeout expires
        before the tool appears, it raises LinuxCNC_Exception.
        """

        def _loaded():
            self.status.poll()
            return self.status.tool_in_spindle == expected_tool
        self._wait(_loaded, "tool %d in the spindle" % expected_tool, timeout,
                   detail=lambda: "tool_in_spindle=%d" % self.status.tool_in_spindle)
