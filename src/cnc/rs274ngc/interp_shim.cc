// interp_shim.cc — C++ shim exposing the RS274NGC interpreter to CGo.
//
// Wraps the InterpBase/Interp C++ virtual class as plain C functions
// for use from Go via CGo.
//
// Original interpreter: Copyright 2004, 2005, 2006 Jeff Epler <jepler@unpythonic.net>
//                       and Chris Radek <chris@timeguy.com> (GPL v2)
// This shim: Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
#include "interp_shim.h"

#include "rs274ngc_interp.hh"
#include "stmak/generated/gmi/canon/canon_api.h"
#include "interp_parameter_io.hh"

struct interp_handle {
    Interp *interp;
};

extern "C" {

interp_handle_t *interp_shim_new(void) {
    auto *h = new interp_handle_t;
    h->interp = new Interp;
    return h;
}

void interp_shim_set_callbacks(interp_handle_t *h,
                               const canon_callbacks_t *cb) {
    if (h && h->interp && cb) {
        h->interp->set_canon_callbacks(cb);
    }
}

void interp_shim_set_ini_accessor(interp_handle_t *h,
                                  const interp_shim_ini_accessor_t *acc) {
    if (h && h->interp && acc) {
        h->interp->_setup.ini_accessor.ctx = acc->ctx;
        h->interp->_setup.ini_accessor.get = acc->get;
        h->interp->_setup.ini_accessor.get_nth = acc->get_nth;
    }
}

// Preview never EXECUTES a user-defined M-code: it has no HAL, no machine and
// no handler registry.  But refusing to READ one is a different thing, and
// that is what happens by default -- convert_m rejects any M100-M199 whose
// slot is empty (interp_convert.cc, NCE_UNKNOWN_M_CODE_USED), so a program
// that uses them cannot be previewed at all.  A machine whose whole workflow
// runs through M-code handlers therefore cannot show a toolpath.
//
// A no-op in every slot lets the preview walk the program.  What the program
// then reads back from #5399 is 0, because that is what the preview canon's
// get_user_defined_result reports -- and a program that branches on it takes
// its "simulation" path, which is exactly what a preview should show.
static void shim_user_defined_noop(int num, double arg1, double arg2) {
    (void)num; (void)arg1; (void)arg2;
}

int interp_shim_init(interp_handle_t *h) {
    if (!h || !h->interp) return INTERP_SHIM_ERROR;
    int rc = h->interp->init();
    // After init(), not before: init() clears the table (interp_setup.cc).
    if (rc == INTERP_OK) {
        for (int i = 0; i < USER_DEFINED_FUNCTION_NUM; i++) {
            h->interp->_setup.user_defined_function[i] = shim_user_defined_noop;
        }
    }
    return rc;
}

int interp_shim_open(interp_handle_t *h, const char *filename) {
    if (!h || !h->interp || !filename) return INTERP_SHIM_ERROR;
    return h->interp->open(filename);
}

int interp_shim_read(interp_handle_t *h) {
    if (!h || !h->interp) return INTERP_SHIM_ERROR;
    return h->interp->read();
}

int interp_shim_read_string(interp_handle_t *h, const char *code) {
    if (!h || !h->interp || !code) return INTERP_SHIM_ERROR;
    return h->interp->read(code);
}

int interp_shim_execute(interp_handle_t *h) {
    if (!h || !h->interp) return INTERP_SHIM_ERROR;
    return h->interp->execute();
}

int interp_shim_sequence_number(interp_handle_t *h) {
    if (!h || !h->interp) return 0;
    return h->interp->sequence_number();
}

void interp_shim_file_name(interp_handle_t *h, char *buf, int buf_size) {
    if (!buf || buf_size <= 0) return;
    buf[0] = '\0';
    if (!h || !h->interp) return;
    h->interp->file_name(buf, (size_t)buf_size);
}

int interp_shim_close(interp_handle_t *h) {
    if (!h || !h->interp) return INTERP_SHIM_ERROR;
    return h->interp->close();
}

double interp_shim_get_parameter(interp_handle_t *h, int index) {
    if (!h || !h->interp || index < 0 || index >= 5602) return 0.0;
    return h->interp->_setup.parameters[index];
}

void interp_shim_destroy(interp_handle_t *h) {
    if (h) {
        delete h->interp;
        delete h;
    }
}

void interp_shim_error_text(interp_handle_t *h, int error_code,
                            char *buf, int buf_size) {
    if (!h || !h->interp) {
        snprintf(buf, buf_size, "no interpreter");
        return;
    }
    h->interp->error_text(error_code, buf, buf_size);
}

void interp_shim_set_param_io(interp_handle_t *h,
                              const interp_param_io_t *io) {
    if (h && h->interp) {
        h->interp->set_param_io(io);
    }
}

} // extern "C"
