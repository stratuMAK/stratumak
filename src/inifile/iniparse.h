/*
 * iniparse.h - Minimal INI file parser
 *
 * Provides simple key=value lookup by section, with support for:
 * - [section] headers
 * - # and ; comments
 * - nth occurrence of a tag
 * - backslash line continuation
 * - tilde expansion
 *
 * It reads exactly one file: #INCLUDE is not resolved, and ini_open()
 * refuses a file that uses it.  The machine INI with its includes is read
 * through stmakd (REST API, gmi.IniFile).
 *
 * Copyright (C) 2004 Fred Proctor & Will Shackleford (original)
 * Copyright (C) 2026 LinuxCNC contributors (cleanup)
 * License: GPL Version 2
 */

#ifndef INIPARSE_H
#define INIPARSE_H

#include <stdio.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/*
 * Open file for ini_find(): tilde expansion, then fopen(file, "r").
 * A file that contains an #INCLUDE directive is refused with a message on
 * stderr - returns NULL with errno = ENOTSUP - because skipping the
 * directive would silently lose the included keys.
 * Returns NULL with errno set on any other error as well.
 */
FILE *ini_open(const char *file);

/*
 * Find the nth occurrence of tag in section.
 * Returns pointer to value string (static buffer, not reentrant), or NULL.
 * If section is NULL, searches the entire file.
 * num is 1-based (1 = first occurrence).
 */
const char *ini_find(FILE *fp, const char *tag, const char *section, int num);

/*
 * Find tag in section and parse as integer.
 * Returns 0 on success, non-zero on error (tag not found or conversion failed).
 */
int ini_find_int(FILE *fp, const char *tag, const char *section, int *result);

/*
 * Find tag in section and parse as double.
 * Returns 0 on success, non-zero on error (tag not found or conversion failed).
 */
int ini_find_double(FILE *fp, const char *tag, const char *section, double *result);

/*
 * Expand leading ~/ to $HOME/.
 * Returns 0 on success, non-zero on error.
 */
int ini_tilde_expansion(const char *file, char *path, size_t size);

#ifdef __cplusplus
}
#endif

#endif /* INIPARSE_H */
