// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//
// interp_param_io_persist.c — bridges interp_param_io_t to persist_callbacks_t.
// This is compiled as part of the Go task package (cgo).

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define PERSIST_API_CGO
#include "persist_api.h"
#include "emc/rs274ngc/interp_parameter_io.hh"

#define PARAM_IO_NAMESPACE "ngc_vars"

typedef struct {
    const persist_callbacks_t *persist;
    int32_t handle;
} param_io_persist_ctx_t;

static int persist_restore(void *ctx, double parameters[INTERP_PARAM_MAX])
{
    param_io_persist_ctx_t *pctx = (param_io_persist_ctx_t *)ctx;
    int k;
    int variable;
    double value;

    // Zero all parameters first.
    for (k = 0; k < INTERP_PARAM_MAX; k++)
        parameters[k] = 0;

    // Get all entries from the ngc_vars namespace.  A non-zero rc is a real
    // storage failure and must not be reported as "no parameters saved yet" —
    // restore_parameters() turns it into "Unable to restore parameters"
    // instead of silently handing the interpreter an all-zero parameter set.
    persist_entry_slice_t res;
    memset(&res, 0, sizeof(res));
    if (pctx->persist->get_entries(pctx->persist->ctx, pctx->handle, &res) != 0)
        return -1;

    if (res.data == NULL)
        return 0; // no data yet, OK

    for (size_t i = 0; i < res.len; i++) {
        if (res.data[i].key == NULL || res.data[i].value == NULL)
            continue;
        variable = atoi(res.data[i].key);
        if (variable <= 0 || variable >= INTERP_PARAM_MAX)
            continue;
        value = atof(res.data[i].value);
        parameters[variable] = value;
    }

    // Free the entries (allocated by the persist provider).
    free(res.data);
    return 0;
}

static int persist_save(void *ctx, const double parameters[INTERP_PARAM_MAX],
                        const int required_params[])
{
    param_io_persist_ctx_t *pctx = (param_io_persist_ctx_t *)ctx;
    int index = 0;
    int required;
    size_t count = 0;

    // Count required parameters.
    while (required_params[count] < INTERP_PARAM_MAX)
        count++;

    if (count == 0)
        return 0;

    // Build entries array.
    persist_entry_t *entries = (persist_entry_t *)malloc(count * sizeof(persist_entry_t));
    if (!entries)
        return -1;

    // Allocate key/value string buffers.
    // Key: max 5 digits. Value: max 20 chars for a double.
    char (*keys)[8] = (char (*)[8])malloc(count * 8);
    char (*vals)[32] = (char (*)[32])malloc(count * 32);
    if (!keys || !vals) {
        free(entries);
        free(keys);
        free(vals);
        return -1;
    }

    index = 0;
    for (size_t i = 0; i < count; i++) {
        required = required_params[i];
        snprintf(keys[i], 8, "%d", required);
        snprintf(vals[i], 32, "%f", parameters[required]);
        entries[i].key = keys[i];
        entries[i].value = vals[i];
        entries[i].updated = 0;
    }

    persist_set_result_t res;
    memset(&res, 0, sizeof(res));
    int rc = pctx->persist->set_entries(
        pctx->persist->ctx, pctx->handle, entries, count, &res);

    free(entries);
    free(keys);
    free(vals);

    return (rc == 0 && res.ok) ? 0 : -1;
}

// interp_param_io_persist_create builds an interp_param_io_t backed by persist.
interp_param_io_t interp_param_io_persist_create(const persist_callbacks_t *persist)
{
    param_io_persist_ctx_t *pctx = (param_io_persist_ctx_t *)malloc(sizeof(param_io_persist_ctx_t));
    pctx->persist = persist;

    // Open the ngc_vars namespace.  A failed open leaves the handle invalid;
    // -1 makes every later call fail loudly at the persist end rather than
    // reading and writing handle 0, which belongs to another namespace.
    persist_open_result_t open_res;
    memset(&open_res, 0, sizeof(open_res));
    if (persist->open(persist->ctx, PARAM_IO_NAMESPACE, &open_res) != 0)
        pctx->handle = -1;
    else
        pctx->handle = open_res.handle;

    interp_param_io_t io;
    memset(&io, 0, sizeof(io));
    io.restore = persist_restore;
    io.save = persist_save;
    io.ctx = pctx;
    return io;
}

// interp_param_io_persist_destroy frees the persist param IO context.
void interp_param_io_persist_destroy(interp_param_io_t *io)
{
    if (io && io->ctx) {
        free(io->ctx);
        io->ctx = NULL;
    }
}
