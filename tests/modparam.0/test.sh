#!/bin/bash
. "$(dirname "$0")/../hal-stream-driver.sh"
hal_start_server modparam.0.hal
# The instance count comes from the three explicit names in the load line, not
# from a module param (stratuMAK does not derive count from an array param's length).
halcmd list param '*maxaccel'
# step_type=2 is non-default (the default is 0).  If the scalar module param is
# parsed and applied to every named instance, each exports .phase-A.. pins and
# none exports the type-0 .step pin.  This is what makes the test exercise
# module-param application, not just instance creation.
halcmd list pin '*phase-A'
halcmd list pin '*.step'
