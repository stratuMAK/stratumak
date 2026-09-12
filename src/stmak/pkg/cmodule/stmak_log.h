/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: LGPL Version 2.1
 */
// stmak_log.h — Structured logging API for stratuMAK C modules.
//
// All log messages (RT and non-RT) are enqueued into a lock-free ring buffer.
// A Go goroutine drains the buffer and forwards entries to the structured
// logging backend.  This guarantees message ordering across RT and non-RT
// code paths and avoids any Go/CGO crossing on the RT hot path.
//
// Usage:
//   stmak_log_infof(env->log, "mycomp", "started %d slaves", n);
//   stmak_logf(env->log, "mycomp", STMAK_LOG_ERROR | STMAK_LOG_OPER,
//              "init failed: %s", reason);

#ifndef STMAK_LOG_H
#define STMAK_LOG_H

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "stmak_rtapi.h"  // STMAK_RTAPI_NAME_LEN
#include "stmak_rt_check.h"

#ifdef __cplusplus
extern "C" {
#endif

// ---------------------------------------------------------------------------
// Log levels
// ---------------------------------------------------------------------------

typedef enum {
    STMAK_LOG_DEBUG = 0,
    STMAK_LOG_INFO  = 1,
    STMAK_LOG_WARN  = 2,
    STMAK_LOG_ERROR = 3,

    // "The operator should see this."  A FLAG, not a level, because audience
    // and severity are independent: a finished batch is worth telling the
    // operator and is not an error, and an internal consistency failure is an
    // error they can do nothing about.  Folding the two into one ordered scale
    // forces a choice between saying what a message MEANS and getting it seen.
    //
    // OR it into the level: STMAK_LOG_ERROR | STMAK_LOG_OPER.  The severity
    // travels with it, so the operator channel can show a fault differently
    // from a notice (emcerror OPERATOR_ERROR vs OPERATOR_TEXT).
    //
    // Everything that compares a level must mask first -- see
    // STMAK_LOG_SEVERITY.  A raw comparison against a word carrying this bit
    // passes every filter.
    STMAK_LOG_OPER  = 0x10,
} stmak_log_level_t;

// ---------------------------------------------------------------------------
// Ring buffer slot — fixed-size.
// ---------------------------------------------------------------------------

#define STMAK_LOG_LEVEL_MASK    0x0fu
#define STMAK_LOG_SEVERITY(l)   ((uint32_t)(l) & STMAK_LOG_LEVEL_MASK)
#define STMAK_LOG_IS_OPER(l)    (((uint32_t)(l) & (uint32_t)STMAK_LOG_OPER) != 0u)

// The severities a drop is accounted under: DEBUG .. ERROR.  Anything the mask
// lets through above ERROR is counted as ERROR.
#define STMAK_LOG_NUM_SEVERITIES 4

#define STMAK_LOG_MSG_LEN       216
#define STMAK_LOG_COMPONENT_LEN (STMAK_RTAPI_NAME_LEN + 1)

typedef struct {
    // The slot's turn counter (Vyukov's bounded queue).  A producer at
    // position p may take the slot when seq == p; it publishes with
    // seq = p + 1; the consumer, done with it, hands it on with
    // seq = p + STMAK_LOG_RING_SIZE, which is what the producer one lap
    // ahead is waiting for.  Every other value is "not mine" -- nothing is
    // claimed or written until the slot is known to be free, so a full ring
    // costs nothing but the message that did not fit.
    uint32_t          seq;
    uint32_t          level;                            // stmak_log_level_t
    int64_t           timestamp_ns;                     // CLOCK_REALTIME nanoseconds
    char              component[STMAK_LOG_COMPONENT_LEN];
    char              msg[STMAK_LOG_MSG_LEN];
} stmak_log_slot_t;

// Slot is 264 bytes (4+4+8+32+216).

// ---------------------------------------------------------------------------
// Ring buffer — single shared instance per launcher process.
// Multiple producers (C threads), single consumer (Go drain goroutine).
//
// Why this shape and not "fetch-add a position, then see if the slot is
// free": that variant commits a position before it knows there is room, so
// a full ring leaves a hole the consumer has to recognise -- and it cannot
// tell a hole from a producer that has claimed a position and not yet
// reached its slot.  Two producers logging at once were enough for the
// consumer to step over one of them; the stepped-over message was lost
// without being counted, and its slot then tripped a counted drop one lap
// later.  Here the slot's own turn counter is the claim, so there are no
// holes to recognise.
// ---------------------------------------------------------------------------

// 8192 slots, 2.1 MB.  A burst that reaches the ring at RT priority has to
// fit until the drain goroutine gets the CPU, and the drain then has to
// keep pace with whatever the log sink is -- a terminal over ssh included.
#define STMAK_LOG_RING_SIZE_SHIFT 13
#define STMAK_LOG_RING_SIZE       (1u << STMAK_LOG_RING_SIZE_SHIFT)
#define STMAK_LOG_RING_MASK       (STMAK_LOG_RING_SIZE - 1)

// Messages below WARN that are not for the operator stop taking slots once
// the ring is this full.  What remains is headroom for the messages that
// explain why the chatter happened.
#define STMAK_LOG_RING_HEADROOM_AT (STMAK_LOG_RING_SIZE - STMAK_LOG_RING_SIZE / 4)

typedef struct {
    // Producer side.  write_pos is the next position to be claimed; it only
    // moves when the claim succeeds.
    uint32_t write_pos;
    // Severities below this are not enqueued at all (STMAK_LOG_OPER always
    // is).  Maintained by the consumer from what its sinks would print, so a
    // DEBUG line nobody would see does not take a slot from an ERROR.
    uint32_t min_level;
    // Messages that found no room, by severity.
    uint32_t dropped[STMAK_LOG_NUM_SEVERITIES];
    char _pad0[64 - 6 * sizeof(uint32_t)];

    // Consumer side, on its own cache line.  read_pos is the consumer's
    // position, published so a producer can judge how full the ring is.
    uint32_t read_pos;
    char _pad1[64 - sizeof(uint32_t)];

    // Slot array.
    stmak_log_slot_t slots[STMAK_LOG_RING_SIZE];
} stmak_log_ring_t;

// ---------------------------------------------------------------------------
// Subscription handle — per-subscriber ring for fan-out from the drain loop.
// Allocated by subscribe(), freed by unsubscribe().
// ---------------------------------------------------------------------------

typedef struct {
    stmak_log_ring_t *ring;      // per-subscriber ring (filled by Go drain)
    uint32_t         min_level; // minimum level to receive (stmak_log_level_t)
} stmak_log_sub_t;

// ---------------------------------------------------------------------------
// stmak_log_t — the logging handle passed to modules via cmod_env_t.
// ---------------------------------------------------------------------------

typedef struct {
    stmak_log_ring_t *ring;  // pointer to shared ring buffer (producer side)

    // Subscribe to log messages at or above min_level.
    // Returns a subscription handle, or NULL on error.
    stmak_log_sub_t *(*subscribe)(void *ctx, stmak_log_level_t min_level);

    // Unsubscribe and free the subscription handle.
    void (*unsubscribe)(void *ctx, stmak_log_sub_t *sub);

    void *ctx;  // opaque context for subscribe/unsubscribe callbacks
} stmak_log_t;

// ---------------------------------------------------------------------------
// Producer API — pure C, no allocations, no syscalls, RT-safe.
// ---------------------------------------------------------------------------

// Get current wall clock time in nanoseconds (used for log timestamps).
// TRUSTED: clock_gettime is a vDSO read on Linux — no syscall, no lock.
static inline int64_t stmak_log_now_ns(void) STMAK_NONBLOCKING;
STMAK_NONBLOCKING_TRUSTED_BEGIN
static inline int64_t stmak_log_now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + (int64_t)ts.tv_nsec;
}
STMAK_NONBLOCKING_TRUSTED_END

// Format a log message into a fixed-size slot buffer.
// TRUSTED: vsnprintf into a fixed STMAK_LOG_MSG_LEN buffer — no allocation
// for the plain conversions used in RT log messages.  Kept as a minimal
// wrapper so the ring logic in stmak_ring_push stays compiler-verified.
static inline void stmak_log_vformat(char *dst, size_t len, const char *fmt,
                                    va_list ap) STMAK_NONBLOCKING;
STMAK_NONBLOCKING_TRUSTED_BEGIN
static inline void stmak_log_vformat(char *dst, size_t len, const char *fmt,
                                    va_list ap) {
    vsnprintf(dst, len, fmt, ap);
}
STMAK_NONBLOCKING_TRUSTED_END

// The index a severity is accounted under in ring->dropped.
static inline uint32_t stmak_log_drop_index(uint32_t level) STMAK_NONBLOCKING;
static inline uint32_t stmak_log_drop_index(uint32_t level) {
    uint32_t sev = STMAK_LOG_SEVERITY(level);
    return sev < STMAK_LOG_NUM_SEVERITIES ? sev : STMAK_LOG_NUM_SEVERITIES - 1;
}

// Does the ring want this level at all?  Below min_level nothing is
// enqueued; the operator flag overrides, since the operator channel does
// not go through the log sinks that set the floor.
static inline int stmak_ring_wants(const stmak_log_ring_t *ring,
                                   uint32_t level) STMAK_NONBLOCKING;
static inline int stmak_ring_wants(const stmak_log_ring_t *ring,
                                   uint32_t level) {
    if (STMAK_LOG_IS_OPER(level)) return 1;
    return STMAK_LOG_SEVERITY(level) >=
           __atomic_load_n(&ring->min_level, __ATOMIC_RELAXED);
}

// Enqueue a formatted message: claim the next free slot, copy, publish.
// Returns 0 on success, -1 if there was no room (message dropped, counted
// under its severity).
//
// Lock-free, not wait-free: the claim is a CAS on write_pos that another
// producer can win, in which case this one retries at the next position.
// With the handful of threads a machine has logging, that is a retry or two
// at worst, and it never waits for anyone -- a producer that has claimed a
// slot and been preempted in the middle of its copy holds up the consumer
// at that slot, not the other producers.  The copy is all it holds: the
// message was formatted before the claim, so that window is a memcpy, not
// a vsnprintf.
static inline int stmak_ring_push(stmak_log_ring_t *ring,
                                  const stmak_log_slot_t *m) STMAK_NONBLOCKING;
static inline int stmak_ring_push(stmak_log_ring_t *ring,
                                  const stmak_log_slot_t *m) {
    // Chatter yields to the messages that matter: below WARN and not for
    // the operator, a ring past the headroom mark is treated as full.
    const int limited = !STMAK_LOG_IS_OPER(m->level) &&
                        STMAK_LOG_SEVERITY(m->level) < (uint32_t)STMAK_LOG_WARN;

    uint32_t pos = __atomic_load_n(&ring->write_pos, __ATOMIC_RELAXED);
    stmak_log_slot_t *slot;
    for (;;) {
        slot = &ring->slots[pos & STMAK_LOG_RING_MASK];
        uint32_t seq = __atomic_load_n(&slot->seq, __ATOMIC_ACQUIRE);
        int32_t dif = (int32_t)(seq - pos);
        if (dif == 0) {
            // Free for this position.  Full for us?  read_pos is the
            // consumer's, read once here; a lap ago's value would only
            // make the limit a little early or late.
            if (limited &&
                (int32_t)(pos - __atomic_load_n(&ring->read_pos, __ATOMIC_RELAXED))
                    >= (int32_t)STMAK_LOG_RING_HEADROOM_AT) {
                break;
            }
            if (__atomic_compare_exchange_n(&ring->write_pos, &pos, pos + 1, 1,
                                            __ATOMIC_RELAXED, __ATOMIC_RELAXED)) {
                goto claimed;
            }
            // Lost the race; pos now holds the winner's successor.
        } else if (dif < 0) {
            // Still holds the message from one lap ago, unread: full.
            break;
        } else {
            // Someone else has this position; move up.
            pos = __atomic_load_n(&ring->write_pos, __ATOMIC_RELAXED);
        }
    }
    __atomic_fetch_add(&ring->dropped[stmak_log_drop_index(m->level)], 1,
                       __ATOMIC_RELAXED);
    return -1;

claimed:
    slot->level = m->level;
    slot->timestamp_ns = m->timestamp_ns;
    memcpy(slot->component, m->component, STMAK_LOG_COMPONENT_LEN);
    memcpy(slot->msg, m->msg, STMAK_LOG_MSG_LEN);
    __atomic_store_n(&slot->seq, pos + 1, __ATOMIC_RELEASE);
    return 0;
}

// Low-level enqueue: format the message, then push it.
// Returns 0 on success (or when the level is below the ring's floor and
// nothing was enqueued), -1 if the ring had no room (message dropped).
// The only trusted piece is the stmak_log_vformat wrapper above.
static inline int
stmak_log_emit(const stmak_log_t *log, stmak_log_level_t level,
              const char *component, const char *fmt, va_list ap)
    STMAK_NONBLOCKING;
static inline int
stmak_log_emit(const stmak_log_t *log, stmak_log_level_t level,
              const char *component, const char *fmt, va_list ap) {
    if (!log || !log->ring) return -1;
    stmak_log_ring_t *ring = log->ring;
    if (!stmak_ring_wants(ring, (uint32_t)level)) return 0;

    // Formatted on the stack first; the slot is held for a copy only.
    stmak_log_slot_t m;
    m.level = (uint32_t)level;
    m.timestamp_ns = stmak_log_now_ns();
    strncpy(m.component, component, STMAK_RTAPI_NAME_LEN);
    m.component[STMAK_RTAPI_NAME_LEN] = '\0';
    stmak_log_vformat(m.msg, STMAK_LOG_MSG_LEN, fmt, ap);

    return stmak_ring_push(ring, &m);
}

// ---------------------------------------------------------------------------
// Convenience functions with printf format checking.
// ---------------------------------------------------------------------------

static inline __attribute__((format(printf, 3, 4))) void
stmak_log_debugf(const stmak_log_t *log, const char *component,
                const char *fmt, ...) STMAK_NONBLOCKING {
    va_list ap;
    va_start(ap, fmt);
    stmak_log_emit(log, STMAK_LOG_DEBUG, component, fmt, ap);
    va_end(ap);
}

static inline __attribute__((format(printf, 3, 4))) void
stmak_log_infof(const stmak_log_t *log, const char *component,
               const char *fmt, ...) STMAK_NONBLOCKING {
    va_list ap;
    va_start(ap, fmt);
    stmak_log_emit(log, STMAK_LOG_INFO, component, fmt, ap);
    va_end(ap);
}

// --- the general entry point ---------------------------------------------
//
// Takes the whole level word, so a caller composes severity and flags itself:
//
//     stmak_logf(log, comp, STMAK_LOG_ERROR | STMAK_LOG_OPER, "...", ...);
//
// There is deliberately no stmak_log_oper_errorf, nor any other name encoding
// a flag.  Names would have to cover the cross-product of levels and flags:
// one flag already means four extra helpers, a second means sixteen, and each
// one is a place for the two axes to drift apart.  The four bare-level
// wrappers below stay because they are the common case and they do NOT
// multiply -- they are just stmak_logf with a constant.

static inline __attribute__((format(printf, 4, 5))) void
stmak_logf(const stmak_log_t *log, const char *component,
           uint32_t level, const char *fmt, ...) STMAK_NONBLOCKING {
    va_list ap;
    va_start(ap, fmt);
    stmak_log_emit(log, (stmak_log_level_t)level, component, fmt, ap);
    va_end(ap);
}

static inline __attribute__((format(printf, 3, 4))) void
stmak_log_warnf(const stmak_log_t *log, const char *component,
               const char *fmt, ...) STMAK_NONBLOCKING {
    va_list ap;
    va_start(ap, fmt);
    stmak_log_emit(log, STMAK_LOG_WARN, component, fmt, ap);
    va_end(ap);
}

// Log-only: an error the operator can do nothing about.  The operator-facing
// variant above is the usual one; this exists for the rare message that would
// only be noise on a panel.
static inline __attribute__((format(printf, 3, 4))) void
stmak_log_errorf(const stmak_log_t *log, const char *component,
                const char *fmt, ...) STMAK_NONBLOCKING {
    va_list ap;
    va_start(ap, fmt);
    stmak_log_emit(log, STMAK_LOG_ERROR, component, fmt, ap);
    va_end(ap);
}

// ---------------------------------------------------------------------------
// Ring buffer management — used by the Go drain loop.
// ---------------------------------------------------------------------------

// Allocate a log ring buffer with every slot on its first turn.
static inline stmak_log_ring_t *stmak_ring_create(void) {
    stmak_log_ring_t *r = (stmak_log_ring_t *)calloc(1, sizeof(stmak_log_ring_t));
    if (!r) return NULL;
    for (uint32_t i = 0; i < STMAK_LOG_RING_SIZE; i++) {
        r->slots[i].seq = i;
    }
    return r;
}

// Free a log ring buffer.
static inline void stmak_ring_destroy(stmak_log_ring_t *r) {
    free(r);
}

// Set the severity floor: messages below it are not enqueued.
static inline void stmak_ring_set_min_level(stmak_log_ring_t *ring, uint32_t min_level) {
    __atomic_store_n(&ring->min_level, min_level, __ATOMIC_RELAXED);
}

// Read up to max published messages, in order, into out, and hand their
// slots on to the next lap.  Returns the number read.  Stops at the first
// slot not yet published -- free, or claimed by a producer still copying --
// so the order of positions is the order of delivery.
//
// This is the whole consumer.  Copying a batch out and releasing the slots
// before anything is formatted or written keeps the ring's throughput
// independent of the sink's: a burst that arrived while the drain could
// not run is out of the ring one call later, not one write(2) later.
static inline uint32_t
stmak_ring_read_batch(stmak_log_ring_t *ring, stmak_log_slot_t *out, uint32_t max) {
    uint32_t pos = __atomic_load_n(&ring->read_pos, __ATOMIC_RELAXED);
    uint32_t n = 0;
    while (n < max) {
        stmak_log_slot_t *slot = &ring->slots[pos & STMAK_LOG_RING_MASK];
        if (__atomic_load_n(&slot->seq, __ATOMIC_ACQUIRE) != pos + 1) {
            break;
        }
        out[n].level = slot->level;
        out[n].timestamp_ns = slot->timestamp_ns;
        memcpy(out[n].component, slot->component, STMAK_LOG_COMPONENT_LEN);
        memcpy(out[n].msg, slot->msg, STMAK_LOG_MSG_LEN);
        __atomic_store_n(&slot->seq, pos + STMAK_LOG_RING_SIZE, __ATOMIC_RELEASE);
        pos++;
        n++;
    }
    __atomic_store_n(&ring->read_pos, pos, __ATOMIC_RELAXED);
    return n;
}

// ---------------------------------------------------------------------------
// Subscriber poll — read one message from a subscription's ring.
// Returns 1 if a message was read, 0 if no message available.
// ---------------------------------------------------------------------------

static inline int
stmak_log_sub_poll(stmak_log_sub_t *sub,
                  uint32_t *out_level, char *out_component, char *out_msg) {
    stmak_log_slot_t m;
    if (stmak_ring_read_batch(sub->ring, &m, 1) == 0) return 0;
    *out_level = m.level;
    memcpy(out_component, m.component, STMAK_LOG_COMPONENT_LEN);
    memcpy(out_msg, m.msg, STMAK_LOG_MSG_LEN);
    return 1;
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_LOG_H
