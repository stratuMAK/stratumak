/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: LGPL Version 2.1
 */
// stmak_sched.h — where the configuration runs a module's realtime work.
//
// A module's New() runs before the HAL wiring commands create the threads
// and add the functions to them, so a module cannot look at its threads
// yet.  The launcher can: it has parsed every HAL file and reserved each
// newthread's CPU before the first module is loaded, and the thread then
// gets exactly that CPU.  A module that has to set something up on that CPU
// before its threads run -- the EtherCAT master pins its NIC interrupts
// there before its bus scan -- asks here.
//
// Usage:
//   int cpu = env->sched->funct_cpu(env->sched->ctx, "ethercat.read-all");
//   if (cpu < 0) { ... not known: no addf of it, or a thread without affinity }
#ifndef STMAK_SCHED_H
#define STMAK_SCHED_H

#ifdef __cplusplus
extern "C" {
#endif

// stmak_sched_t — scheduling queries.
//
// ctx:        opaque pointer passed as the first argument to each callback.
// funct_cpu:  the CPU of the thread the boot HAL files add funct to (its
//             first addf there); -1 when they do not add it (a module
//             loaded later, at runtime), when that thread has no CPU
//             affinity, or for a NULL funct.
typedef struct {
    void *ctx;
    int (*funct_cpu)(void *ctx, const char *funct);
} stmak_sched_t;

#ifdef __cplusplus
}
#endif

#endif // STMAK_SCHED_H
