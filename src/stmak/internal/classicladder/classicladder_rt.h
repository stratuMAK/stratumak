/* classicladder_rt.h — Shared data structures between RT C code and Go.
 *
 * The classicladder_rt_t struct is allocated by Go and contains all PLC
 * data. RT reads HAL pins, evaluates the ladder, and writes HAL pins.
 * Go handles file I/O, API dispatch, and config changes.
 *
 * Based on Classic Ladder Project by Marc Le Douarain.
 * Copyright (C) Marc Le Douarain <marc.le.douarain@free.fr> (LGPL 2.1+)
 * Adapted for LinuxCNC stratuMAK architecture.
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 *
 * This program is free software; you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License v2.1+.
 */

#ifndef CLASSICLADDER_RT_H
#define CLASSICLADDER_RT_H

#include <stdint.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <string.h>

#include "hal.h"
#include "rtapi.h"

/* --- Sizing defaults --- *
 *
 * The size-configurable arrays are allocated to their exact configured count
 * by classicladder_rt_alloc (2.9 sized its shared memory the same way), so
 * the CL_MAX_* names below are only the defaults a `load classicladder` line
 * gets without an explicit numXxx= argument.  The sequential chart arrays
 * (steps, transitions, comments, pages) stay compile-time fixed: they are not
 * configurable, and the variable regions derived from CL_MAX_STEPS must mean
 * the same thing in every project file. */

#define CL_DEF_RUNGS            100
#define CL_DEF_BITS             100
#define CL_DEF_WORDS            100
#define CL_DEF_TIMERS           10
#define CL_DEF_MONOSTABLES      10
#define CL_DEF_COUNTERS         10
#define CL_DEF_TIMERS_IEC       10
#define CL_DEF_PHYS_INPUTS      15
#define CL_DEF_PHYS_OUTPUTS     15
#define CL_DEF_ARITHM_EXPR      100
#define CL_DEF_SECTIONS         10
#define CL_DEF_SYMBOLS          200
#define CL_DEF_S32_IN           10
#define CL_DEF_S32_OUT          10
#define CL_DEF_FLOAT_IN         10
#define CL_DEF_FLOAT_OUT        10
#define CL_DEF_ERROR_BITS       10

/* Ceiling on every configured count — not a semantic limit, just a sanity
 * cap so a typo'd load line fails at load time instead of allocating
 * gigabytes or overflowing the offset math. */
#define CL_SIZE_LIMIT           100000

#define CL_MAX_STEPS            128
#define CL_MAX_TRANSITIONS      256
#define CL_MAX_SWITCHS          10
#define CL_MAX_SEQ_COMMENTS     50
#define CL_MAX_SEQ_PAGES        5
#define CL_SEQ_COMMENT_LGT      51

/* Chart page grid. Editors put steps on odd rows and transitions on even ones,
 * so a step and the transition below it are a row apart; the engine reads no
 * position at all, so that is a drawing convention and not a rule. */
#define CL_SEQ_PAGE_WIDTH       16
#define CL_SEQ_PAGE_HEIGHT      16

/* Rung grid dimensions */
#define CL_RUNG_WIDTH  10
#define CL_RUNG_HEIGHT 6

/* Runaway guards for the two ways a ladder can fail to terminate: a (J)ump
 * coil looping forever within a section, and a (C)all coil recursing through
 * sub-routines. Hitting either stops the PLC. */
#define CL_MAD_LOOP_LIMIT 99999
#define CL_MAX_SR_DEPTH   25

/* Arithmetic expression max length */
#define CL_ARITHM_EXPR_SIZE 50

/* Bytecode VM constants */
#define CL_EXPR_MAX_CODE    128  /* max bytecode instructions per expression */
#define CL_EXPR_STACK_DEPTH 16   /* evaluation stack depth */

/* Bytecode opcodes */
enum cl_opcode {
    CL_OP_NOP = 0,
    CL_OP_PUSH_CONST,      /* operand: int32 constant */
    CL_OP_LOAD_VAR,        /* operand: (type<<16 | offset) */
    CL_OP_LOAD_VAR_IDX,    /* operand: (type<<16 | base_offset), index from stack top */
    CL_OP_STORE_VAR,       /* operand: (type<<16 | offset) — pops value from stack */
    CL_OP_STORE_VAR_IDX,   /* operand: (type<<16 | base_offset), index+value from stack */
    CL_OP_ADD,
    CL_OP_SUB,
    CL_OP_MUL,
    CL_OP_DIV,
    CL_OP_MOD,
    CL_OP_POW,
    CL_OP_AND,             /* bitwise */
    CL_OP_OR,              /* bitwise */
    CL_OP_XOR,             /* bitwise */
    CL_OP_NOT,             /* logical not (unary) */
    CL_OP_NEG,             /* arithmetic negate (unary) */
    CL_OP_CMP_LT,
    CL_OP_CMP_GT,
    CL_OP_CMP_EQ,
    CL_OP_CMP_LE,
    CL_OP_CMP_GE,
    CL_OP_CMP_NE,
    CL_OP_ABS,             /* unary: abs(top) */
    CL_OP_MINI,            /* binary: min(a,b) */
    CL_OP_MAXI,            /* binary: max(a,b) */
};

/* A single bytecode instruction */
typedef struct {
    uint8_t  opcode;       /* cl_opcode */
    int32_t  operand;      /* constant value, or packed var ref */
} cl_instruction_t;

/* Compiled expression (lives alongside the source string) */
typedef struct {
    uint8_t          kind;   /* 0=compare, 1=operate */
    uint8_t          len;    /* number of instructions */
    uint8_t          valid;  /* 1 if compilation succeeded */
    cl_instruction_t code[CL_EXPR_MAX_CODE];
} cl_compiled_expr_t;

/* Labels/comments */
#define CL_LGT_LABEL   10
#define CL_LGT_COMMENT 30

/* Symbol table */
#define CL_LGT_VAR_NAME       10
#define CL_LGT_SYMBOL_STRING  10
#define CL_LGT_SYMBOL_COMMENT 50

/* Section name */
#define CL_LGT_SECTION_NAME 20

/* --- Element types --- */
#define CL_ELE_FREE             0
#define CL_ELE_INPUT            1
#define CL_ELE_INPUT_NOT        2
#define CL_ELE_RISING_INPUT     3
#define CL_ELE_FALLING_INPUT    4
#define CL_ELE_CONNECTION       9
#define CL_ELE_TIMER            10
#define CL_ELE_MONOSTABLE       11
#define CL_ELE_COUNTER          12
#define CL_ELE_TIMER_IEC        13
#define CL_ELE_COMPAR           20
#define CL_ELE_OUTPUT           50
#define CL_ELE_OUTPUT_NOT       51
#define CL_ELE_OUTPUT_SET       52
#define CL_ELE_OUTPUT_RESET     53
#define CL_ELE_OUTPUT_JUMP      54
#define CL_ELE_OUTPUT_CALL      55
#define CL_ELE_OUTPUT_OPERATE   60
#define CL_ELE_UNUSABLE         99

/* --- Variable types --- */
#define CL_VAR_MEM_BIT          0
#define CL_VAR_TIMER_DONE       10
#define CL_VAR_TIMER_RUNNING    11
#define CL_VAR_TIMER_IEC_DONE   15
#define CL_VAR_MONOSTABLE_RUNNING 20
#define CL_VAR_COUNTER_DONE     25
#define CL_VAR_COUNTER_EMPTY    26
#define CL_VAR_COUNTER_FULL     27
#define CL_VAR_STEP_ACTIVITY    30
#define CL_VAR_PHYS_INPUT       50
#define CL_VAR_PHYS_OUTPUT      60
#define CL_VAR_ERROR_BIT        70
#define CL_VAR_ARE_WORD         199
#define CL_VAR_MEM_WORD         200
#define CL_VAR_STEP_TIME        220
#define CL_VAR_TIMER_PRESET     230
#define CL_VAR_TIMER_VALUE      231
#define CL_VAR_MONOSTABLE_PRESET 240
#define CL_VAR_MONOSTABLE_VALUE 241
#define CL_VAR_COUNTER_PRESET   250
#define CL_VAR_COUNTER_VALUE    251
#define CL_VAR_TIMER_IEC_PRESET 260
#define CL_VAR_TIMER_IEC_VALUE  261
#define CL_VAR_PHYS_WORD_INPUT  270
#define CL_VAR_PHYS_WORD_OUTPUT 280
#define CL_VAR_PHYS_FLOAT_INPUT 300
#define CL_VAR_PHYS_FLOAT_OUTPUT 310

/* Ladder states */
#define CL_STATE_LOADING 0
#define CL_STATE_STOP    1
#define CL_STATE_RUN     2

/* Section languages */
#define CL_SECTION_LADDER     0
#define CL_SECTION_SEQUENTIAL 1

/* Timer IEC modes */
#define CL_TIMER_IEC_TON  0
#define CL_TIMER_IEC_TOF  1
#define CL_TIMER_IEC_TP   2

/* Time bases, in milliseconds. The .clp file and the API carry the base as an
 * id (CL_BASE_*); the structures below hold the millisecond value. */
#define CL_TIME_BASE_MINS  60000
#define CL_TIME_BASE_SECS  1000
#define CL_TIME_BASE_100MS 100

#define CL_BASE_MINS  0
#define CL_BASE_SECS  1
#define CL_BASE_100MS 2

/* --- Data structures --- */

typedef struct {
    int16_t type;
    int8_t  connected_with_top;
    int32_t var_type;
    int32_t var_num;
    /* Dynamic state — written by the RT scan, read lock-free by
     * GetRungStates/WatchRungStates to animate the ladder view. Single-byte
     * flags, so a torn read cannot happen; a stale one is one frame old. */
    int8_t  dynamic_input;
    int8_t  dynamic_state;
    int8_t  dynamic_var_bak;
    int8_t  dynamic_output;
} cl_element_t;

typedef struct {
    int     used;
    int     prev_rung;
    int     next_rung;
    char    label[CL_LGT_LABEL];
    char    comment[CL_LGT_COMMENT];
    cl_element_t elements[CL_RUNG_WIDTH][CL_RUNG_HEIGHT];
} cl_rung_t;

typedef struct {
    int  preset;
    int  value;
    int  base;
    char input_enable;
    char input_control;
    char output_done;
    char output_running;
} cl_timer_t;

typedef struct {
    int  preset;
    int  value;
    int  base;
    char input;
    char input_bak;
    char output_running;
} cl_monostable_t;

typedef struct {
    int  preset;
    int  value;
    int  value_bak;
    char input_reset;
    char input_preset;
    char input_count_up;
    char input_count_up_bak;
    char input_count_down;
    char input_count_down_bak;
    char output_done;
    char output_empty;
    char output_full;
} cl_counter_t;

typedef struct {
    int  preset;
    int  value;
    int  base;
    char timer_mode;
    char input;
    char input_bak;
    char output;
    char timer_started;
    int  value_to_reach_one_base_unit;
} cl_timer_iec_t;

typedef struct {
    char expr[CL_ARITHM_EXPR_SIZE];
} cl_arithm_expr_t;

typedef struct {
    char used;
    char name[CL_LGT_SECTION_NAME];
    int  language;
    int  sub_routine_number;
    int  first_rung;
    int  last_rung;
    int  sequential_page;
} cl_section_t;

typedef struct {
    char var_name[CL_LGT_VAR_NAME];
    char symbol[CL_LGT_SYMBOL_STRING];
    char comment[CL_LGT_SYMBOL_COMMENT];
} cl_symbol_t;

/* Sequential Function Chart (SFC/Grafcet) structures */
typedef struct {
    char init_step;      /* activated at init */
    int  step_number;    /* logical step number (for VAR_STEP_ACTIVITY) */
    int8_t num_page;     /* -1 if not used */
    char posi_x;
    char posi_y;
    /* dynamic state */
    char activated;
    int  time_activated; /* ms */
} cl_step_t;

typedef struct {
    int  var_type_condi;  /* condition variable type */
    int  var_num_condi;   /* condition variable offset */
    int16_t num_step_to_activ[CL_MAX_SWITCHS];
    int16_t num_step_to_desactiv[CL_MAX_SWITCHS];
    int16_t num_trans_linked_for_start[CL_MAX_SWITCHS];
    int16_t num_trans_linked_for_end[CL_MAX_SWITCHS];
    int8_t num_page;     /* -1 if not used */
    char posi_x;
    char posi_y;
    /* dynamic state */
    char activated;
} cl_transition_t;

typedef struct {
    int8_t num_page;     /* -1 if not used */
    char posi_x;
    char posi_y;
    char comment[CL_SEQ_COMMENT_LGT];
} cl_seq_comment_t;

/* PLC size configuration */
typedef struct {
    int nbr_rungs;
    int nbr_bits;
    int nbr_words;
    int nbr_timers;
    int nbr_monostables;
    int nbr_counters;
    int nbr_timers_iec;
    int nbr_phys_inputs;
    int nbr_phys_outputs;
    int nbr_arithm_expr;
    int nbr_sections;
    int nbr_symbols;
    int nbr_s32_in;
    int nbr_s32_out;
    int nbr_float_in;
    int nbr_float_out;
    int nbr_error_bits;
} cl_sizes_t;

/* Main RT instance.
 *
 * Allocated by classicladder_rt_alloc as ONE block: this header struct
 * followed by the size-configurable arrays, with the pointer fields below
 * aiming into that trailing region.  classicladder_rt_free is a single
 * free().  The sequential chart arrays are compile-time fixed and stay
 * inline.
 *
 * Ownership: Go mutates program data (rungs, sections, expressions, SFC
 * structures, symbols) under the module mutex; the RT scan walks them
 * lock-free and writes the per-element dynamic_* fields, the block and
 * variable state, and — via the runaway guard — `state`.  Structural
 * mutations must therefore publish in the documented order (see
 * structure.go); everything the scan writes is glitch-tolerant for readers
 * that hold no lock. */
typedef struct {
    /* State (atomic for RT/Go coordination) */
    _Atomic int         state;          /* CL_STATE_xxx */
    /* 2.9's UnderCalculationPleaseWait, done for real this time (the 2.9 HAL
     * module never actually set it, leaving StopRunIfRunning's wait vacuous).
     * The RT function raises it BEFORE it reads `state` and clears it when the
     * scan is done, both seq_cst, so a writer that stores a non-RUN state and
     * then sees it low knows no scan is in flight (Dekker pairing — see
     * waitScanSettled in api.go). */
    _Atomic int         scanning;
    _Atomic int32_t     duration_of_last_scan_ns;
    _Atomic uint32_t    generation;     /* bumped on any program change */

    /* Configuration */
    cl_sizes_t          sizes;
    int                 periodic_refresh_ms;
    unsigned long       period_leftover_ns; /* sub-millisecond scan remainder */
    int                 first_rung;
    int                 last_rung;
    int                 current_rung;

    /* Total lengths of the packed variable arrays, derived from `sizes` by
     * classicladder_rt_alloc so Go and C size their views identically. */
    int                 var_bits_count;
    int                 var_words_count;
    int                 var_floats_count;

    /* Program data — written by Go under the module mutex, walked lock-free
     * by the RT scan (which also writes the elements' dynamic_* fields) */
    cl_rung_t          *rungs;           /* [sizes.nbr_rungs] */
    cl_section_t       *sections;        /* [sizes.nbr_sections] */
    cl_arithm_expr_t   *arithm_exprs;    /* [sizes.nbr_arithm_expr] */
    cl_compiled_expr_t *compiled_exprs;  /* [sizes.nbr_arithm_expr] */

    /* Sequential (SFC) data — fixed size, inline */
    cl_step_t           steps[CL_MAX_STEPS];
    cl_transition_t     transitions[CL_MAX_TRANSITIONS];
    cl_seq_comment_t    seq_comments[CL_MAX_SEQ_COMMENTS];

    /* Runtime data — written by RT, read by Go for monitoring */
    cl_timer_t         *timers;          /* [sizes.nbr_timers] */
    cl_monostable_t    *monostables;     /* [sizes.nbr_monostables] */
    cl_counter_t       *counters;        /* [sizes.nbr_counters] */
    cl_timer_iec_t     *timers_iec;      /* [sizes.nbr_timers_iec] */

    /* Variable arrays — written by RT; packed regions per cl_var_region_size */
    char               *var_bits;        /* [var_bits_count] */
    int32_t            *var_words;       /* [var_words_count] */
    double             *var_floats;      /* [var_floats_count] */

    /* Symbol table (non-RT, for UI) */
    cl_symbol_t        *symbols;         /* [sizes.nbr_symbols] */

    /* HAL pin pointers (set up during init, used by RT) */
    hal_bit_t         **hal_inputs;        /* [sizes.nbr_phys_inputs] */
    hal_bit_t         **hal_outputs;       /* [sizes.nbr_phys_outputs] */
    hal_s32_t         **hal_s32_inputs;    /* [sizes.nbr_s32_in] */
    hal_s32_t         **hal_s32_outputs;   /* [sizes.nbr_s32_out] */
    hal_float_t       **hal_float_inputs;  /* [sizes.nbr_float_in] */
    hal_float_t       **hal_float_outputs; /* [sizes.nbr_float_out] */
} classicladder_rt_t;

/* --- RT functions (called from HAL thread) --- */

/* The main scan function — exported to HAL via hal_export_funct().
 * Reads inputs, evaluates all sections, writes outputs. */
void classicladder_refresh(void *arg, long period);

/* One PLC scan of every main section, with the given elapsed milliseconds.
 * Does not touch HAL pins — classicladder_refresh wraps it with the pin I/O
 * and the sub-millisecond period accounting. */
void cl_scan(classicladder_rt_t *rt, int ms_elapsed);

/* Allocate and initialize a classicladder_rt_t instance sized to *sizes.
 * Returns NULL if any count is negative, above CL_SIZE_LIMIT, or if
 * nbr_rungs/nbr_sections is < 1 (the scan reads both unconditionally). */
classicladder_rt_t *classicladder_rt_alloc(const cl_sizes_t *sizes);

/* Free an instance. */
void classicladder_rt_free(classicladder_rt_t *rt);

/* Initialize all runtime data (vars, timers, counters). */
void classicladder_rt_init_data(classicladder_rt_t *rt);

/* Reset every piece of dynamic state before the PLC starts running: timers,
 * monostables, counters, IEC timers, edge-contact history and the sequential
 * init steps (2.9 PrepareAllDatasBeforeRun). Call on any transition to RUN. */
void cl_prepare_all_datas_before_run(classicladder_rt_t *rt);

/* Write a variable (for forcing from UI). Thread-safe for single-writer. */
void write_var_ext(classicladder_rt_t *rt, int type, int offset, int value);

/* Read a variable (for Modbus slave / external access). */
int read_var_ext(classicladder_rt_t *rt, int type, int offset);

/* Evaluate a compiled COMPAR expression. Returns 1 (true) or 0 (false). */
int cl_eval_compare(classicladder_rt_t *rt, int expr_index);

/* Evaluate a compiled OPERATE expression. Writes result to target var. */
void cl_eval_operate(classicladder_rt_t *rt, int expr_index);

/* Initialize sequential data (set init steps active). */
void cl_prepare_sequential(classicladder_rt_t *rt);

/* Evaluate one sequential page. */
void cl_refresh_sequential_page(classicladder_rt_t *rt, int page_nbr);

#endif /* CLASSICLADDER_RT_H */
