#!/bin/bash
set -x

# Structured Text components: the .st frontend must produce a loadable cmod
# module, and must refuse a body that breaks the real-time rules.

# --- these must succeed ---
for name in and2 loops statemachine arrays ramplimit fbnest twoprograms; do
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

# A function block instance holds its I/O as well as its state, since IEC says
# they persist between calls -- that is what makes a read of an output a read
# of retained state.
if ! grep -q 'Ramp_t r;' ramplimit.c; then
    echo 'ramplimit.c does not hold its function block instance in inst_t'
    exit 1
fi
if ! grep -q 'Ramp_body(__comp_inst, &(__comp_inst->r));' ramplimit.c; then
    echo 'ramplimit.c does not invoke its function block'
    exit 1
fi

# An instance nests and arrays because a block has no HAL identity of its own.
if ! grep -q 'Averager_t avg\[4\];' fbnest.c; then
    echo 'fbnest.c did not emit its array of instances'
    exit 1
fi
if ! grep -q 'Accumulator_t acc;' fbnest.c; then
    echo 'fbnest.c did not nest one instance inside another'
    exit 1
fi

# Each PROGRAM in a file becomes its own HAL function, with its own pins and
# its own fp flag: read touches no floating point, write does.
if ! grep -q '"%s.read"' twoprograms.c || ! grep -q '"%s.write"' twoprograms.c; then
    echo 'twoprograms.c did not export one HAL function per PROGRAM'
    exit 1
fi
if ! grep -q 'funct_read, inst, 0, 0,' twoprograms.c; then
    echo 'twoprograms.c: read should export with fp = 0'
    exit 1
fi
if ! grep -q 'funct_write, inst, 1, 0,' twoprograms.c; then
    echo 'twoprograms.c: write should export with fp = 1'
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

rm -f bad_recursive.c
modcompile --preprocess bad_recursive.st -o bad_recursive.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted a function block that contains itself'
    exit 1
fi

rm -f bad_shared_name.c
modcompile --preprocess bad_shared_name.st -o bad_shared_name.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted two programs declaring the same pin'
    exit 1
fi

exit 0
