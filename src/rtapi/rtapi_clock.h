/*
 * rtapi_clock.h — the RTAPI timebase.
 *
 * All RTAPI time — rtapi_get_time(), the task deadlines and with them the
 * task PLL reference — is CLOCK_MONOTONIC_RAW.  CLOCK_MONOTONIC is not used
 * as timebase because NTP slews its rate: systemd-timesyncd hands an initial
 * offset below 0.4 s to the kernel PLL, which then makes CLOCK_MONOTONIC run
 * several percent fast or slow for tens of seconds (measured: -29400 ppm
 * for a 250 ms offset).  A task period derived from it bends by the same
 * amount, far beyond what an EtherCAT DC PLL can follow.  RAW only follows
 * the oscillator.
 *
 * clock_nanosleep() does not accept CLOCK_MONOTONIC_RAW, so an absolute RAW
 * deadline is converted to CLOCK_MONOTONIC right before sleeping, using the
 * MONOTONIC/RAW rate measured between successive samples.  The conversion
 * error does not accumulate: the deadlines themselves stay in RAW time.
 *
 * Header-only so that every RTAPI implementation (uspace_rtapi_lib.c,
 * librtapi_core.so) shares this one definition.  RTAPI time must never be
 * compared with CLOCK_MONOTONIC values taken elsewhere — the two drift
 * apart whenever NTP slews.
 */
#ifndef RTAPI_CLOCK_H
#define RTAPI_CLOCK_H

#include <time.h>

#define RTAPI_CLOCK_ID CLOCK_MONOTONIC_RAW

/* Plausible MONOTONIC/RAW rates.  The kernel NTP correction stays well
   inside: +-500 ppm frequency plus the offset slew, at most 12.5 % (0.5 s
   offset at time constant 0). */
#define RTAPI_CLOCK_RATE_MIN 0.85
#define RTAPI_CLOCK_RATE_MAX 1.15

/* Shortest RAW interval the rate is measured over.  The two clock reads of a
   sample are some 10 ns apart, so this bounds the rate noise to ~100 ppm,
   i.e. ~100 ns on a 1 ms sleep.  Faster tasks keep their reference sample
   until enough time has passed. */
#define RTAPI_CLOCK_RATE_MIN_INTERVAL_NS 100000LL

static inline long long rtapi_clock_ts2ns(const struct timespec *ts) {
    return ts->tv_sec * 1000000000LL + ts->tv_nsec;
}

static inline void rtapi_clock_ns2ts(long long ns, struct timespec *ts) {
    ts->tv_sec = ns / 1000000000LL;
    ts->tv_nsec = ns % 1000000000LL;
}

/* Current RTAPI time in ns.  vDSO read — no syscall, no lock. */
static inline long long rtapi_clock_now(void) {
    struct timespec ts;
    clock_gettime(RTAPI_CLOCK_ID, &ts);
    return rtapi_clock_ts2ns(&ts);
}

/* Per-task state for sleeping until RAW deadlines. */
struct rtapi_clock_sleeper {
    long long now_raw;   /* latest sample pair */
    long long now_mono;
    long long ref_raw;   /* sample pair the rate is measured against */
    long long ref_mono;
    double rate;         /* MONOTONIC ns per RAW ns */
};

/* Take a RAW/MONOTONIC sample pair and update the rate.  Returns the RAW
   time of the sample. */
static inline long long rtapi_clock_sleeper_sample(struct rtapi_clock_sleeper *s) {
    struct timespec raw, mono;
    clock_gettime(RTAPI_CLOCK_ID, &raw);
    clock_gettime(CLOCK_MONOTONIC, &mono);
    s->now_raw = rtapi_clock_ts2ns(&raw);
    s->now_mono = rtapi_clock_ts2ns(&mono);

    long long draw = s->now_raw - s->ref_raw;
    if (draw >= RTAPI_CLOCK_RATE_MIN_INTERVAL_NS) {
        double rate = (double)(s->now_mono - s->ref_mono) / (double)draw;
        if (rate >= RTAPI_CLOCK_RATE_MIN && rate <= RTAPI_CLOCK_RATE_MAX) {
            s->rate = rate;
        }
        s->ref_raw = s->now_raw;
        s->ref_mono = s->now_mono;
    }
    return s->now_raw;
}

/* Start a sleeper: first sample, nominal rate until a second one exists. */
static inline long long rtapi_clock_sleeper_init(struct rtapi_clock_sleeper *s) {
    s->rate = 1.0;
    s->ref_raw = 0;
    s->ref_mono = 0;
    rtapi_clock_sleeper_sample(s);
    s->ref_raw = s->now_raw;
    s->ref_mono = s->now_mono;
    return s->now_raw;
}

/* CLOCK_MONOTONIC equivalent of a RAW deadline, based on the latest sample.
   'mono_now' receives that sample's MONOTONIC time. */
static inline void rtapi_clock_sleeper_target(const struct rtapi_clock_sleeper *s,
        long long deadline_raw, struct timespec *mono_target, struct timespec *mono_now) {
    long long dmono = (long long)((double)(deadline_raw - s->now_raw) * s->rate);
    rtapi_clock_ns2ts(s->now_mono + dmono, mono_target);
    rtapi_clock_ns2ts(s->now_mono, mono_now);
}

#endif /* RTAPI_CLOCK_H */
