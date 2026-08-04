"""
LinuxCNC User Interface helper functions
"""

# Under stmak, linuxcnc.so is a deprecation stub: linuxcnc.command/stat/
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


class LinuxCNC_Exception(Exception):
    pass


# How far an axis may drift and still count as "did not move". Sized to swallow
# float noise from sampling a live machine (and stmak's mm -> machine-unit
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


    def wait_for_linuxcnc_startup(self, timeout=10.0):

        """Poll the Status buffer waiting for it to look initialized,
        rather than just allocated (all-zero).  Returns on success, throws
        RuntimeError on failure."""

        start_time = time.time()
        while time.time() - start_time < timeout:
            self.status.poll()
            if (self.status.angular_units == 0.0) \
                or (self.status.axis_mask == 0) \
                or (self.status.cycle_time == 0.0) \
                or (self.status.exec_state != EXEC_DONE) \
                or (self.status.interp_state != INTERP_IDLE) \
                or (self.status.inpos == False) \
                or (self.status.linear_units == 0.0) \
                or (self.status.max_acceleration == 0.0) \
                or (self.status.max_velocity == 0.0) \
                or (self.status.program_units == 0.0) \
                or (self.status.rapidrate == 0.0) \
                or (self.status.state != RCS_DONE) \
                or (self.status.task_state != STATE_ESTOP):
                time.sleep(0.1)
            else:
                # looks good
                return

        # timeout, throw an exception
        raise RuntimeError


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

        Returns if all the specified joints homed before the timeout,
        raises LinuxCNC_Exception if the timeout expired first."""

        start_time = time.time()
        while (time.time() - start_time) < timeout:
            if self.all_joints_homed(joints):
                # The `homed` flag flips a few servo cycles before the
                # trajectory finishes converging on the home coordinate: a
                # caller that samples position the instant homing "completes"
                # can catch an axis still creeping to its home value (seen as
                # a jog_axis start_pos that later drifts to 0, tripping its
                # "no other axis moved" check). Let the machine settle first.
                self.wait_for_all_axes_to_stop(timeout=timeout)
                return
            time.sleep(0.1)

        raise LinuxCNC_Exception("timeout waiting for homing to complete:\nstatus.homed:" + str(self.status.homed) + "\nstatus.position:" + str(self.status.position))


    def wait_for_all_axes_to_stop(self, timeout=10.0):
        """Poll the Status buffer until every axis position is unchanged
        across two consecutive polls, i.e. the machine has come to rest.
        Returns on success, raises LinuxCNC_Exception if the timeout expires
        while some axis is still moving."""

        self.status.poll()
        start_time = time.time()
        prev_pos = self.status.position
        while (time.time() - start_time) < timeout:
            time.sleep(0.1)
            self.status.poll()
            new_pos = self.status.position
            if all(a == b for a, b in zip(prev_pos, new_pos)):
                return
            prev_pos = new_pos
        raise LinuxCNC_Exception("machine didn't settle within %.3f seconds; position still changing: %s" % (timeout, str(self.status.position)))


    def wait_for_axis_to_stop(self, axis_letter, timeout=10.0):
        """Arguments:
            'axis_letter' is a single character from the set [xyzabcuvw]
            indicating the axis to wait for stillness on.

            'timeout' is a float, the number of seconds to wait for the
            axis to stop before giving up.

        Returns if the specified axis stops moving before the timeout
        expires.  Raises LinuxCNC_Exception if the timeout expires before
        the axis stops.
        """

        axis_letter = axis_letter.lower()
        axis_index = 'xyzabcuvw'.index(axis_letter)
        print("waiting for axis", axis_letter, "to stop")
        self.status.poll()
        start_time = time.time()
        prev_pos = self.status.position[axis_index]
        while (time.time() - start_time) < timeout:
            time.sleep(0.1)
            self.status.poll()
            new_pos = self.status.position[axis_index]
            if new_pos == prev_pos:
                return
            prev_pos = new_pos
        raise LinuxCNC_Exception("axis %s didn't stop jogging!\n" % axis_letter + "axis %s is at %.3f %.3f seconds after reaching target (prev_pos=%.3f)\n" % (axis_letter, self.status.position[axis_index], timeout, prev_pos))


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
        while not done(self.status.position[axis_index]) and ((time.time() - start) < timeout):
            time.sleep(0.1)
            # stmak dead-mans continuous jogs not refreshed within its jog
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
            # running machine, and under stmak they are also scaled (mm -> machine
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

        This function polls the LinuxCNC Status buffer and monitors the
        position field, waiting for the specified axis to come to rest
        at the specified target.  If this does not happen before the
        timeout, the function raises LinuxCNC_Exception.
        """

        axis_letter = axis_letter.lower()
        axis_index = 'xyzabcuvw'.index(axis_letter)

        self.status.poll()
        start = time.time()

        while ((time.time() - start) < timeout):
            prev_pos = self.status.position[axis_index]
            self.status.poll()
            vel = self.status.position[axis_index] - prev_pos
            error = math.fabs(self.status.position[axis_index] - target)
            if (error < tolerance) and (vel == 0):
                print("axis %s stopped at %.3f" % (axis_letter, target))
                return
            time.sleep(0.1)
        raise LinuxCNC_Exception("timeout waiting for axis %s to stop at %.3f (pos=%.3f, vel=%.3f)" % (axis_letter, target, self.status.position[axis_index], vel))


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

        start_time = time.time()
        while (time.time() - start_time) < timeout:
            self.status.poll()
            if self.status.interp_state == target_state:
                return
            time.sleep(0.001)

        if self.status.interp_state != target_state:
            raise LinuxCNC_Exception("interpreter state %d did not reach target state %d" % (self.state.interp_state, target_state))


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

        start_time = time.time()
        while (time.time() - start_time) < timeout:
            time.sleep(0.1)
            self.status.poll()
            if self.status.tool_in_spindle == expected_tool:
                print("the Stat buffer's toolInSpindle reached the value of %d after %f seconds" % (expected_tool, time.time() - start_time))
                return
        raise LinuxCNC_Exception("the Stat buffer's toolInSpindle value is %d, expected %d" % (self.status.tool_in_spindle, expected_tool))

