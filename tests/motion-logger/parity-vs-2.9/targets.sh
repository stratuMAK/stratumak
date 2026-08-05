# shellcheck shell=bash
# shellcheck disable=SC2034  # PARITY_TARGETS is consumed by the scripts that source this
# Shared target table for the motion-logger parity-vs-2.9 harness.
# Sourced by sync-oracle.sh (vendors the 2.9 gold) and compare.sh (diffs).
#
# Fields, whitespace-separated (no spaces allowed inside a field):
#   label        short name used on the command line / in output
#   oracle_rel   path under oracle-2.9/ where the vendored 2.9 gold lives
#   stmak_rel     path (relative to this dir) of the stratuMAK gold to compare
#   src_rel      path under the 2.9 tree's tests/ to vendor FROM (sync only)
#   strip        "strip" = drop inline bring-up preamble; "-" = pre-split segment
#   units        machine-units->mm factor for the ORACLE side (25.4 = inch
#                config; 1 = mm config). stratuMAK golds are always mm (factor 1).
#
# Only parity-able tests are listed. Excluded and why:
#   motion-logger/startup-gcode-abort — enabled and passing with a stratuMAK gold, but
#                                        not a comparable oracle: 2.9 defers the
#                                        startup move via interp_list (0 SET_LINE),
#                                        stratuMAK dispatches it at estop (1 SET_LINE).
#                                        See PARITY_FINDINGS.md.
#   abort/{on_abort_command,stop-button}-crazy-move — enabled, but run on real
#                                        core_sim motion; pass/fail comes from
#                                        axis position, not a motion-logger gold.
#   basic builtin-startup / reset — machine bring-up + hygiene, never compared.
PARITY_TARGETS=(
"basic/g0    basic/expected.g0                  ../basic/expected.g0                                                   motion-logger/basic/expected.g0                                  -     25.4"
"basic/g1    basic/expected.g1                  ../basic/expected.g1                                                   motion-logger/basic/expected.g1                                  -     25.4"
"basic/s     basic/expected.s                   ../basic/expected.s                                                    motion-logger/basic/expected.s                                   -     25.4"
"mountaindew mountaindew/expected.motion-logger ../mountaindew/expected.motion-logger                                  motion-logger/mountaindew/expected.motion-logger                 strip 25.4"
"m98m99-12   m98m99-12/expected.motion-logger   ../../interp/m98m99/12-M99-endless-main-program/expected.motion-logger interp/m98m99/12-M99-endless-main-program/expected.motion-logger strip 25.4"
)
