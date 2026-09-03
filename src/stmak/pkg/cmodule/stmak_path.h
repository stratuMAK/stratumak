/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: LGPL Version 2.1
 */
// stmak_path.h — configuration path resolution for stratuMAK C modules.
//
// A path that comes from configuration — a `config=` argument, a filename
// attribute inside a config file, an INI value — is a *server-side* path.  The
// REST client that issued a runtime `load` has its own working directory, which
// is not part of the protocol and is meaningless when the client is remote.  So
// C modules must not fopen() such a value directly; they resolve it here first.
//
// The rule (implemented in Go, internal/pathres — the same one the startup
// HAL-file processing uses):
//
//   - a leading "~"/"~/" expands to the user's home directory
//   - a "LIB:" prefix resolves against the HAL library directories only
//   - a relative path resolves against the config directory first, then each
//     HAL library directory
//   - an absolute path is used as given
//   - the result must lie inside those directories after symlinks are
//     resolved, or resolution fails
//
// Resolve where the file is opened, not where the argument is parsed: a path
// read out of a config file (say an ESI <initCmds filename="...">) needs the
// same treatment as the argument that named that config file.
//
// NOT every configuration string that names something is a path.  Device nodes
// (`spidev_path=/dev/spidev1.0`, `port=/dev/ttyUSB0`), hardware configuration
// strings (`hm2_pci config=num_encoders=...`) and EtherCAT FoE filenames must
// NOT be resolved here — resolution is opt-in per call site for that reason.
//
// Usage:
//   const char *err = NULL;
//   const char *path = env->path->resolve(env->path->ctx, filename,
//                                         STMAK_PATH_READ, &err);
//   if (!path) {
//       stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER, "config file %s: %s", filename, err);
//       goto fail;
//   }
//   FILE *f = fopen(path, "r");

#ifndef STMAK_PATH_H
#define STMAK_PATH_H

#ifdef __cplusplus
extern "C" {
#endif

// Access mode — decides what the resolved path must already be.
typedef enum {
    // An existing regular file.  Directories and non-regular files (device
    // nodes, FIFOs, sockets) are rejected: opening a FIFO blocks forever, and
    // the launcher holds its module lock across a whole load.
    STMAK_PATH_READ = 0,
    // A regular file that may not exist yet; its parent directory must exist
    // and be contained.  A relative target resolves under the config directory
    // only — never into a HAL library directory.
    STMAK_PATH_WRITE = 1,
    // A directory, or a contained parent in which one can be created.
    STMAK_PATH_DIR = 2,
} stmak_path_mode_t;

// stmak_path_t — path resolution callbacks.
//
// ctx:      opaque pointer passed as the first argument to each callback.
// resolve:  returns the resolved absolute path, or NULL when the path cannot
//           be found or lies outside the allowed directories.  On failure and
//           if err_out is non-NULL, *err_out receives a human-readable reason.
//           Both the returned path and the error string have arena lifetime —
//           valid until Destroy().  A NULL or empty name always fails.
typedef struct {
    void *ctx;
    const char* (*resolve)(void *ctx, const char *name, stmak_path_mode_t mode,
                           const char **err_out);
} stmak_path_t;

#ifdef __cplusplus
}
#endif

#endif // STMAK_PATH_H
