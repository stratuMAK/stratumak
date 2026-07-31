/* classicladder_rt.c — RT ladder evaluation engine for gomc.
 *
 * Based on Classic Ladder Project by Marc Le Douarain (calc.c, module_hal.c).
 * Copyright (C) Marc Le Douarain <marc.le.douarain@free.fr> (LGPL 2.1+)
 * Adapted for the gomc cgo architecture — no shared memory, direct struct access.
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 *
 * This program is free software; you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License v2.1+.
 */

#include "classicladder_rt.h"
#include <stdlib.h>
#include <string.h>

/* --- Variable access helpers --- */

/* Timer/monostable/IEC-timer time bases are held in milliseconds (2.9
 * TIME_BASE_MINS/SECS/100MS). A zero base would divide by zero in the public
 * variable accessors, so clamp it to 1ms. */
static inline int cl_base_ms(int base) {
    return base > 0 ? base : 1;
}

/* Number of valid offsets for a variable type: the configured size of the
 * region that (type, offset) indexes. The backing arrays are allocated and
 * packed by these same runtime sizes, so an offset within its region can
 * never index out of the allocation. Returns 0 for unknown types. */
static int cl_var_region_size(const classicladder_rt_t *rt, int type) {
    switch (type) {
    case CL_VAR_MEM_BIT:          return rt->sizes.nbr_bits;
    case CL_VAR_PHYS_INPUT:       return rt->sizes.nbr_phys_inputs;
    case CL_VAR_PHYS_OUTPUT:      return rt->sizes.nbr_phys_outputs;
    case CL_VAR_ERROR_BIT:        return rt->sizes.nbr_error_bits;
    case CL_VAR_STEP_ACTIVITY:    return CL_MAX_STEPS;
    case CL_VAR_STEP_TIME:        return CL_MAX_STEPS;
    case CL_VAR_MEM_WORD:         return rt->sizes.nbr_words;
    case CL_VAR_PHYS_WORD_INPUT:  return rt->sizes.nbr_s32_in;
    case CL_VAR_PHYS_WORD_OUTPUT: return rt->sizes.nbr_s32_out;
    case CL_VAR_PHYS_FLOAT_INPUT: return rt->sizes.nbr_float_in;
    case CL_VAR_PHYS_FLOAT_OUTPUT: return rt->sizes.nbr_float_out;
    case CL_VAR_TIMER_DONE:
    case CL_VAR_TIMER_RUNNING:
    case CL_VAR_TIMER_PRESET:
    case CL_VAR_TIMER_VALUE:      return rt->sizes.nbr_timers;
    case CL_VAR_MONOSTABLE_RUNNING:
    case CL_VAR_MONOSTABLE_PRESET:
    case CL_VAR_MONOSTABLE_VALUE: return rt->sizes.nbr_monostables;
    case CL_VAR_COUNTER_DONE:
    case CL_VAR_COUNTER_EMPTY:
    case CL_VAR_COUNTER_FULL:
    case CL_VAR_COUNTER_PRESET:
    case CL_VAR_COUNTER_VALUE:    return rt->sizes.nbr_counters;
    case CL_VAR_TIMER_IEC_DONE:
    case CL_VAR_TIMER_IEC_PRESET:
    case CL_VAR_TIMER_IEC_VALUE:  return rt->sizes.nbr_timers_iec;
    default:                      return 0;
    }
}

static int read_var(classicladder_rt_t *rt, int type, int offset) {
    /* Reject out-of-range offsets (e.g. from the REST set_variable endpoint)
     * before they index a backing array out of bounds. */
    if (offset < 0 || offset >= cl_var_region_size(rt, type))
        return 0;
    switch (type) {
    case CL_VAR_MEM_BIT:
        return rt->var_bits[offset];
    case CL_VAR_PHYS_INPUT:
        return rt->var_bits[rt->sizes.nbr_bits + offset];
    case CL_VAR_PHYS_OUTPUT:
        return rt->var_bits[rt->sizes.nbr_bits + rt->sizes.nbr_phys_inputs + offset];
    case CL_VAR_ERROR_BIT:
        return rt->var_bits[rt->sizes.nbr_bits + rt->sizes.nbr_phys_inputs +
                           rt->sizes.nbr_phys_outputs + CL_MAX_STEPS + offset];
    case CL_VAR_STEP_ACTIVITY:
        return rt->var_bits[rt->sizes.nbr_bits + rt->sizes.nbr_phys_inputs +
                           rt->sizes.nbr_phys_outputs + offset];
    case CL_VAR_TIMER_DONE:
        return rt->timers[offset].output_done;
    case CL_VAR_TIMER_RUNNING:
        return rt->timers[offset].output_running;
    case CL_VAR_TIMER_IEC_DONE:
        return rt->timers_iec[offset].output;
    case CL_VAR_MONOSTABLE_RUNNING:
        return rt->monostables[offset].output_running;
    case CL_VAR_COUNTER_DONE:
        return rt->counters[offset].output_done;
    case CL_VAR_COUNTER_EMPTY:
        return rt->counters[offset].output_empty;
    case CL_VAR_COUNTER_FULL:
        return rt->counters[offset].output_full;
    /* Word variables */
    case CL_VAR_MEM_WORD:
        return rt->var_words[offset];
    case CL_VAR_STEP_TIME:
        return rt->var_words[rt->sizes.nbr_words + rt->sizes.nbr_s32_in +
                            rt->sizes.nbr_s32_out + offset];
    /* Timer/monostable preset and value are held in milliseconds; the public
     * variable is expressed in base units (as in 2.9 vars_access.c). */
    case CL_VAR_TIMER_PRESET:
        return rt->timers[offset].preset / cl_base_ms(rt->timers[offset].base);
    case CL_VAR_TIMER_VALUE:
        return rt->timers[offset].value / cl_base_ms(rt->timers[offset].base);
    case CL_VAR_MONOSTABLE_PRESET:
        return rt->monostables[offset].preset / cl_base_ms(rt->monostables[offset].base);
    case CL_VAR_MONOSTABLE_VALUE:
        return rt->monostables[offset].value / cl_base_ms(rt->monostables[offset].base);
    case CL_VAR_COUNTER_PRESET:
        return rt->counters[offset].preset;
    case CL_VAR_COUNTER_VALUE:
        return rt->counters[offset].value;
    case CL_VAR_TIMER_IEC_PRESET:
        return rt->timers_iec[offset].preset;
    case CL_VAR_TIMER_IEC_VALUE:
        return rt->timers_iec[offset].value;
    case CL_VAR_PHYS_WORD_INPUT:
        return rt->var_words[rt->sizes.nbr_words + offset];
    case CL_VAR_PHYS_WORD_OUTPUT:
        return rt->var_words[rt->sizes.nbr_words + rt->sizes.nbr_s32_in + offset];
    case CL_VAR_PHYS_FLOAT_INPUT:
        return (int)rt->var_floats[offset];
    case CL_VAR_PHYS_FLOAT_OUTPUT:
        return (int)rt->var_floats[rt->sizes.nbr_float_in + offset];
    default:
        return 0;
    }
}

static void write_var(classicladder_rt_t *rt, int type, int offset, int value) {
    /* Reject out-of-range offsets (e.g. from the REST set_variable endpoint)
     * before they index a backing array out of bounds. */
    if (offset < 0 || offset >= cl_var_region_size(rt, type))
        return;
    switch (type) {
    case CL_VAR_MEM_BIT:
        rt->var_bits[offset] = value ? 1 : 0;
        break;
    case CL_VAR_PHYS_INPUT:
        rt->var_bits[rt->sizes.nbr_bits + offset] = value ? 1 : 0;
        break;
    case CL_VAR_PHYS_OUTPUT:
        rt->var_bits[rt->sizes.nbr_bits + rt->sizes.nbr_phys_inputs + offset] = value ? 1 : 0;
        break;
    case CL_VAR_ERROR_BIT:
        rt->var_bits[rt->sizes.nbr_bits + rt->sizes.nbr_phys_inputs +
                    rt->sizes.nbr_phys_outputs + CL_MAX_STEPS + offset] = value ? 1 : 0;
        break;
    case CL_VAR_STEP_ACTIVITY:
        rt->var_bits[rt->sizes.nbr_bits + rt->sizes.nbr_phys_inputs +
                    rt->sizes.nbr_phys_outputs + offset] = value ? 1 : 0;
        break;
    /* Block outputs are published through the variable layer, so that a
     * contact on %C0 or %TI0 sees what the block just computed. */
    case CL_VAR_COUNTER_DONE:
        rt->counters[offset].output_done = value ? 1 : 0;
        break;
    case CL_VAR_COUNTER_EMPTY:
        rt->counters[offset].output_empty = value ? 1 : 0;
        break;
    case CL_VAR_COUNTER_FULL:
        rt->counters[offset].output_full = value ? 1 : 0;
        break;
    case CL_VAR_TIMER_IEC_DONE:
        rt->timers_iec[offset].output = value ? 1 : 0;
        break;
    case CL_VAR_MEM_WORD:
        rt->var_words[offset] = value;
        break;
    case CL_VAR_STEP_TIME:
        rt->var_words[rt->sizes.nbr_words + rt->sizes.nbr_s32_in +
                     rt->sizes.nbr_s32_out + offset] = value;
        break;
    /* Public value is in base units, stored in milliseconds. */
    case CL_VAR_TIMER_PRESET:
        rt->timers[offset].preset = value * cl_base_ms(rt->timers[offset].base);
        break;
    /* Deliberate divergence: 2.9's WriteVar has no VALUE cases — writing
     * %T0.V or %M0.V was a logged no-op there. Accepting it lets an operator
     * rewind or force a running timer from the variable spy, which is what a
     * write to the value can only mean. Differential scripts must simply not
     * write these. */
    case CL_VAR_TIMER_VALUE:
        rt->timers[offset].value = value * cl_base_ms(rt->timers[offset].base);
        break;
    case CL_VAR_MONOSTABLE_PRESET:
        rt->monostables[offset].preset = value * cl_base_ms(rt->monostables[offset].base);
        break;
    case CL_VAR_MONOSTABLE_VALUE:
        rt->monostables[offset].value = value * cl_base_ms(rt->monostables[offset].base);
        break;
    case CL_VAR_COUNTER_PRESET:
        rt->counters[offset].preset = value;
        break;
    case CL_VAR_COUNTER_VALUE:
        rt->counters[offset].value = value;
        break;
    case CL_VAR_TIMER_IEC_PRESET:
        rt->timers_iec[offset].preset = value;
        break;
    case CL_VAR_TIMER_IEC_VALUE:
        rt->timers_iec[offset].value = value;
        break;
    case CL_VAR_PHYS_WORD_INPUT:
        rt->var_words[rt->sizes.nbr_words + offset] = value;
        break;
    case CL_VAR_PHYS_WORD_OUTPUT:
        rt->var_words[rt->sizes.nbr_words + rt->sizes.nbr_s32_in + offset] = value;
        break;
    case CL_VAR_PHYS_FLOAT_INPUT:
        rt->var_floats[offset] = value;
        break;
    case CL_VAR_PHYS_FLOAT_OUTPUT:
        rt->var_floats[rt->sizes.nbr_float_in + offset] = value;
        break;
    default:
        break;
    }
}

/* --- HAL I/O --- */

static void read_hal_inputs(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_phys_inputs; i++) {
        if (rt->hal_inputs[i])
            write_var(rt, CL_VAR_PHYS_INPUT, i, *(rt->hal_inputs[i]));
    }
}

static void write_hal_outputs(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_phys_outputs; i++) {
        if (rt->hal_outputs[i])
            *(rt->hal_outputs[i]) = read_var(rt, CL_VAR_PHYS_OUTPUT, i);
    }
}

static void read_hal_s32_inputs(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_s32_in; i++) {
        if (rt->hal_s32_inputs[i])
            write_var(rt, CL_VAR_PHYS_WORD_INPUT, i, *(rt->hal_s32_inputs[i]));
    }
}

static void write_hal_s32_outputs(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_s32_out; i++) {
        if (rt->hal_s32_outputs[i])
            *(rt->hal_s32_outputs[i]) = read_var(rt, CL_VAR_PHYS_WORD_OUTPUT, i);
    }
}

static void read_hal_float_inputs(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_float_in; i++) {
        if (rt->hal_float_inputs[i])
            write_var(rt, CL_VAR_PHYS_FLOAT_INPUT, i, (int)(*(rt->hal_float_inputs[i])));
    }
}

static void write_hal_float_outputs(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_float_out; i++) {
        if (rt->hal_float_outputs[i])
            *(rt->hal_float_outputs[i]) = (double)read_var(rt, CL_VAR_PHYS_FLOAT_OUTPUT, i);
    }
}

/* --- Rung evaluation ---
 *
 * Faithful port of 2.9 calc.c. Every cell of every rung is evaluated on every
 * scan, whether or not it carries power: an element's DynamicOutput is what
 * feeds the cells to its right, and a de-energized coil must still be written
 * back to 0. Blocks (timers, monostables, counters, IEC timers) are evaluated
 * inline, when and only when the rung that contains them is executed.
 */

/* State arriving on the left of (x,y): the output of the cell to the left,
 * OR-ed with the outputs left of every cell reachable through an unbroken
 * chain of vertical connections — upwards and downwards. */
static char state_on_left(int x, int y, cl_rung_t *rung) {
    char state = 0;
    int posy;
    char still_connected;

    /* Directly connected to the left power rail? */
    if (x == 0)
        return 1;

    /* Direct on left */
    if (rung->elements[x - 1][y].dynamic_output)
        state = 1;

    /* Up */
    posy = y;
    still_connected = rung->elements[x][posy].connected_with_top;
    while (posy > 0 && still_connected) {
        posy--;
        if (rung->elements[x - 1][posy].dynamic_output)
            state = 1;
        if (!rung->elements[x][posy].connected_with_top)
            still_connected = 0;
    }

    /* Down */
    if (y < CL_RUNG_HEIGHT - 1) {
        posy = y + 1;
        still_connected = rung->elements[x][posy].connected_with_top;
        while (posy < CL_RUNG_HEIGHT && still_connected) {
            if (rung->elements[x - 1][posy].dynamic_output)
                state = 1;
            posy++;
            if (posy < CL_RUNG_HEIGHT) {
                if (!rung->elements[x][posy].connected_with_top)
                    still_connected = 0;
            }
        }
    }
    return state;
}

/* Set the dynamic output of a cell of a multi-row block, if it is on the grid. */
static void set_block_output(cl_rung_t *rung, int x, int y, char state) {
    if (y >= 0 && y < CL_RUNG_HEIGHT)
        rung->elements[x][y].dynamic_output = state;
}

/* State on the left of a multi-column block whose head sits at column x and
 * which spans `width` columns. Column 0 of the grid is always powered. */
static char block_state_on_left(int x, int y, int width, cl_rung_t *rung) {
    int left = x - (width - 1);
    if (left <= 0)
        return 1;
    if (y < 0 || y >= CL_RUNG_HEIGHT)
        return 0;
    return state_on_left(left, y, rung);
}

/* Elements : -| |- -|/|- -|P|- -|N|- */
static void calc_type_input(classicladder_rt_t *rt, int x, int y,
                            cl_rung_t *rung, char is_not, char only_fronts) {
    cl_element_t *ele = &rung->elements[x][y];
    char state;
    char state_element;
    char state_var;

    state_element = read_var(rt, ele->var_type, ele->var_num) ? 1 : 0;
    if (is_not)
        state_element = !state_element;
    state_var = state_element;
    if (only_fronts) {
        if (state_element && ele->dynamic_var_bak)
            state_element = 0;
    }
    ele->dynamic_state = state_element;
    if (x == 0) {
        state = state_element;
    } else {
        ele->dynamic_input = state_on_left(x, y, rung);
        state = state_element && ele->dynamic_input;
    }
    ele->dynamic_output = state;
    ele->dynamic_var_bak = state_var;
}

/* Element : --- */
static void calc_type_connection(int x, int y, cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    char state = 1;

    if (x != 0) {
        ele->dynamic_input = state_on_left(x, y, rung);
        state = ele->dynamic_input;
    }
    ele->dynamic_state = state;
    ele->dynamic_output = state;
}

/* Elements : -( )- and -(/)- */
static void calc_type_output(classicladder_rt_t *rt, int x, int y,
                             cl_rung_t *rung, char is_not) {
    cl_element_t *ele = &rung->elements[x][y];
    char state = state_on_left(x, y, rung);

    ele->dynamic_input = state;
    ele->dynamic_state = state;
    if (is_not)
        state = !state;
    write_var(rt, ele->var_type, ele->var_num, state);
}

/* Elements : -(S)- and -(R)- */
static void calc_type_output_set_reset(classicladder_rt_t *rt, int x, int y,
                                       cl_rung_t *rung, char is_reset) {
    cl_element_t *ele = &rung->elements[x][y];
    char state = state_on_left(x, y, rung);

    ele->dynamic_input = state;
    ele->dynamic_state = state;
    if (state)
        write_var(rt, ele->var_type, ele->var_num, is_reset ? 0 : 1);
}

/* Element : -(J)- — returns the rung to jump to, or -1. */
static int calc_type_output_jump(int x, int y, cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    char state = state_on_left(x, y, rung);
    int goto_rung = -1;

    if (state)
        goto_rung = ele->var_num;
    ele->dynamic_input = state;
    ele->dynamic_state = state;
    return goto_rung;
}

/* Find the section holding the sub-routine with this number, or -1. */
static int search_sub_routine_with_its_number(classicladder_rt_t *rt, int nbr) {
    for (int i = 0; i < rt->sizes.nbr_sections; i++) {
        if (rt->sections[i].used && rt->sections[i].sub_routine_number == nbr)
            return i;
    }
    return -1;
}

/* Element : -(C)- — returns the section to call, or -1. */
static int calc_type_output_call(classicladder_rt_t *rt, int x, int y,
                                 cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    char state = state_on_left(x, y, rung);
    int call_sr_section = -1;

    if (state)
        call_sr_section = search_sub_routine_with_its_number(rt, ele->var_num);
    ele->dynamic_input = state;
    ele->dynamic_state = state;
    return call_sr_section;
}

/* Element : Timer (2x2 block, head at the right column, inputs E and C on
 * rows y and y+1, outputs D and R on the same two rows).
 * LinuxCNC forces the (C) control input true when the block sits on the left
 * rail, so that ladders written before the control input existed still run. */
static void calc_type_timer(classicladder_rt_t *rt, int x, int y,
                            cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    cl_timer_t *timer;

    if (ele->var_num < 0 || ele->var_num >= rt->sizes.nbr_timers)
        return;
    timer = &rt->timers[ele->var_num];

    timer->input_enable = block_state_on_left(x, y, 2, rung);
    timer->input_control = block_state_on_left(x, y + 1, 2, rung);

    if (!timer->input_enable) {
        timer->output_running = 0;
        timer->output_done = 0;
        timer->value = timer->preset;
    } else {
        if (timer->value > 0) {
            if (timer->input_control) {
                timer->value -= rt->periodic_refresh_ms;
                timer->output_running = 1;
                timer->output_done = 0;
            }
        } else {
            timer->output_running = 0;
            timer->output_done = 1;
        }
    }
    set_block_output(rung, x, y, timer->output_done);
    set_block_output(rung, x, y + 1, timer->output_running);
}

/* Element : Monostable (2x2 block, head at the right column) */
static void calc_type_monostable(classicladder_rt_t *rt, int x, int y,
                                 cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    cl_monostable_t *mono;

    if (ele->var_num < 0 || ele->var_num >= rt->sizes.nbr_monostables)
        return;
    mono = &rt->monostables[ele->var_num];

    mono->input = block_state_on_left(x, y, 2, rung);

    /* Rising edge on the input starts it; it is not retriggerable. */
    if (mono->input && !mono->input_bak && mono->value == 0) {
        mono->output_running = 1;
        mono->value = mono->preset;
    }
    if (mono->value > 0)
        mono->value -= rt->periodic_refresh_ms;
    else
        mono->output_running = 0;
    mono->input_bak = mono->input;
    set_block_output(rung, x, y, mono->output_running);
}

/* Element : Counter (2x4 block, head at the right column; inputs R/P/U/D on
 * rows y..y+3, outputs E/D/F on rows y, y+1, y+2) */
static void calc_type_counter(classicladder_rt_t *rt, int x, int y,
                              cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    cl_counter_t *counter;
    int counter_nbr = ele->var_num;
    int current_value, preset_value;
    char done_result, empty_result, full_result;

    if (counter_nbr < 0 || counter_nbr >= rt->sizes.nbr_counters)
        return;
    counter = &rt->counters[counter_nbr];
    current_value = read_var(rt, CL_VAR_COUNTER_VALUE, counter_nbr);
    preset_value = read_var(rt, CL_VAR_COUNTER_PRESET, counter_nbr);

    counter->input_reset = block_state_on_left(x, y, 2, rung);
    counter->input_preset = block_state_on_left(x, y + 1, 2, rung);
    counter->input_count_up = block_state_on_left(x, y + 2, 2, rung);
    counter->input_count_down = block_state_on_left(x, y + 3, 2, rung);

    if (counter->input_count_up && counter->input_count_up_bak == 0) {
        counter->value_bak = current_value;
        current_value++;
        if (current_value > 9999)
            current_value = 0;
    }
    if (counter->input_count_down && counter->input_count_down_bak == 0) {
        counter->value_bak = current_value;
        current_value--;
        if (current_value < 0)
            current_value = 9999;
    }
    if (counter->input_preset) {
        counter->value_bak = current_value;
        current_value = preset_value;
    }
    if (counter->input_reset) {
        counter->value_bak = current_value;
        current_value = 0;
    }
    counter->input_count_up_bak = counter->input_count_up;
    counter->input_count_down_bak = counter->input_count_down;

    done_result = (current_value == preset_value) ? 1 : 0;
    empty_result = (current_value == 9999 && counter->value_bak == 0) ? 1 : 0;
    full_result = (current_value == 0 && counter->value_bak == 9999) ? 1 : 0;
    set_block_output(rung, x, y, empty_result);
    set_block_output(rung, x, y + 1, done_result);
    set_block_output(rung, x, y + 2, full_result);

    write_var(rt, CL_VAR_COUNTER_DONE, counter_nbr, done_result);
    write_var(rt, CL_VAR_COUNTER_EMPTY, counter_nbr, empty_result);
    write_var(rt, CL_VAR_COUNTER_FULL, counter_nbr, full_result);
    write_var(rt, CL_VAR_COUNTER_PRESET, counter_nbr, preset_value);
    write_var(rt, CL_VAR_COUNTER_VALUE, counter_nbr, current_value);
}

/* Element : IEC timer (2x2 block, head at the right column). Its preset and
 * value are counted in base units, not milliseconds. */
static void calc_type_timer_iec(classicladder_rt_t *rt, int x, int y,
                                cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    cl_timer_iec_t *timer_iec;
    int timer_nbr = ele->var_num;
    int current_value, preset_value;
    char output_result;
    char do_inc_time = 0;

    if (timer_nbr < 0 || timer_nbr >= rt->sizes.nbr_timers_iec)
        return;
    timer_iec = &rt->timers_iec[timer_nbr];
    current_value = read_var(rt, CL_VAR_TIMER_IEC_VALUE, timer_nbr);
    preset_value = read_var(rt, CL_VAR_TIMER_IEC_PRESET, timer_nbr);
    output_result = read_var(rt, CL_VAR_TIMER_IEC_DONE, timer_nbr);

    timer_iec->input = block_state_on_left(x, y, 2, rung);

    switch (timer_iec->timer_mode) {
    case CL_TIMER_IEC_TON:
        if (!timer_iec->input) {
            output_result = 0;
            current_value = 0;
        } else {
            if (current_value < preset_value)
                do_inc_time = 1;
            else
                output_result = 1;
        }
        break;
    case CL_TIMER_IEC_TOF:
        if (timer_iec->input) {
            output_result = 1;
            current_value = 0;
            timer_iec->timer_started = 0;
        } else {
            /* falling edge on the input starts the off-delay */
            if (timer_iec->input_bak)
                timer_iec->timer_started = 1;
        }
        break;
    case CL_TIMER_IEC_TP:
        /* rising edge on the input; the pulse is not retriggerable */
        if (timer_iec->input && !timer_iec->input_bak &&
            timer_iec->timer_started == 0) {
            output_result = 1;
            current_value = 0;
            timer_iec->timer_started = 1;
        }
        break;
    }

    if (timer_iec->timer_mode == CL_TIMER_IEC_TOF ||
        timer_iec->timer_mode == CL_TIMER_IEC_TP) {
        if (timer_iec->timer_started) {
            if (current_value < preset_value) {
                do_inc_time = 1;
            } else {
                output_result = 0;
                current_value = 0;
                timer_iec->timer_started = 0;
            }
        }
    }

    if (do_inc_time) {
        timer_iec->value_to_reach_one_base_unit += rt->periodic_refresh_ms;
        if (timer_iec->value_to_reach_one_base_unit >= cl_base_ms(timer_iec->base)) {
            current_value++;
            /* keep the little too-much time part */
            timer_iec->value_to_reach_one_base_unit -= cl_base_ms(timer_iec->base);
        }
    }
    timer_iec->input_bak = timer_iec->input;
    set_block_output(rung, x, y, output_result);

    write_var(rt, CL_VAR_TIMER_IEC_DONE, timer_nbr, output_result);
    write_var(rt, CL_VAR_TIMER_IEC_PRESET, timer_nbr, preset_value);
    write_var(rt, CL_VAR_TIMER_IEC_VALUE, timer_nbr, current_value);
}

/* Element : Compare (3 cells wide, head at the right column) */
static void calc_type_compar(classicladder_rt_t *rt, int x, int y,
                             cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    char state_element = cl_eval_compare(rt, ele->var_num) ? 1 : 0;
    char state;

    ele->dynamic_state = state_element;
    ele->dynamic_input = block_state_on_left(x, y, 3, rung);
    state = state_element && ele->dynamic_input;
    ele->dynamic_output = state;
}

/* Element : Operate (3 cells wide, head at the right column) */
static void calc_type_output_operate(classicladder_rt_t *rt, int x, int y,
                                     cl_rung_t *rung) {
    cl_element_t *ele = &rung->elements[x][y];
    char state = block_state_on_left(x, y, 3, rung);

    if (state)
        cl_eval_operate(rt, ele->var_num);
    ele->dynamic_input = state;
    ele->dynamic_state = state;
}

/* Evaluate one rung, column by column. Sets *jump_to to the rung a taken (J)
 * coil targets, or -1. Sub-routine (C) coils are refreshed recursively here,
 * which is why the call depth and the iteration budget have to be threaded
 * through. */
static void refresh_a_section(classicladder_rt_t *rt, cl_section_t *section,
                              int depth, int *budget);

static void refresh_rung(classicladder_rt_t *rt, cl_rung_t *rung, int *jump_to,
                         int depth, int *budget) {
    int jump_to_rung = -1;

    for (int x = 0; x < CL_RUNG_WIDTH && jump_to_rung == -1; x++) {
        for (int y = 0; y < CL_RUNG_HEIGHT && jump_to_rung == -1; y++) {
            cl_element_t *ele = &rung->elements[x][y];
            switch (ele->type) {
            case CL_ELE_FREE:
            case CL_ELE_UNUSABLE:
                /* No output of their own, but the drawing wants the input. */
                ele->dynamic_input = state_on_left(x, y, rung);
                break;
            case CL_ELE_INPUT:
                calc_type_input(rt, x, y, rung, 0, 0);
                break;
            case CL_ELE_INPUT_NOT:
                calc_type_input(rt, x, y, rung, 1, 0);
                break;
            case CL_ELE_RISING_INPUT:
                calc_type_input(rt, x, y, rung, 0, 1);
                break;
            case CL_ELE_FALLING_INPUT:
                calc_type_input(rt, x, y, rung, 1, 1);
                break;
            case CL_ELE_CONNECTION:
                calc_type_connection(x, y, rung);
                break;
            case CL_ELE_TIMER:
                calc_type_timer(rt, x, y, rung);
                break;
            case CL_ELE_MONOSTABLE:
                calc_type_monostable(rt, x, y, rung);
                break;
            case CL_ELE_COUNTER:
                calc_type_counter(rt, x, y, rung);
                break;
            case CL_ELE_TIMER_IEC:
                calc_type_timer_iec(rt, x, y, rung);
                break;
            case CL_ELE_COMPAR:
                calc_type_compar(rt, x, y, rung);
                break;
            case CL_ELE_OUTPUT:
                calc_type_output(rt, x, y, rung, 0);
                break;
            case CL_ELE_OUTPUT_NOT:
                calc_type_output(rt, x, y, rung, 1);
                break;
            case CL_ELE_OUTPUT_SET:
                calc_type_output_set_reset(rt, x, y, rung, 0);
                break;
            case CL_ELE_OUTPUT_RESET:
                calc_type_output_set_reset(rt, x, y, rung, 1);
                break;
            case CL_ELE_OUTPUT_JUMP:
                /* abort the rest of the rung immediately */
                jump_to_rung = calc_type_output_jump(x, y, rung);
                break;
            case CL_ELE_OUTPUT_CALL: {
                int section_to_call = calc_type_output_call(rt, x, y, rung);
                if (section_to_call != -1) {
                    cl_section_t *sub = &rt->sections[section_to_call];
                    if (sub->used && sub->sub_routine_number >= 0)
                        refresh_a_section(rt, sub, depth + 1, budget);
                }
                break;
            }
            case CL_ELE_OUTPUT_OPERATE:
                calc_type_output_operate(rt, x, y, rung);
                break;
            default:
                break;
            }
        }
    }

    *jump_to = jump_to_rung;
}

/* Refresh every rung of a section, following (J)ump coils within it. */
static void refresh_a_section(classicladder_rt_t *rt, cl_section_t *section,
                              int depth, int *budget) {
    int num_rung = section->first_rung;
    int done = 0;

    /* 2.9 recurses without a bound; in an RT thread the stack is not ours to
     * blow, so a runaway (C)all chain stops the PLC like a runaway jump. */
    if (depth > CL_MAX_SR_DEPTH) {
        atomic_store_explicit(&rt->state, CL_STATE_STOP, memory_order_release);
        return;
    }
    if (num_rung < 0 || num_rung >= rt->sizes.nbr_rungs)
        return;

    do {
        int goto_rung;

        /* 2.9 counts only jumps here, which leaves a corrupt next-rung chain
         * free to spin forever; in an RT thread that is fatal, so every
         * iteration counts against the limit. The budget is shared down the
         * whole (C)all tree of one top-level section: a per-invocation
         * counter would let each caller iteration re-run the callee's full
         * allowance — 99999^depth rung evaluations, hours of a wedged HAL
         * thread — and a tripped child would go unnoticed by a parent loop
         * that never re-reads the state it stopped. */
        if (--(*budget) < 0) {
            atomic_store_explicit(&rt->state, CL_STATE_STOP, memory_order_release);
            return;
        }

        refresh_rung(rt, &rt->rungs[num_rung], &goto_rung, depth, budget);

        if (goto_rung != -1) {
            if (goto_rung < 0 || goto_rung >= rt->sizes.nbr_rungs ||
                !rt->rungs[goto_rung].used) {
                /* jump to an undefined rung — give up on this section */
                done = 1;
            } else {
                num_rung = goto_rung;
            }
        } else {
            if (num_rung == section->last_rung) {
                done = 1;
            } else {
                num_rung = rt->rungs[num_rung].next_rung;
                if (num_rung < 0 || num_rung >= rt->sizes.nbr_rungs)
                    done = 1;
            }
        }
    } while (!done);
}

/* Refresh every main section, in the order they are defined. Sub-routine
 * sections run only when a (C)all coil reaches them. */
static void refresh_all_sections(classicladder_rt_t *rt) {
    for (int s = 0; s < rt->sizes.nbr_sections; s++) {
        cl_section_t *sec = &rt->sections[s];

        if (!sec->used)
            continue;

        if (sec->language == CL_SECTION_LADDER && sec->sub_routine_number == -1) {
            int budget = CL_MAD_LOOP_LIMIT;
            refresh_a_section(rt, sec, 0, &budget);
        }

        if (sec->language == CL_SECTION_SEQUENTIAL)
            cl_refresh_sequential_page(rt, sec->sequential_page);
    }
}

/* --- Prepare all data before a run (2.9 PrepareAllDatasBeforeRun) --- */

static void prepare_timers(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_timers; i++) {
        cl_timer_t *t = &rt->timers[i];
        t->value = t->preset;
        t->input_enable = 0;
        t->input_control = 0;
        t->output_done = 0;
        t->output_running = 0;
    }
}

static void prepare_monostables(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_monostables; i++) {
        cl_monostable_t *m = &rt->monostables[i];
        m->value = 0;
        m->input = 0;
        m->input_bak = 0;
        m->output_running = 0;
    }
}

static void prepare_counters(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_counters; i++) {
        cl_counter_t *c = &rt->counters[i];
        c->value = 0;
        c->value_bak = 0;
        c->input_reset = 0;
        c->input_preset = 0;
        c->input_count_up = 0;
        c->input_count_up_bak = 0;
        c->input_count_down = 0;
        c->input_count_down_bak = 0;
        c->output_done = 0;
        c->output_empty = 0;
        c->output_full = 0;
    }
}

static void prepare_timers_iec(classicladder_rt_t *rt) {
    for (int i = 0; i < rt->sizes.nbr_timers_iec; i++) {
        cl_timer_iec_t *t = &rt->timers_iec[i];
        t->value = 0;
        t->input = 0;
        t->input_bak = 0;
        t->output = 0;
        t->timer_started = 0;
        t->value_to_reach_one_base_unit = 0;
    }
}

/* Seed DynamicVarBak so that an edge contact does not fire on the first scan
 * just because its variable happens to be true already. */
static void prepare_rungs(classicladder_rt_t *rt) {
    for (int r = 0; r < rt->sizes.nbr_rungs; r++) {
        for (int x = 0; x < CL_RUNG_WIDTH; x++) {
            for (int y = 0; y < CL_RUNG_HEIGHT; y++) {
                cl_element_t *ele = &rt->rungs[r].elements[x][y];
                if (ele->type == CL_ELE_RISING_INPUT ||
                    ele->type == CL_ELE_FALLING_INPUT) {
                    char state = read_var(rt, ele->var_type, ele->var_num) ? 1 : 0;
                    if (ele->type == CL_ELE_FALLING_INPUT)
                        state = !state;
                    ele->dynamic_var_bak = state;
                }
            }
        }
    }
}

void cl_prepare_all_datas_before_run(classicladder_rt_t *rt) {
    prepare_timers(rt);
    prepare_monostables(rt);
    prepare_counters(rt);
    prepare_timers_iec(rt);
    prepare_rungs(rt);
    cl_prepare_sequential(rt);
}

/* --- Public API --- */

void cl_scan(classicladder_rt_t *rt, int ms_elapsed) {
    rt->periodic_refresh_ms = ms_elapsed;
    refresh_all_sections(rt);
}

void classicladder_refresh(void *arg, long period) {
    classicladder_rt_t *rt = (classicladder_rt_t *)arg;

    /* The accumulator is per instance: two classicladders in the same thread
     * must not share the sub-millisecond remainder. */
    rt->period_leftover_ns += (unsigned long)period;
    unsigned long ms = rt->period_leftover_ns / 1000000;
    rt->period_leftover_ns %= 1000000;

    if (ms < 1)
        return;

    /* Raise `scanning` before reading `state` (both seq_cst): a writer that
     * publishes STOP and then finds `scanning` low knows no scan that saw RUN
     * can still be in flight. */
    atomic_store(&rt->scanning, 1);
    int state = atomic_load(&rt->state);
    if (state != CL_STATE_RUN) {
        atomic_store(&rt->scanning, 0);
        return;
    }

    unsigned long t0 = rtapi_get_time();

    /* Read HAL inputs → internal variables */
    read_hal_inputs(rt);
    read_hal_s32_inputs(rt);
    read_hal_float_inputs(rt);

    /* Evaluate all main sections, timed by the actual thread period. Timers,
     * monostables, counters and IEC timers are evaluated inline by the rungs
     * that hold them. */
    cl_scan(rt, (int)ms);

    /* Write internal variables → HAL outputs */
    write_hal_outputs(rt);
    write_hal_s32_outputs(rt);
    write_hal_float_outputs(rt);

    unsigned long t1 = rtapi_get_time();
    atomic_store_explicit(&rt->duration_of_last_scan_ns,
                         (int32_t)(t1 - t0), memory_order_relaxed);
    atomic_store(&rt->scanning, 0);
}

/* Region starts are aligned generously so every array type is satisfied
 * regardless of the order the regions are laid out in. */
static size_t cl_align_up(size_t n) {
    return (n + 15) & ~(size_t)15;
}

static int cl_size_ok(int n) {
    return n >= 0 && n <= CL_SIZE_LIMIT;
}

classicladder_rt_t *classicladder_rt_alloc(const cl_sizes_t *sizes) {
    /* Validate every count before it enters the offset math.  The scan reads
     * rungs[first_rung] and sections[0] unconditionally, so those two must
     * exist even in an empty program. */
    if (sizes->nbr_rungs < 1 || sizes->nbr_sections < 1)
        return NULL;
    if (!cl_size_ok(sizes->nbr_rungs) || !cl_size_ok(sizes->nbr_bits) ||
        !cl_size_ok(sizes->nbr_words) || !cl_size_ok(sizes->nbr_timers) ||
        !cl_size_ok(sizes->nbr_monostables) || !cl_size_ok(sizes->nbr_counters) ||
        !cl_size_ok(sizes->nbr_timers_iec) || !cl_size_ok(sizes->nbr_phys_inputs) ||
        !cl_size_ok(sizes->nbr_phys_outputs) || !cl_size_ok(sizes->nbr_arithm_expr) ||
        !cl_size_ok(sizes->nbr_sections) || !cl_size_ok(sizes->nbr_symbols) ||
        !cl_size_ok(sizes->nbr_s32_in) || !cl_size_ok(sizes->nbr_s32_out) ||
        !cl_size_ok(sizes->nbr_float_in) || !cl_size_ok(sizes->nbr_float_out) ||
        !cl_size_ok(sizes->nbr_error_bits))
        return NULL;

    const size_t bits_count = (size_t)sizes->nbr_bits + sizes->nbr_phys_inputs +
                              sizes->nbr_phys_outputs + CL_MAX_STEPS +
                              sizes->nbr_error_bits;
    const size_t words_count = (size_t)sizes->nbr_words + sizes->nbr_s32_in +
                               sizes->nbr_s32_out + CL_MAX_STEPS;
    const size_t floats_count = (size_t)sizes->nbr_float_in + sizes->nbr_float_out;

    /* One block: the header struct followed by every sized array. */
    size_t total = cl_align_up(sizeof(classicladder_rt_t));
    const size_t o_rungs = total;
    total = cl_align_up(total + sizeof(cl_rung_t) * sizes->nbr_rungs);
    const size_t o_sections = total;
    total = cl_align_up(total + sizeof(cl_section_t) * sizes->nbr_sections);
    const size_t o_arithm = total;
    total = cl_align_up(total + sizeof(cl_arithm_expr_t) * sizes->nbr_arithm_expr);
    const size_t o_compiled = total;
    total = cl_align_up(total + sizeof(cl_compiled_expr_t) * sizes->nbr_arithm_expr);
    const size_t o_timers = total;
    total = cl_align_up(total + sizeof(cl_timer_t) * sizes->nbr_timers);
    const size_t o_monostables = total;
    total = cl_align_up(total + sizeof(cl_monostable_t) * sizes->nbr_monostables);
    const size_t o_counters = total;
    total = cl_align_up(total + sizeof(cl_counter_t) * sizes->nbr_counters);
    const size_t o_timers_iec = total;
    total = cl_align_up(total + sizeof(cl_timer_iec_t) * sizes->nbr_timers_iec);
    const size_t o_var_bits = total;
    total = cl_align_up(total + sizeof(char) * bits_count);
    const size_t o_var_words = total;
    total = cl_align_up(total + sizeof(int32_t) * words_count);
    const size_t o_var_floats = total;
    total = cl_align_up(total + sizeof(double) * floats_count);
    const size_t o_symbols = total;
    total = cl_align_up(total + sizeof(cl_symbol_t) * sizes->nbr_symbols);
    const size_t o_hal_in = total;
    total = cl_align_up(total + sizeof(hal_bit_t *) * sizes->nbr_phys_inputs);
    const size_t o_hal_out = total;
    total = cl_align_up(total + sizeof(hal_bit_t *) * sizes->nbr_phys_outputs);
    const size_t o_hal_s32_in = total;
    total = cl_align_up(total + sizeof(hal_s32_t *) * sizes->nbr_s32_in);
    const size_t o_hal_s32_out = total;
    total = cl_align_up(total + sizeof(hal_s32_t *) * sizes->nbr_s32_out);
    const size_t o_hal_float_in = total;
    total = cl_align_up(total + sizeof(hal_float_t *) * sizes->nbr_float_in);
    const size_t o_hal_float_out = total;
    total = cl_align_up(total + sizeof(hal_float_t *) * sizes->nbr_float_out);

    char *base = (char *)calloc(1, total);
    if (!base)
        return NULL;

    classicladder_rt_t *rt = (classicladder_rt_t *)base;
    rt->sizes = *sizes;
    rt->var_bits_count = (int)bits_count;
    rt->var_words_count = (int)words_count;
    rt->var_floats_count = (int)floats_count;
    rt->rungs = (cl_rung_t *)(base + o_rungs);
    rt->sections = (cl_section_t *)(base + o_sections);
    rt->arithm_exprs = (cl_arithm_expr_t *)(base + o_arithm);
    rt->compiled_exprs = (cl_compiled_expr_t *)(base + o_compiled);
    rt->timers = (cl_timer_t *)(base + o_timers);
    rt->monostables = (cl_monostable_t *)(base + o_monostables);
    rt->counters = (cl_counter_t *)(base + o_counters);
    rt->timers_iec = (cl_timer_iec_t *)(base + o_timers_iec);
    rt->var_bits = base + o_var_bits;
    rt->var_words = (int32_t *)(base + o_var_words);
    rt->var_floats = (double *)(base + o_var_floats);
    rt->symbols = (cl_symbol_t *)(base + o_symbols);
    rt->hal_inputs = (hal_bit_t **)(base + o_hal_in);
    rt->hal_outputs = (hal_bit_t **)(base + o_hal_out);
    rt->hal_s32_inputs = (hal_s32_t **)(base + o_hal_s32_in);
    rt->hal_s32_outputs = (hal_s32_t **)(base + o_hal_s32_out);
    rt->hal_float_inputs = (hal_float_t **)(base + o_hal_float_in);
    rt->hal_float_outputs = (hal_float_t **)(base + o_hal_float_out);

    atomic_store(&rt->state, CL_STATE_STOP);
    atomic_store(&rt->generation, 0);
    return rt;
}

void classicladder_rt_free(classicladder_rt_t *rt) {
    if (rt)
        free(rt);
}

void classicladder_rt_init_data(classicladder_rt_t *rt) {
    memset(rt->var_bits, 0, sizeof(char) * rt->var_bits_count);
    memset(rt->var_words, 0, sizeof(int32_t) * rt->var_words_count);
    memset(rt->var_floats, 0, sizeof(double) * rt->var_floats_count);
    memset(rt->timers, 0, sizeof(cl_timer_t) * rt->sizes.nbr_timers);
    memset(rt->monostables, 0, sizeof(cl_monostable_t) * rt->sizes.nbr_monostables);
    memset(rt->counters, 0, sizeof(cl_counter_t) * rt->sizes.nbr_counters);
    memset(rt->timers_iec, 0, sizeof(cl_timer_iec_t) * rt->sizes.nbr_timers_iec);
    /* Initialize SFC: set all steps/transitions to unused */
    for (int i = 0; i < CL_MAX_STEPS; i++) {
        rt->steps[i].num_page = -1;
        rt->steps[i].activated = 0;
        rt->steps[i].time_activated = 0;
    }
    for (int i = 0; i < CL_MAX_TRANSITIONS; i++) {
        rt->transitions[i].num_page = -1;
        for (int j = 0; j < CL_MAX_SWITCHS; j++) {
            rt->transitions[i].num_step_to_activ[j] = -1;
            rt->transitions[i].num_step_to_desactiv[j] = -1;
            rt->transitions[i].num_trans_linked_for_start[j] = -1;
            rt->transitions[i].num_trans_linked_for_end[j] = -1;
        }
    }
    for (int i = 0; i < CL_MAX_SEQ_COMMENTS; i++) {
        rt->seq_comments[i].num_page = -1;
    }
    /* Default time bases, in milliseconds */
    for (int i = 0; i < rt->sizes.nbr_timers; i++) {
        rt->timers[i].base = CL_TIME_BASE_SECS;
    }
    for (int i = 0; i < rt->sizes.nbr_monostables; i++) {
        rt->monostables[i].base = CL_TIME_BASE_SECS;
    }
    for (int i = 0; i < rt->sizes.nbr_timers_iec; i++) {
        rt->timers_iec[i].base = CL_TIME_BASE_SECS;
        rt->timers_iec[i].timer_mode = CL_TIMER_IEC_TON;
    }
}

void write_var_ext(classicladder_rt_t *rt, int type, int offset, int value) {
    write_var(rt, type, offset, value);
}

int read_var_ext(classicladder_rt_t *rt, int type, int offset) {
    return read_var(rt, type, offset);
}

/* --- Bytecode expression evaluator (RT-safe: no malloc, no string ops) --- */

static int ipow(int base, int exp) {
    if (exp < 0) return 0;
    int result = 1;
    while (exp > 0) {
        if (exp & 1) result *= base;
        base *= base;
        exp >>= 1;
    }
    return result;
}

static int eval_bytecode(classicladder_rt_t *rt, const cl_compiled_expr_t *ce) {
    int32_t stack[CL_EXPR_STACK_DEPTH];
    int sp = 0;
    int a, b;

    for (int i = 0; i < ce->len; i++) {
        const cl_instruction_t *ins = &ce->code[i];
        switch (ins->opcode) {
        case CL_OP_PUSH_CONST:
            if (sp >= CL_EXPR_STACK_DEPTH) return 0;
            stack[sp++] = ins->operand;
            break;
        case CL_OP_LOAD_VAR:
            if (sp >= CL_EXPR_STACK_DEPTH) return 0;
            stack[sp++] = read_var(rt, (ins->operand >> 16) & 0xFFFF,
                                   ins->operand & 0xFFFF);
            break;
        case CL_OP_LOAD_VAR_IDX: {
            if (sp < 1) return 0;
            a = stack[--sp]; /* index value */
            if (sp >= CL_EXPR_STACK_DEPTH) return 0;
            /* The add must not overflow int before read_var can range-check
             * the result; a hostile index is a zero read, not UB. */
            int64_t idx = (int64_t)(ins->operand & 0xFFFF) + a;
            stack[sp++] = (idx < 0 || idx > INT32_MAX)
                              ? 0
                              : read_var(rt, (ins->operand >> 16) & 0xFFFF,
                                         (int)idx);
            break;
        }
        case CL_OP_STORE_VAR:
            if (sp < 1) return 0;
            a = stack[--sp];
            write_var(rt, (ins->operand >> 16) & 0xFFFF,
                      ins->operand & 0xFFFF, a);
            break;
        case CL_OP_STORE_VAR_IDX: {
            if (sp < 2) return 0;
            a = stack[--sp]; /* value */
            b = stack[--sp]; /* index */
            int64_t idx = (int64_t)(ins->operand & 0xFFFF) + b;
            if (idx >= 0 && idx <= INT32_MAX)
                write_var(rt, (ins->operand >> 16) & 0xFFFF, (int)idx, a);
            break;
        }
        case CL_OP_ADD:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] + stack[sp]; break;
        case CL_OP_SUB:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] - stack[sp]; break;
        case CL_OP_MUL:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] * stack[sp]; break;
        /* A zero divisor leaves the left operand as the result — 2.9 skips
         * the operation entirely (arithm_eval.c), and only that choice keeps
         * the differential oracle quiet. The int64 detour makes INT32_MIN
         * / -1 defined (it wraps) instead of a SIGFPE from an expression. */
        case CL_OP_DIV:
            if (sp < 2) return 0;
            sp--;
            if (stack[sp])
                stack[sp-1] = (int32_t)(uint32_t)((int64_t)stack[sp-1] / stack[sp]);
            break;
        case CL_OP_MOD:
            if (sp < 2) return 0;
            sp--;
            if (stack[sp])
                stack[sp-1] = (int32_t)(uint32_t)((int64_t)stack[sp-1] % stack[sp]);
            break;
        case CL_OP_POW:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = ipow(stack[sp-1], stack[sp]); break;
        case CL_OP_AND:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] & stack[sp]; break;
        case CL_OP_OR:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] | stack[sp]; break;
        case CL_OP_XOR:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] ^ stack[sp]; break;
        case CL_OP_NOT:
            if (sp < 1) return 0;
            stack[sp-1] = !stack[sp-1]; break;
        case CL_OP_NEG:
            if (sp < 1) return 0;
            stack[sp-1] = -stack[sp-1]; break;
        case CL_OP_CMP_LT:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] < stack[sp]; break;
        case CL_OP_CMP_GT:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] > stack[sp]; break;
        case CL_OP_CMP_EQ:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] == stack[sp]; break;
        case CL_OP_CMP_LE:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] <= stack[sp]; break;
        case CL_OP_CMP_GE:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] >= stack[sp]; break;
        case CL_OP_CMP_NE:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] != stack[sp]; break;
        case CL_OP_ABS:
            if (sp < 1) return 0;
            stack[sp-1] = stack[sp-1] < 0 ? -stack[sp-1] : stack[sp-1]; break;
        case CL_OP_MINI:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] < stack[sp] ? stack[sp-1] : stack[sp]; break;
        case CL_OP_MAXI:
            if (sp < 2) return 0;
            sp--; stack[sp-1] = stack[sp-1] > stack[sp] ? stack[sp-1] : stack[sp]; break;
        default:
            return 0;
        }
    }
    return sp > 0 ? stack[0] : 0;
}

int cl_eval_compare(classicladder_rt_t *rt, int expr_index) {
    if (expr_index < 0 || expr_index >= rt->sizes.nbr_arithm_expr)
        return 0;
    const cl_compiled_expr_t *ce = &rt->compiled_exprs[expr_index];
    if (!ce->valid || ce->len == 0)
        return 0;
    return eval_bytecode(rt, ce) ? 1 : 0;
}

void cl_eval_operate(classicladder_rt_t *rt, int expr_index) {
    if (expr_index < 0 || expr_index >= rt->sizes.nbr_arithm_expr)
        return;
    const cl_compiled_expr_t *ce = &rt->compiled_exprs[expr_index];
    if (!ce->valid || ce->len == 0)
        return;
    eval_bytecode(rt, ce);
}

/* --- Sequential Function Chart (SFC/Grafcet) evaluation --- */

void cl_prepare_sequential(classicladder_rt_t *rt) {
    for (int i = 0; i < CL_MAX_STEPS; i++) {
        rt->steps[i].activated = 0;
        rt->steps[i].time_activated = 0;
        if (rt->steps[i].init_step && rt->steps[i].num_page >= 0)
            rt->steps[i].activated = 1;
    }
    for (int i = 0; i < CL_MAX_TRANSITIONS; i++) {
        rt->transitions[i].activated = 0;
    }
}

static int refresh_transition(classicladder_rt_t *rt, cl_transition_t *trans) {
    /* Read transition condition */
    trans->activated = read_var(rt, trans->var_type_condi, trans->var_num_condi);
    if (!trans->activated)
        return 0;

    /* Check all steps to deactivate are currently active. Deliberate
     * divergence from 2.9: a transition whose deactivate list names no valid
     * step never fires here. 2.9 read the empty list as "all upstream steps
     * are active", so such a transition fired on every scan, reported a state
     * change every pass, and ran the settle loop to its full 50 iterations —
     * measured at 20x the scan cost. The validator refuses building one
     * through the API; this closes the same hole for entries a lenient .clp
     * load canonicalized away. */
    int all_on = 1;
    int valid_steps = 0;
    for (int i = 0; i < CL_MAX_SWITCHS && trans->num_step_to_desactiv[i] != -1; i++) {
        int step_idx = trans->num_step_to_desactiv[i];
        if (step_idx >= 0 && step_idx < CL_MAX_STEPS) {
            valid_steps++;
            if (!rt->steps[step_idx].activated) {
                all_on = 0;
                break;
            }
        }
    }

    if (!all_on || valid_steps == 0)
        return 0;

    /* Transition fires: deactivate upstream steps */
    for (int i = 0; i < CL_MAX_SWITCHS && trans->num_step_to_desactiv[i] != -1; i++) {
        int step_idx = trans->num_step_to_desactiv[i];
        if (step_idx >= 0 && step_idx < CL_MAX_STEPS)
            rt->steps[step_idx].activated = 0;
    }

    /* Activate downstream steps */
    for (int i = 0; i < CL_MAX_SWITCHS && trans->num_step_to_activ[i] != -1; i++) {
        int step_idx = trans->num_step_to_activ[i];
        if (step_idx >= 0 && step_idx < CL_MAX_STEPS)
            rt->steps[step_idx].activated = 1;
    }

    return 1; /* state changed */
}

static void refresh_steps_vars(classicladder_rt_t *rt) {
    for (int i = 0; i < CL_MAX_STEPS; i++) {
        cl_step_t *step = &rt->steps[i];
        if (step->num_page < 0)
            continue;
        if (step->activated)
            step->time_activated += rt->periodic_refresh_ms;
        else
            step->time_activated = 0;
        write_var(rt, CL_VAR_STEP_ACTIVITY, step->step_number, step->activated);
        write_var(rt, CL_VAR_STEP_TIME, step->step_number, step->time_activated / 1000);
    }
}

void cl_refresh_sequential_page(classicladder_rt_t *rt, int page_nbr) {
    int state_changed;
    int loop_safety = 0;
    do {
        state_changed = 0;
        for (int i = 0; i < CL_MAX_TRANSITIONS; i++) {
            cl_transition_t *trans = &rt->transitions[i];
            if (trans->num_page != page_nbr)
                continue;
            if (refresh_transition(rt, trans))
                state_changed = 1;
        }
        loop_safety++;
    } while (state_changed && loop_safety < 50);
    refresh_steps_vars(rt);
}
