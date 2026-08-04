#!/bin/bash -e

. ../stmak-driver.sh

# stmakd does not launch the [DISPLAY] program itself, so start the server
# and drive it with the test UI (ported to the gmi REST/WS client).
stmak_start_server --inherit test.ini

# Wait for milltask to be up and serving.
stmak_wait_ready

./test-ui.py
