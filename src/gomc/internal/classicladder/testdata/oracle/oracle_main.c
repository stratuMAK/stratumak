/* oracle_main.c — headless driver for the LinuxCNC 2.9 ClassicLadder engine.
 *
 * This is the reference implementation the gomc RT engine is differentially
 * tested against. It loads a .clp project, applies a script of variable writes
 * and scans read from stdin, and dumps variable state on demand. Nothing here
 * ships; it exists so the port can be checked against the original.
 *
 * Script commands, one per line:
 *   set <var_type> <offset> <value>   write a variable
 *   scan <ms>                         one PLC scan of <ms> milliseconds
 *   dump                              print the whole variable state
 *   prepare                           PrepareAllDatasBeforeRun()
 *   dumpnum                           print just %Q0, %QW0, %QW1, %QF0
 *   varname <type> <offset>           print the written variable name
 *   varparse <text>                   parse a written name back
 *   varrw <type> <offset>             print whether it is writable
 *
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: GPL Version 2
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "classicladder.h"
#include "global.h"
#include "calc.h"
#include "files_project.h"
#include "vars_access.h"
#include "vars_names.h"
#include "protocol_modbus_master.h"

/* vars_names.c reports parse failures through this global, which normally
 * lives in the editor (edit.c). The oracle only reads projects, so a
 * definition is all it needs. */
char *ErrorMessageVarParser = NULL;

/* files.c parses the Modbus sections of a project unconditionally. The oracle
 * exercises the ladder engine only, so it supplies the storage those parsers
 * write into and never starts a Modbus master. */
StrModbusMasterReq ModbusMasterReq[NBR_MODBUS_MASTER_REQ];
char ModbusSerialPortNameUsed[30];
int ModbusSerialSpeed;
int ModbusSerialDataBits;
int ModbusSerialStopBits;
int ModbusSerialParity;
int ModbusSerialUseRtsToSend;
int ModbusTimeInterFrame;
int ModbusTimeOutReceipt;
int ModbusTimeAfterTransmit;
int ModbusEleOffset;
int ModbusDebugLevel;
int MapCoilRead;
int MapCoilWrite;
int MapInputs;
int MapHolding;
int MapRegisterRead;
int MapRegisterWrite;

int modmaster;
void PrepareModbusMaster(void) {}

/* emc_mods.c labels ladder variables with the HAL signal names wired to the
 * classicladder pins. There is no HAL here, so symbols stay as loaded. */
void SymbolsAutoAssign(void) {}

/* Variable types worth dumping, paired with the size that bounds them. */
static void dump_state(void) {
    int i;

    printf("BITS");
    for (i = 0; i < NBR_BITS; i++)
        printf(" %d", ReadVar(VAR_MEM_BIT, i));
    printf("\n");

    printf("INPUTS");
    for (i = 0; i < NBR_PHYS_INPUTS; i++)
        printf(" %d", ReadVar(VAR_PHYS_INPUT, i));
    printf("\n");

    printf("OUTPUTS");
    for (i = 0; i < NBR_PHYS_OUTPUTS; i++)
        printf(" %d", ReadVar(VAR_PHYS_OUTPUT, i));
    printf("\n");

    printf("WORDS");
    for (i = 0; i < NBR_WORDS; i++)
        printf(" %d", ReadVar(VAR_MEM_WORD, i));
    printf("\n");

    printf("TIMER_DONE");
    for (i = 0; i < NBR_TIMERS; i++)
        printf(" %d", ReadVar(VAR_TIMER_DONE, i));
    printf("\n");

    printf("TIMER_RUNNING");
    for (i = 0; i < NBR_TIMERS; i++)
        printf(" %d", ReadVar(VAR_TIMER_RUNNING, i));
    printf("\n");

    printf("TIMER_VALUE");
    for (i = 0; i < NBR_TIMERS; i++)
        printf(" %d", ReadVar(VAR_TIMER_VALUE, i));
    printf("\n");

    printf("MONO_RUNNING");
    for (i = 0; i < NBR_MONOSTABLES; i++)
        printf(" %d", ReadVar(VAR_MONOSTABLE_RUNNING, i));
    printf("\n");

    printf("COUNTER_VALUE");
    for (i = 0; i < NBR_COUNTERS; i++)
        printf(" %d", ReadVar(VAR_COUNTER_VALUE, i));
    printf("\n");

    printf("COUNTER_DONE");
    for (i = 0; i < NBR_COUNTERS; i++)
        printf(" %d", ReadVar(VAR_COUNTER_DONE, i));
    printf("\n");

    printf("TIMER_IEC_DONE");
    for (i = 0; i < NBR_TIMERS_IEC; i++)
        printf(" %d", ReadVar(VAR_TIMER_IEC_DONE, i));
    printf("\n");

    printf("TIMER_IEC_VALUE");
    for (i = 0; i < NBR_TIMERS_IEC; i++)
        printf(" %d", ReadVar(VAR_TIMER_IEC_VALUE, i));
    printf("\n");

    printf("ERROR_BITS");
    for (i = 0; i < NBR_ERROR_BITS; i++)
        printf(" %d", ReadVar(VAR_ERROR_BIT, i));
    printf("\n");

    printf("END\n");
    fflush(stdout);
}

/* A narrow dump for the numeric-pin test: the handful of outputs it samples,
 * in the order the HAL test captures them. */
static void dump_numeric(void) {
    printf("NUM %d %d %d %d\n",
           ReadVar(VAR_PHYS_OUTPUT, 0),
           ReadVar(VAR_PHYS_WORD_OUTPUT, 0),
           ReadVar(VAR_PHYS_WORD_OUTPUT, 1),
           ReadVar(VAR_PHYS_FLOAT_OUTPUT, 0));
    fflush(stdout);
}

/* Variable-name conversion, for the conformance test in varnames_test.go.
 * CreateVarName renders a type/offset as its written form; TextParserForAVar
 * reads one back. Both print "-" when the original refuses. */
static void do_varname(int type, int offset) {
    char *name = CreateVarName(type, offset);
    printf("NAME %s\n", (name && strcmp(name, "???")) ? name : "-");
    fflush(stdout);
}

static void do_varparse(const char *text) {
    int type = -1, offset = -1, chars = 0;
    if (TextParserForAVar((char *)text, &type, &offset, &chars, FALSE))
        printf("PARSE %d %d %d\n", type, offset, chars);
    else
        printf("PARSE -\n");
    fflush(stdout);
}

static void do_varrw(int type, int offset) {
    printf("RW %d\n", TestVarIsReadWrite(type, offset) ? 1 : 0);
    fflush(stdout);
}

/* The sizes have to be set before ClassicLadder_AllocAll(). They mirror the
 * ones the gomc side is configured with, so both engines see the same PLC. */
static void set_sizes(void) {
    plc_sizeinfo_s *s = &GeneralParamsMirror.SizesInfos;
    s->nbr_rungs = 100;
    s->nbr_bits = 100;
    s->nbr_words = 100;
    s->nbr_timers = 10;
    s->nbr_monostables = 10;
    s->nbr_counters = 10;
    s->nbr_timers_iec = 10;
    s->nbr_phys_inputs = 15;
    s->nbr_phys_outputs = 15;
    s->nbr_arithm_expr = 100;
    s->nbr_sections = 10;
    s->nbr_symbols = 200;
    s->nbr_phys_words_inputs = 10;
    s->nbr_phys_words_outputs = 10;
    s->nbr_phys_float_inputs = 10;
    s->nbr_phys_float_outputs = 10;
    s->nbr_error_bits = 10;
}

int main(int argc, char *argv[]) {
    char line[256];

    if (argc < 2) {
        fprintf(stderr, "usage: %s <project.clp> < script\n", argv[0]);
        return 2;
    }

    set_sizes();
    GeneralParamsMirror.PeriodicRefreshMilliSecs = 1;
    if (!ClassicLadder_AllocAll(1)) {
        fprintf(stderr, "alloc failed\n");
        return 1;
    }
    /* The name table carries compile-time default sizes until this patches it
     * with the configured ones. 2.9 calls it on the shared-memory attach path,
     * which a standalone creator never takes — without it %B99 would be
     * reported as out of range in a 100-bit PLC. */
    UpdateSizesOfConvVarNameTable();
    ClassicLadder_InitAllDatas();

    if (!LoadProjectFiles(argv[1])) {
        fprintf(stderr, "failed to load project %s\n", argv[1]);
        return 1;
    }
    PrepareAllDatasBeforeRun();
    InfosGene->LadderState = STATE_RUN;
    /* CreateVarName substitutes a symbol for the variable name when this is
     * set, which is a display choice rather than part of the name mapping. The
     * conformance test wants the canonical names. */
    InfosGene->DisplaySymbols = FALSE;

    while (fgets(line, sizeof(line), stdin)) {
        char cmd[32];
        int a, b, c;

        if (sscanf(line, "%31s", cmd) != 1)
            continue;
        if (!strcmp(cmd, "set")) {
            if (sscanf(line, "%*s %d %d %d", &a, &b, &c) == 3)
                WriteVar(a, b, c);
        } else if (!strcmp(cmd, "scan")) {
            if (sscanf(line, "%*s %d", &a) == 1)
                InfosGene->GeneralParams.PeriodicRefreshMilliSecs = a;
            ClassicLadder_RefreshAllSections();
        } else if (!strcmp(cmd, "prepare")) {
            PrepareAllDatasBeforeRun();
        } else if (!strcmp(cmd, "dump")) {
            dump_state();
        } else if (!strcmp(cmd, "dumpnum")) {
            dump_numeric();
        } else if (!strcmp(cmd, "varname")) {
            if (sscanf(line, "%*s %d %d", &a, &b) == 2)
                do_varname(a, b);
        } else if (!strcmp(cmd, "varrw")) {
            if (sscanf(line, "%*s %d %d", &a, &b) == 2)
                do_varrw(a, b);
        } else if (!strcmp(cmd, "varparse")) {
            char text[128];
            if (sscanf(line, "%*s %127s", text) == 1)
                do_varparse(text);
        }
    }
    return 0;
}
