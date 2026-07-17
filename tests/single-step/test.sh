#!/bin/bash -e

. ../gomc-driver.sh

# gomc-server does not launch the [DISPLAY] program itself, so start the server
# and drive it with the test UI (ported to the gmi REST/WS client).
gomc_start_server --inherit test.ini

# Wait for milltask to be up and serving.
gomc_wait_ready

./test-ui.py
