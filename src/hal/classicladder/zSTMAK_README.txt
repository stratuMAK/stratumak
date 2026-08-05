What is left of this directory, and why
=======================================

The ClassicLadder GTK application (classicladder.c, the *_gtk.* files, the
modbus/serial/socket I/O, module_hal.c and the Submakefile) was removed: the
stratuMAK port -- RT engine in src/stmak/internal/classicladder, UI in
src/webapp/classicladder -- replaced it, and the Submakefile had already been
dropped from SUBDIRS so none of it was being built.

The remaining sources are NOT dead code. They are the LinuxCNC 2.9 ladder
engine, kept as the executable specification of ladder semantics: the
differential test suite (src/stmak/internal/classicladder/oracle_test.go)
compiles them into a headless oracle binary
(testdata/oracle/Makefile) and requires the stratuMAK RT engine to match it scan
for scan. That test hard-fails -- deliberately -- if these files go missing.

Do not "clean up" the engine files without moving the oracle with them.
