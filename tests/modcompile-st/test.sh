#!/bin/bash
set -x

# Structured Text components: the .st frontend must produce a loadable cmod
# module, and must refuse a body that breaks the real-time rules.

# --- these must succeed ---
for name in and2 loops; do
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

# --- this one must fail ---
rm -f bad_unbounded.c
modcompile --preprocess bad_unbounded.st -o bad_unbounded.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted an unbounded WHILE in a real-time body'
    exit 1
fi

exit 0
