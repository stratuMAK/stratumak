#!/bin/bash
set -x

# Structured Text components: the .st frontend must produce a loadable cmod
# module, and must refuse a body that breaks the real-time rules.

# --- these must succeed ---
for name in and2 loops statemachine arrays; do
    rm -f $name.c $name.so
    modcompile --preprocess $name.st -o $name.c
    if [ $? -ne 0 ]; then
        echo "modcompile failed to preprocess $name.st"
        exit 1
    fi
    if [ ! -f $name.c ]; then
        echo "modcompile produced no $name.c"
        exit 1
    fi
    # The generated body must map back to the .st the author edits, not to
    # the generated file nobody keeps.
    if ! grep -q "^#line .*$name\.st" $name.c; then
        echo "$name.c carries no #line directive naming $name.st"
        exit 1
    fi

    modcompile --compile $name.st
    if [ $? -ne 0 ]; then
        echo "modcompile failed to compile $name.st to a module"
        exit 1
    fi
    if [ ! -f $name.so ]; then
        echo "modcompile produced no $name.so"
        exit 1
    fi
done

# and2 must export the loader entry point, or stmakd cannot load it.
if ! nm -D --defined-only and2.so | grep -q ' T New$'; then
    echo 'and2.so does not export New()'
    exit 1
fi

# An enumeration constant must reach the generated C as a resolved constant.
# The standalone prototype emitted `inst->RED` here, which does not compile.
if ! grep -q '#define STATE_IDLE 0' statemachine.c; then
    echo 'statemachine.c did not resolve its enumeration constants'
    exit 1
fi
if ! grep -q 'case STATE_IDLE:' statemachine.c; then
    echo 'statemachine.c did not label its CASE arms by constant'
    exit 1
fi

# An array pin gets HAL's indexed names, and a runtime subscript is clamped
# rather than trusted -- an out-of-range access here corrupts the daemon.
if ! grep -q '"%s.channel.%01d"' arrays.c; then
    echo 'arrays.c did not give its array pin indexed HAL names'
    exit 1
fi
if ! grep -q '__st_idx(' arrays.c; then
    echo 'arrays.c does not clamp its runtime subscripts'
    exit 1
fi
# A multi-dimensional array keeps its shape in the generated C.
if ! grep -q 'double grid\[2\]\[3\];' arrays.c; then
    echo 'arrays.c flattened a multi-dimensional array'
    exit 1
fi

# --- these must fail ---
rm -f bad_unbounded.c
modcompile --preprocess bad_unbounded.st -o bad_unbounded.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted an unbounded WHILE in a real-time body'
    exit 1
fi

rm -f bad_index.c
modcompile --preprocess bad_index.st -o bad_index.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted a constant index outside the declared bounds'
    exit 1
fi

exit 0
