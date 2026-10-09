#!/bin/bash
# The HAL names of array pins and params: the run of '#' in the declared name
# holds each element's index, zero-padded to the run's length.  An array
# without a run gave every element the same name, so the module failed to
# load; modcompile refuses it now, as halcompile does.
set -e

rm -f no_hash.c
if modcompile --preprocess no_hash.comp -o no_hash.c 2>no_hash.err; then
    echo 'modcompile accepted an array pin with no # in its name' >&2
    exit 1
fi
if ! grep -qF "no_hash.comp:2:12: pin array name \"in\" has no '#'" no_hash.err; then
    echo 'modcompile refused no_hash.comp for the wrong reason:' >&2
    cat no_hash.err >&2
    exit 1
fi
if [ -f no_hash.c ]; then
    echo 'modcompile produced no_hash.c' >&2
    exit 1
fi

# Build the test comp into cmod/ first -- never rely on a leftover.
${SUDO} modcompile --install array_names.comp
. "$(dirname "$0")/../../hal-stream-driver.sh"
hal_start_server array_names.hal
halcmd list pin | tr ' ' '\n' | grep '^an\.'
halcmd list param | tr ' ' '\n' | grep '^an\.'
