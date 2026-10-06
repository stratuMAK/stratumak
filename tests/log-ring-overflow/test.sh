#!/bin/bash
${SUDO} modcompile --install logburst.comp
. ../stmak-driver.sh
stmak_start_server log-ring-overflow.ini
stmak_wait_ready
./test-ui.py
