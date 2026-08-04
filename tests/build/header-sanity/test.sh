#!/bin/sh
set -xe
# Several flat headers reference generated GMI headers (interp_ext.h ->
# interp_ext_api.h -> interp_ctx_api.h; canon_interface.hh and everything
# that reaches it through interp_internal.hh -> the src-relative
# stmak/generated/gmi/canon path). Out-of-tree builds get these -I from
# modcompile (the gmi_provide include set); mirror them here so the headers
# still get sanity-checked instead of skipped.
CPPFLAGS="$(pkg-config --silence-errors --cflags libtirpc) \
    -I$EMC2_HOME/src \
    -I$EMC2_HOME/src/stmak/generated/gmi/interp_ext \
    -I$EMC2_HOME/src/stmak/generated/gmi/interp_ctx"
for i in $HEADERS/*.h; do
    case $i in
    */rtapi_app.h) continue ;;
    esac
    gcc ${CPPFLAGS} -DULAPI -I$HEADERS -E -x c $i > /dev/null
done
for i in $HEADERS/*.h $HEADERS/*.hh; do
    case $i in
    */rtapi_app.h) continue ;;
    */interp_internal.hh) continue ;;
    esac
    if g++ ${CPPFLAGS} -std=c++11 -S -o /dev/null -xcxx /dev/null > /dev/null 2>&1; then
        ELEVEN=-std=c++11
    else
        ELEVEN=-std=c++0x
    fi
    g++ ${CPPFLAGS} $ELEVEN -DULAPI -I$HEADERS -E -x c++ $i > /dev/null
done
