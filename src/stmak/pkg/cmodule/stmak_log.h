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
//   stmak_log_errorf(env->log, "mycomp", "init failed: %s", reason);

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
} stmak_log_level_t;

// ---------------------------------------------------------------------------
// Ring buffer slot — fixed-size, cache-line aligned.
// ---------------------------------------------------------------------------

#define STMAK_LOG_MSG_LEN       216
#define STMAK_LOG_COMPONENT_LEN (STMAK_RTAPI_NAME_LEN + 1)

typedef struct {
    uint32_t          seq;                              // sequence number (0 = free)
    uint32_t          level;                            // stmak_log_level_t
    int64_t           timestamp_ns;                     // CLOCK_MONOTONIC nanoseconds
    char              component[STMAK_LOG_COMPONENT_LEN];
    char              msg[STMAK_LOG_MSG_LEN];
} stmak_log_slot_t;

// Slot is 264 bytes (4+4+8+32+216).  Adjust STMAK_LOG_MSG_LEN if exact
// power-of-2 alignment is desired.

// ---------------------------------------------------------------------------
// Ring buffer — single shared instance per launcher process.
// Multiple producers (C threads), single consumer (Go drain goroutine).
// ---------------------------------------------------------------------------

#define STMAK_LOG_RING_SIZE_SHIFT 10
#define STMAK_LOG_RING_SIZE       (1u << STMAK_LOG_RING_SIZE_SHIFT)  // 1024 slots
#define STMAK_LOG_RING_MASK       (STMAK_LOG_RING_SIZE - 1)

typedef struct {
    // Producer side — each writer does atomic fetch-add on write_pos to
    // claim a slot, then fills it and publishes via store-release on seq.
    uint32_t write_pos;

    // Consumer side — the Go drain goroutine tracks its own read position.
    // Padding avoids false sharing between producer and consumer cache lines.
    char _pad[60];
    uint32_t read_pos;

    // Slot array.
    stmak_log_slot_t slots[STMAK_LOG_RING_SIZE];
} stmak_log_ring_t;

// ---------------------------------------------------------------------------
// Subscription handle — per-subscriber ring for fan-out from the drain loop.
// Allocated by subscribe(), freed by unsubscribe().
// ---------------------------------------------------------------------------

typedef struct {
    stmak_log_ring_t *ring;      // per-subscriber ring (filled by Go drain)
    uint32_t         read_pos;  // consumer's read position
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
// wrapper so the ring logic in stmak_log_emit stays compiler-verified.
static inline void stmak_log_vformat(char *dst, size_t len, const char *fmt,
                                    va_list ap) STMAK_NONBLOCKING;
STMAK_NONBLOCKING_TRUSTED_BEGIN
static inline void stmak_log_vformat(char *dst, size_t len, const char *fmt,
                                    va_list ap) {
    vsnprintf(dst, len, fmt, ap);
}
STMAK_NONBLOCKING_TRUSTED_END

// Low-level enqueue: claim a slot, format the message, publish.
// Returns 0 on success, -1 if the ring is full (message dropped).
// The slot claim/publish is lock-free (atomics on fixed slots,
// drop-on-full instead of blocking) and compiler-verified; the only
// trusted piece is the stmak_log_vformat wrapper above.
static inline int
stmak_log_emit(const stmak_log_t *log, stmak_log_level_t level,
              const char *component, const char *fmt, va_list ap)
    STMAK_NONBLOCKING;
static inline int
stmak_log_emit(const stmak_log_t *log, stmak_log_level_t level,
              const char *component, const char *fmt, va_list ap) {
    if (!log) return -1;
    stmak_log_ring_t *ring = log->ring;

    // Claim a slot (lock-free, multi-producer safe).
    uint32_t pos = __atomic_fetch_add(&ring->write_pos, 1, __ATOMIC_RELAXED);
    uint32_t idx = pos & STMAK_LOG_RING_MASK;
    stmak_log_slot_t *slot = &ring->slots[idx];

    // Check if the consumer has drained this slot (seq == 0 means free).
    // If not, the ring is full — drop the message to avoid blocking.
    uint32_t expected = 0;
    if (!__atomic_compare_exchange_n(&slot->seq, &expected, 1, 0,
                                     __ATOMIC_ACQUIRE, __ATOMIC_RELAXED)) {
        // Ring full — message dropped.  This should be rare if the Go drain
        // goroutine keeps up.  A dropped-message counter could be added here.
        return -1;
    }

    // Fill the slot.
    slot->level = (uint32_t)level;
    slot->timestamp_ns = stmak_log_now_ns();
    strncpy(slot->component, component, STMAK_RTAPI_NAME_LEN);
    slot->component[STMAK_RTAPI_NAME_LEN] = '\0';
    stmak_log_vformat(slot->msg, STMAK_LOG_MSG_LEN, fmt, ap);

    // Publish: set seq to pos+1 so the consumer knows this slot is ready.
    // The consumer reads slots in order and waits for seq == expected_seq.
    __atomic_store_n(&slot->seq, pos + 1, __ATOMIC_RELEASE);

    return 0;
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

static inline __attribute__((format(printf, 3, 4))) void
stmak_log_warnf(const stmak_log_t *log, const char *component,
               const char *fmt, ...) STMAK_NONBLOCKING {
    va_list ap;
    va_start(ap, fmt);
    stmak_log_emit(log, STMAK_LOG_WARN, component, fmt, ap);
    va_end(ap);
}

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

// Allocate a zeroed log ring buffer.
static inline stmak_log_ring_t *stmak_ring_create(void) {
    return (stmak_log_ring_t *)calloc(1, sizeof(stmak_log_ring_t));
}

// Free a log ring buffer.
static inline void stmak_ring_destroy(stmak_log_ring_t *r) {
    free(r);
}

// Try to read the next slot from the ring at the given read position.
// Returns 1 if a slot was read (output params filled), 0 otherwise.
static inline int
stmak_ring_try_read(stmak_log_ring_t *ring, uint32_t read_pos,
                   uint32_t *out_level, int64_t *out_ts,
                   char *out_component, char *out_msg) {
    uint32_t idx = read_pos & STMAK_LOG_RING_MASK;
    stmak_log_slot_t *slot = &ring->slots[idx];

    uint32_t seq = __atomic_load_n(&slot->seq, __ATOMIC_ACQUIRE);
    if (seq != read_pos + 1) {
        return 0;  // slot not ready yet
    }

    *out_level = slot->level;
    *out_ts = slot->timestamp_ns;
    memcpy(out_component, slot->component, STMAK_RTAPI_NAME_LEN + 1);
    memcpy(out_msg, slot->msg, STMAK_LOG_MSG_LEN);

    // Release the slot for reuse.
    __atomic_store_n(&slot->seq, 0, __ATOMIC_RELEASE);
    return 1;
}

// ---------------------------------------------------------------------------
// Subscriber poll — read one message from a subscription's ring.
// Returns 1 if a message was read, 0 if no message available.
// ---------------------------------------------------------------------------

static inline int
stmak_log_sub_poll(stmak_log_sub_t *sub,
                  uint32_t *out_level, char *out_component, char *out_msg) {
    int64_t ts;  // discarded — subscribers don't need timestamps
    int ok = stmak_ring_try_read(sub->ring, sub->read_pos,
                                out_level, &ts, out_component, out_msg);
    if (ok) sub->read_pos++;
    return ok;
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_LOG_H
