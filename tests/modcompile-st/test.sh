#!/bin/bash
set -x

# Structured Text components: the .st frontend must produce a loadable cmod
# module, and must refuse a body that breaks the real-time rules.

# --- these must succeed ---
for name in and2 loops statemachine arrays ramplimit fbnest twoprograms atmap actions params period durations uselib methods strings; do
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
if ! grep -q '__fb_body_Ramp(__comp_inst, &(__comp_inst->r));' ramplimit.c; then
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

# An action of an instance, called from outside the block that declares it.
# Actions and methods share this syntax -- a name is one or the other -- and
# the action's own C function already takes the instance, so the call site
# just names a different one.
if ! grep -q 'Counter_clear(__comp_inst, &((__comp_inst->c)));' actions.c; then
    echo 'actions.c did not emit an action call on another instance'
    exit 1
fi

# A HAL parameter is a value in shared memory that no signal connects to.  It
# differs from a pin in inst_hal_t: a pin is a pointer into shared memory, a
# parameter is the storage itself, so every access is one dereference less.
if ! grep -q 'stmak_hal_float_t gain;' params.c; then
    echo 'params.c holds its parameter as a pointer, like a pin'
    exit 1
fi
if ! grep -q 'stmak_hal_float_t \*cmd;' params.c; then
    echo 'params.c does not hold its pin as a pointer'
    exit 1
fi
# Registered with HAL's own directions, and defaults applied before
# registration -- including one declared inside a function block, which has to
# reach HAL rather than only the instance.
if ! grep -q 'stmak_hal_param_float_newf(env->hal, STMAK_HAL_RW,' params.c; then
    echo 'params.c did not register an rw parameter'
    exit 1
fi
if ! grep -q 'stmak_hal_param_bit_newf(env->hal, STMAK_HAL_RO,' params.c; then
    echo 'params.c did not register an r parameter'
    exit 1
fi
if ! grep -q 'inst->hal->lim_ceiling = 100.0;' params.c; then
    echo "params.c lost a function block parameter's default"
    exit 1
fi
# A parameter inside a block is named by the path that reaches it, exactly as
# a published pin is.
for p in '"%s.gain"' '"%s.active"' '"%s.lim.ceiling"' '"%s.lim.clamped"'; do
    if ! grep -qF "$p" params.c; then
        echo "params.c is missing the parameter $p"
        exit 1
    fi
done
# The body reads a parameter in place: a pin is driven by a thread and could
# change under the body, a parameter is changed by a person typing setp.
if grep -q '__pin_gain' params.c; then
    echo 'params.c snapshotted a parameter; only pins are'
    exit 1
fi
# The man page documents parameters as it documents pins.
rm -f params.9
modcompile --document params.st -o params.9
if [ $? -ne 0 ]; then
    echo 'modcompile failed to document params.st'
    exit 1
fi
if ! grep -q '^\.SH PARAMETERS' params.9; then
    echo 'params.9 has no PARAMETERS section'
    exit 1
fi
if ! grep -q 'Multiplies the command before limiting' params.9; then
    echo 'params.9 does not document its parameters'
    exit 1
fi

# fperiod is the period of the thread the body is running in, and the backend
# reaches it two ways: through the HAL function's own period argument where
# that is in scope, and through the RT task where it is not -- a block body or
# an action, each of which is its own C function.
if ! grep -q '((__p->elapsed) + fperiod)' period.c; then
    echo 'period.c: an exported body should use the period argument directly'
    exit 1
fi
if ! grep -q 'static double __st_fperiod(inst_t \*__comp_inst)' period.c; then
    echo 'period.c did not emit the runtime fperiod helper'
    exit 1
fi
if ! grep -q '__rt->task_period(__rt->ctx, __task)' period.c; then
    echo 'period.c does not ask the RT task for its period'
    exit 1
fi
if ! grep -q '__st_fperiod(__comp_inst)' period.c; then
    echo 'period.c: a block body should reach fperiod through the helper'
    exit 1
fi
# The helper is a static function and cmods build with -Werror, so one that
# nothing calls fails the build.  ramplimit uses fperiod in its PROGRAM only.
if grep -q '__st_fperiod' ramplimit.c; then
    echo 'ramplimit.c got the helper it never calls; -Werror=unused-function'
    exit 1
fi

# A duration is int64 nanoseconds -- what get_time() and a task's period
# already are -- and every conversion carries the scale reconciling that with
# the unit the number is in.
if ! grep -q 'int64_t pt;' durations.c; then
    echo 'durations.c does not hold a TIME as int64'
    exit 1
fi
if ! grep -q 'int64_t errDelay;' durations.c; then
    echo 'durations.c does not hold a TIME inside a STRUCT'
    exit 1
fi
if ! grep -q '90000000000LL' durations.c; then
    echo 'durations.c: T#1m30s should reach the C as 90000000000LL'
    exit 1
fi
# The millisecond bridge is what a ported CODESYS source depends on:
# TIME_TO_DINT(T#1s) is 1000, not 1000000000.  The seconds pair is stratuMAK's
# own unit and carries a name no CODESYS source contains.
if ! grep -q '/ (int64_t)1000000)' durations.c; then
    echo 'durations.c: TIME_TO_DINT should yield milliseconds, as CODESYS does'
    exit 1
fi
if ! grep -q '\* 1000000000))' durations.c; then
    echo 'durations.c: SECONDS_TO_TIME should scale up to nanoseconds'
    exit 1
fi
# Dividing a duration by a runtime value must be guarded: unguarded it is a
# SIGFPE in the RT thread.  A duration is not on the numeric lattice, so it
# does not get this for free.
if ! grep -q '__st_div_s(__comp_inst, (int64_t)(__comp_inst->pt)' durations.c; then
    echo 'durations.c: dividing a duration is unguarded; a zero divisor would SIGFPE'
    exit 1
fi

# toC collapses a run of separators into one underscore, because stripping an
# array pin's '#' marker leaves its neighbours adjacent.  A literal underscore
# is not a separator: collapsing those too made a_b and a__b one C identifier
# while ST kept them two variables, so one silently aliased the other.
if ! grep -q 'axes_position' atmap.c; then
    echo 'atmap.c: an array pin path should collapse to one underscore'
    exit 1
fi

# A library is a .st of TYPE/FUNCTION/FUNCTION_BLOCK declarations and no
# PROGRAM, named by USING.  What it declares is generated into the consumer's
# own module: no shared object, no ABI, and the call is a static function in
# the same translation unit.
if ! grep -q 'static void __fb_body_TON' uselib.c; then
    echo 'uselib.c did not generate the library block it uses'
    exit 1
fi
# ...but only what is used.  A block body is a static function and cmods build
# with -Werror, so a library's unused half would fail the build outright.
if grep -q 'TOF' uselib.c; then
    echo 'uselib.c generated a library block nothing uses; -Werror=unused-function'
    exit 1
fi
# A fault inside a library block maps back to the library's own source, not to
# the component that happened to use it.
if ! grep -q '^#line [0-9]* "timers.st"' uselib.c; then
    echo 'uselib.c does not map the library body back to timers.st'
    exit 1
fi
# Pointing modcompile at a library is refused: making a component of it would
# export nothing at all.
modcompile --preprocess timers.st -o /dev/null
if [ $? -eq 0 ]; then
    echo 'modcompile tried to make a component out of a library'
    exit 1
fi

# A METHOD is an operation on an instance, with its own interface and its own
# locals, reaching the instance's variables.  Calling one on the instance
# already in scope needs no name for it: the generated function has __self.
if ! grep -q 'static int32_t Accumulator_add(inst_t \*__comp_inst, Accumulator_t \*__self, int32_t n, int32_t weight)' methods.c; then
    echo 'methods.c did not emit a method over the instance'
    exit 1
fi
if ! grep -q 'Accumulator_add(__comp_inst, __self, 1, 1)' methods.c; then
    echo 'methods.c did not pass __self through a bare sibling call'
    exit 1
fi
if ! grep -q 'Accumulator_clear(__comp_inst, __self)' methods.c; then
    echo 'methods.c did not resolve THIS^ to the instance in scope'
    exit 1
fi
# A method returning nothing is void, not a type ST would have to invent.
if ! grep -q 'static void Accumulator_clear' methods.c; then
    echo 'methods.c gave a result to a method that declares none'
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

# A library's documentation has to be reachable.  Every block carries doc
# pragmas on its interface, and until --document understood a library those
# were write-only: the source was the only place to read them.
rm -rf libdoc && mkdir -p libdoc
modcompile --document timers.st -o libdoc
if [ $? -ne 0 ]; then
    echo 'modcompile failed to document a library'
    exit 1
fi
# One page per block, named for the block, in section 3 -- a block is not a
# HAL component, and `man 3 TON` is the question somebody asks.
if [ ! -f libdoc/TON.3 ]; then
    echo 'modcompile --document produced no page for TON'
    exit 1
fi
if ! grep -q '^\.TH TON "3"' libdoc/TON.3; then
    echo 'TON.3 is not a section 3 man page'
    exit 1
fi
# The interface, with the documentation that was stranded in the source.
for want in 'USING timers;' 'How long IN must stay true' '^\.SH INPUTS' '^\.SH OUTPUTS'; do
    if ! grep -q "$want" libdoc/TON.3; then
        echo "TON.3 is missing: $want"
        exit 1
    fi
done
# A block the consumer never uses is still documented: pruning is about what
# gets compiled, not about what can be looked up.
if [ ! -f libdoc/TOF.3 ]; then
    echo 'libdoc has no page for TOF, which nothing in this test uses'
    exit 1
fi
# And the licence travels with it, because these are not GPL like the rest.
if ! grep -q 'SEE ALSO' libdoc/TON.3; then
    echo 'TON.3 does not point at the librarys other blocks'
    exit 1
fi
rm -rf libdoc

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

rm -f bad_param.c
modcompile --preprocess bad_param.st -o bad_param.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted an assignment to an rw parameter'
    exit 1
fi

rm -f bad_period.c
modcompile --preprocess bad_period.st -o bad_period.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted fperiod inside a FUNCTION'
    exit 1
fi

rm -f bad_time.c
modcompile --preprocess bad_time.st -o bad_time.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted a number added to a duration'
    exit 1
fi

rm -f bad_cname.c
modcompile --preprocess bad_cname.st -o bad_cname.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted a variable named for a C keyword'
    exit 1
fi

rm -f bad_library.c
modcompile --preprocess bad_library.st -o bad_library.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted USING a library that does not exist'
    exit 1
fi

rm -f bad_extaction.c
modcompile --preprocess bad_extaction.st -o bad_extaction.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted arguments to an external action call'
    exit 1
fi

rm -f bad_pinlen.c
modcompile --preprocess bad_pinlen.st -o bad_pinlen.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted a pin name HAL would have to truncate'
    exit 1
fi

rm -f bad_thisrec.c
modcompile --preprocess bad_thisrec.st -o bad_thisrec.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted two methods calling each other'
    exit 1
fi

rm -f bad_shared_name.c
modcompile --preprocess bad_shared_name.st -o bad_shared_name.c
if [ $? -eq 0 ]; then
    echo 'modcompile accepted two programs declaring the same pin'
    exit 1
fi

exit 0

# A STRING is a char array wherever it is declared, and C has no assignment
# for one.  Each of these emitted `x = "lit"` or `char x;` once, which is
# either a compile error or one byte where the declaration asked for more.
if ! grep -q 'snprintf(__self->base, sizeof(__self->base), "%s", "ax");' strings.c; then
    echo 'strings.c assigned a block member STRING instead of copying it'
    exit 1
fi
if ! grep -q 'char cut\[17\] = "none";' strings.c; then
    echo 'strings.c did not give a function-local STRING its length'
    exit 1
fi
# A string result is built in a buffer the call site owns, and the calls nest
# as expressions -- one buffer per site, or the inner call overwrites what the
# outer one is reading.
if [ "$(grep -c 'char __st_s[0-9]*\[' strings.c)" -ne 5 ]; then
    echo "strings.c declared $(grep -c 'char __st_s[0-9]*\[' strings.c) result buffers, want one per call site"
    exit 1
fi
# The action and the method are their own C functions, and a helper either
# calls still has to be defined in this file.  Leaving them out of the scan
# emitted the call and not the definition.
if ! grep -q 'static char \*__st_concat' strings.c; then
    echo 'strings.c calls __st_concat from an action and a method but defines it nowhere'
    exit 1
fi
