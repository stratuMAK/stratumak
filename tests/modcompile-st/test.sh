#!/bin/bash
set -x

# Structured Text components: the .st frontend must produce a loadable cmod
# module, and must refuse a body that breaks the real-time rules.

# --- these must succeed ---
for name in and2 loops statemachine arrays ramplimit fbnest twoprograms atmap actions; do
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

# An AT clause maps a plain VAR onto a pin and may rename it, and a function
# block publishes pins along the path of each instance -- with an array segment
# becoming the pin's index.
for pin in '"%s.enable"' '"%s.active"' '"%s.x.position"' '"%s.axes.%01d.position"'; do
    if ! grep -qF "$pin" atmap.c; then
        echo "atmap.c is missing the pin $pin"
        exit 1
    fi
done
# Published pins move between HAL and the instance at the boundary, so the
# block body itself only ever touches its own members.
if ! grep -q '(\*(__comp_inst->hal->x_position)) = __comp_inst->x.pos;' atmap.c; then
    echo 'atmap.c does not store a published pin back from its instance'
    exit 1
fi

# An ACTION is a named piece of a POU's body, sharing its variables.  For a
# function block that means the instance pointer is the whole signature; for a
# PROGRAM, whose pins live in a per-cycle snapshot, the snapshot has to be
# reachable too or the action would read stale shared memory instead.
if ! grep -q 'static void Counter_clear(inst_t \*__comp_inst, Counter_t \*__self)' actions.c; then
    echo 'actions.c: an action of a function block should take the instance'
    exit 1
fi
if ! grep -q 'typedef struct {' actions.c || ! grep -q '} actions_pins_t;' actions.c; then
    echo 'actions.c did not emit the pin snapshot its actions read'
    exit 1
fi
if ! grep -q 'static void actions_advance(inst_t \*__comp_inst, actions_pins_t \*__p)' actions.c; then
    echo 'actions.c: an action touching a pin should be handed the snapshot'
    exit 1
fi
# The body reads and writes the snapshot rather than shared memory, which is
# what makes the action and the body agree on the values for the cycle.
if ! grep -q '__p->enable = (\*(__comp_inst->hal->enable));' actions.c; then
    echo 'actions.c does not load its pins into the snapshot'
    exit 1
fi
if ! grep -q '(\*(__comp_inst->hal->count)) = __p->count;' actions.c; then
    echo 'actions.c does not store the snapshot back to its pins'
    exit 1
fi
# An action has no state and no interface, so nothing of it reaches inst_t.
if grep -q 'advance;' actions.c; then
    echo 'actions.c gave an action storage of its own'
    exit 1
fi

# A man page generated from an .st carries what one generated from a .comp
# does.  docgen is shared, so this really checks that every field it reads is
# reachable from ST.
rm -f documented.9
modcompile --document documented.st -o documented.9
if [ $? -ne 0 ]; then
    echo 'modcompile failed to document documented.st'
    exit 1
fi
for section in NAME SYNOPSIS DESCRIPTION FUNCTIONS PINS EXAMPLES 'SEE ALSO' NOTES AUTHOR LICENSE; do
    if ! grep -q "^\.SH $section" documented.9; then
        echo "documented.9 has no $section section"
        exit 1
    fi
done
# Each PROGRAM is documented as its own HAL function, and a pin's doc stays
# with its pin rather than drifting onto the next one.
if ! grep -q 'Latches the raw input when strobed' documented.9; then
    echo 'documented.9 does not document its functions'
    exit 1
fi
if ! grep -q 'Value to latch\.' documented.9; then
    echo 'documented.9 does not document its pins'
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

rm -f bad_address.c
modcompile --preprocess bad_address.st -o bad_address.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted an IEC physical address in an AT clause'
    exit 1
fi

rm -f bad_action.c
modcompile --preprocess bad_action.st -o bad_action.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted an action that calls itself'
    exit 1
fi

rm -f bad_shared_name.c
modcompile --preprocess bad_shared_name.st -o bad_shared_name.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted two programs declaring the same pin'
    exit 1
fi

exit 0
