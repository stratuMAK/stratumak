[HAL]
# list of hal config files to run through halcmd
# files are executed in the order in which they appear
HALFILE = LIB:linuxcnc.hal
HALFILE = LIB:threads.hal
HALFILE = core_sim.hal
HALFILE = sim_spindle_encoder.hal
HALFILE = LIB:manualtoolchange.hal
HALFILE = simulated_home.hal
HALUI   = halui
