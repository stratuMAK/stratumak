#ifndef RTAPI_TASK_H
#define RTAPI_TASK_H

#include <stddef.h>
#include <time.h>

/* rtapi_task structure — single definition shared by the launcher CGo shims
   and uspace_rtapi_lib.c.  Converted from the C++ class hierarchy that
   lived in the now-deleted rtapi_uspace.hh. */
struct rtapi_task {
    int magic;
    int id;
    int owner;
    int uses_fp;
    size_t stacksize;
    int prio;
    int cpu_number;  /* CPU to pin this task to; -1 = no affinity */
    long period;
    struct timespec nextstart;
    long pll_correction;
    long pll_correction_limit;
    void *arg;
    void (*taskcode)(void*);
    volatile int task_exit;  /* cooperative exit flag — set to 1 to request clean shutdown */
    /* uspace non-RT only (do_thread_lock): does this task currently hold
       thread_lock?  The cooperative-exit flag can be observed either inside
       task_wait() or by the task loop's own condition afterwards, and the two
       leave the lock in different states; task_wrapper() uses this to release
       it exactly once on whichever path the task exits. */
    volatile int holds_thread_lock;
};

#define MAX_TASKS  64
#define TASK_MAGIC    21979
#define TASK_MAGIC_INIT   ((struct rtapi_task*)(-1))

#endif /* RTAPI_TASK_H */
