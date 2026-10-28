//
// siglog.c — record the change history of HAL signals to a binary file (cmod)
//
// Once loaded and addf'ed, every cycle compares the value of each selected
// signal with the one it saw last and logs the ones that changed, tagged with
// the cycle number, for the whole lifetime of the instance.
// scripts/siglog-decode turns the file into text, CSV or VCD.
//
// Usage:
//   load siglog file=siglog-%Y%m%d-%H%M%S.bin [include=glob,...] [exclude=glob,...] [depth=N]
//   addf siglog.sample servo-thread        # once, LAST in the thread
//
//   file=     output file; strftime conversions (%Y, %H, ... ; %% for a
//             literal %) are expanded with the local time at load, so every
//             run gets its own file.  A name without them is overwritten.
//   include=  comma-separated fnmatch globs; when given, only matching signals
//             are logged (default: all)
//   exclude=  comma-separated globs removed from that set afterwards
//   depth=    RT -> I/O ring size in entries (rounded up to a power of two,
//             raised to fit at least four full snapshots)
//
// Pins:
//   <name>.active    bit out  logging runs (set in the first logged cycle)
//   <name>.signals   s32 out  number of signals logged (0 until the snapshot)
//   <name>.overruns  s32 out  cycles whose changes were dropped (ring full)
//
// Design:
//   - The signal set is taken once, at the first RT call (i.e. when the HAL
//     threads actually start), because only then is the wiring complete --
//     cmod Start() runs before the HAL file's `net` lines in halrun mode and
//     on a runtime `load`.  Until the I/O thread has published the table, the
//     RT function only counts cycles.  Signals created later are not logged;
//     a changed signal set is reported once as a warning.  Deleting a logged
//     signal is harmless: HAL never frees the value slot, so it just freezes.
//   - The RT function writes one frame per cycle with changes into an SPSC
//     ring; a frame that does not fit is dropped whole, counted, and the
//     next cycle logs every signal again (a full snapshot), so the file stays
//     consistent after a loss.  The first logged cycle is a full snapshot too.
//   - A non-RT I/O thread encodes the frames and writes the file.  A write
//     error (disk full) is logged once and ends logging for good: .active
//     drops and the file keeps what was written before, without END.
//   - Everything RT touches (instance, table, shadow, ring) comes from
//     rtapi calloc, which locks and pre-faults it: rtapi uses no
//     MCL_FUTURE, so plain heap memory could page-fault in the servo thread.
//   - Port signals are never logged.  Signals written in other threads are
//     only seen at the rate of the thread siglog runs in.
//
// File format (version 1, little endian; varint = unsigned LEB128,
// zvarint = zigzag + varint):
//   header:
//     "STMSLOG\0"                          8 bytes magic
//     u32 version                          1
//     u32 period_ns                        period of the thread siglog runs in
//     i64 realtime_ns                      CLOCK_REALTIME   } taken together,
//     i64 monoraw_ns                       CLOCK_MONOTONIC_RAW } maps TIME to wall clock
//     str include, str exclude             as given (str = varint len + bytes)
//     varint nsig, nsig x (u8 type, str name)   type: 1 bit, 2 float, 3 s32, 4 u32
//   records (u8 tag + payload), each moves a cycle cursor forward by dcycle:
//     0x01 CYCLE  varint dcycle, varint n, n x (varint sig index, value)
//                 value: bit u8 | float f64 | s32 zvarint | u32 varint
//     0x02 LOSS   varint dcycle (to the first dropped cycle), varint ncycles
//     0x03 TIME   varint dcycle, u64 monoraw_ns (rtapi_get_time in that cycle)
//     0x04 END    varint dcycle (to the last cycle run); clean close
//   Cycle 0 is the first call of the RT function.  A file without END was
//   not closed cleanly; everything before its last complete record is valid.
//
// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
//

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <errno.h>
#include <fnmatch.h>
#include <time.h>
#include <unistd.h>
#include <pthread.h>

#include "stmak_env.h"

#define DEFAULT_DEPTH   65536
#define MAX_PATTERNS    64
#define IO_POLL_US      1000           // poll bounds; the actual interval
#define IO_POLL_MAX_US  10000          // follows the ring's capacity
#define FLUSH_NS        1000000000LL   // fflush + signal-set check interval
#define TIME_NS         1000000000LL   // TIME record interval

#define FILE_MAGIC      "STMSLOG"      // + NUL = 8 bytes
#define FILE_VERSION    1

#define REC_CYCLE       0x01
#define REC_LOSS        0x02
#define REC_TIME        0x03
#define REC_END         0x04

// Ring entry.  A frame is a CYCLE mark followed by `aux` entries: an optional
// LOSS mark, an optional TIME mark, then the changed values.
#define MARK_CYCLE      0xFFFFFFFFu    // aux = entries following, val = cycle
#define MARK_LOSS       0xFFFFFFFEu    // aux = dropped cycles, val = first dropped
#define MARK_TIME       0xFFFFFFFDu    // val = rtapi_get_time() ns

typedef struct {
    uint32_t id;
    uint32_t aux;
    uint64_t val;
} sl_entry_t;

typedef struct {
    char name[STMAK_HAL_NAME_LEN + 1];
    int type;
    volatile void *data;
} sl_sig_t;

enum { PHASE_WAIT = 0, PHASE_SNAPSHOT, PHASE_RUN, PHASE_FAILED };

typedef struct {
    cmod_env_t env;
    cmod_t mod;
    int comp_id;
    char name[64];

    // configuration
    char *include, *exclude;               // as given ("" if absent)
    char *inc_pat[MAX_PATTERNS], *exc_pat[MAX_PATTERNS];
    int n_inc, n_exc;
    int depth;
    FILE *fp;

    // pins
    volatile unsigned *active;
    volatile int32_t *signals;
    volatile int32_t *overruns;

    // RT <-> I/O handshake
    volatile int phase;
    volatile long period;                  // set by the first RT call

    // signal table + ring, owned by the I/O thread until PHASE_RUN
    int nsig;
    sl_sig_t *sigs;
    uint64_t *shadow;                      // last logged value per signal
    sl_entry_t *ring;
    uint32_t mask;
    volatile uint32_t wpos;                // written by RT only
    volatile uint32_t rpos;                // written by I/O thread only

    // RT state
    uint64_t cycle;                        // counts every RT call
    int resync;                            // log every signal next frame
    uint32_t lost;                         // dropped cycles not yet reported
    uint64_t first_lost;
    int64_t time_every;                    // cycles between TIME marks
    int64_t time_due;

    // I/O thread state
    uint64_t file_cycle;                   // cycle cursor of the file
    useconds_t poll_us;
    unsigned gen;                          // last struct_generation seen
    uint64_t set_fp;                       // fingerprint of the full signal set
    int set_warned;
    pthread_t io_tid;
    volatile int io_stop;
    int io_running;
} siglog_t;

// ---------------------------------------------------------------------------
// RT function
// ---------------------------------------------------------------------------

static inline uint64_t read_value(const sl_sig_t *s) STMAK_NONBLOCKING {
    uint64_t v = 0;
    switch (s->type) {
    // The low byte only: HAL writes bits as bool, cmods as unsigned.
    case STMAK_HAL_BIT:   v = *(volatile uint8_t *)s->data != 0; break;
    case STMAK_HAL_S32:   v = (uint32_t)*(volatile int32_t *)s->data; break;
    case STMAK_HAL_U32:   v = *(volatile uint32_t *)s->data; break;
    // Compared as bits, so NaN and -0.0 neither hide nor invent a change.
    case STMAK_HAL_FLOAT: v = *(volatile uint64_t *)s->data; break;
    }
    return v;
}

static void sample_funct(void *arg, long period) STMAK_NONBLOCKING {
    siglog_t *m = (siglog_t *)arg;
    uint64_t cycle = m->cycle++;

    if (m->phase != PHASE_RUN) {
        *(m->active) = 0;
        if (m->phase == PHASE_WAIT) {
            m->period = period;
            __sync_synchronize();
            m->phase = PHASE_SNAPSHOT;
        }
        return;
    }
    __sync_synchronize();     // see the table published with PHASE_RUN
    *(m->active) = 1;

    uint32_t start = m->wpos;
    uint32_t space = m->mask + 1 - (start - m->rpos);
    uint32_t k = 1;           // slot 0 of the frame is the CYCLE mark
    int full = 0;

    #define PUT(i, a, v)                                                       \
        do {                                                                   \
            if (k >= space) { full = 1; break; }                               \
            sl_entry_t *e_ = &m->ring[(start + k++) & m->mask];                \
            e_->id = (i); e_->aux = (a); e_->val = (v);                        \
        } while (0)

    if (m->lost)
        PUT(MARK_LOSS, m->lost, m->first_lost);
    if (!full && --m->time_due <= 0) {
        PUT(MARK_TIME, 0, (uint64_t)m->env.rtapi->get_time(m->env.rtapi->ctx));
        m->time_due = m->time_every;
    }
    for (int n = 0; n < m->nsig && !full; n++) {
        uint64_t v = read_value(&m->sigs[n]);
        if (v == m->shadow[n] && !m->resync) continue;
        m->shadow[n] = v;
        PUT((uint32_t)n, 0, v);
    }
    #undef PUT

    if (full) {
        // Drop the frame; the shadow is half-updated, so log everything again.
        if (m->lost == 0) m->first_lost = cycle;
        if (m->lost != UINT32_MAX) m->lost++;
        m->resync = 1;
        m->time_due = 0;
        (*(m->overruns))++;
        return;
    }
    if (k == 1) return;       // nothing changed

    sl_entry_t *mark = &m->ring[start & m->mask];
    mark->id = MARK_CYCLE; mark->aux = k - 1; mark->val = cycle;
    m->lost = 0;
    m->resync = 0;
    __sync_synchronize();     // publish the frame before advancing the index
    m->wpos = start + k;
}

// ---------------------------------------------------------------------------
// Encoding helpers (I/O thread)
// ---------------------------------------------------------------------------

// Only the I/O thread (and Destroy, after joining it) touches the FILE, so
// the stdio lock buys nothing; it would be taken once per byte.
static void put_u8(FILE *f, unsigned v) { fputc_unlocked((int)(v & 0xFF), f); }

static void put_le(FILE *f, uint64_t v, int bytes) {
    for (int i = 0; i < bytes; i++) put_u8(f, (unsigned)(v >> (8 * i)));
}

static void put_varint(FILE *f, uint64_t v) {
    while (v >= 0x80) { put_u8(f, (unsigned)(v & 0x7F) | 0x80); v >>= 7; }
    put_u8(f, (unsigned)v);
}

static void put_str(FILE *f, const char *s) {
    size_t len = strlen(s);
    put_varint(f, len);
    fwrite_unlocked(s, 1, len, f);
}

static void put_value(FILE *f, int type, uint64_t v) {
    switch (type) {
    case STMAK_HAL_BIT:   put_u8(f, (unsigned)v); break;
    case STMAK_HAL_FLOAT: put_le(f, v, 8); break;
    case STMAK_HAL_S32: {
        int32_t s = (int32_t)(uint32_t)v;
        put_varint(f, (uint32_t)((uint32_t)s << 1) ^ (uint32_t)(s >> 31));
        break;
    }
    case STMAK_HAL_U32:   put_varint(f, v); break;
    }
}

// Advance the file's cycle cursor to `cycle` and return the delta.
static uint64_t dcycle(siglog_t *m, uint64_t cycle) {
    uint64_t d = cycle - m->file_cycle;
    m->file_cycle = cycle;
    return d;
}

// Flush and check for a write error; on one, log it and stop logging.
// Returns 0 while the file is healthy.
static int flush_checked(siglog_t *m) {
    if (fflush(m->fp) == 0 && !ferror(m->fp)) return 0;
    int err = errno;
    m->phase = PHASE_FAILED;
    stmak_logf(m->env.log, m->name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
               "write error (%s); logging stopped", strerror(err ? err : EIO));
    return -1;
}

static int64_t clock_ns(clockid_t id) {
    struct timespec ts;
    clock_gettime(id, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

// ---------------------------------------------------------------------------
// Signal selection + snapshot (I/O thread)
// ---------------------------------------------------------------------------

typedef struct {
    siglog_t *m;
    int inc_hits[MAX_PATTERNS], exc_hits[MAX_PATTERNS];
    int total;
    uint64_t fp;
    int count_only;
    int oom;
    sl_sig_t *tab;            // selected signals (plain heap, copied later)
    int ntab, cap;
} select_ctx_t;

// FNV-1a over name, type and value slot of every signal, so a changed set is
// noticed -- including a signal deleted and created again under its name.
static uint64_t fp_add(uint64_t h, const char *name, int type,
                       volatile void *data) {
    for (const char *p = name; *p; p++) { h ^= (uint8_t)*p; h *= 0x100000001b3ULL; }
    h ^= (uint64_t)type; h *= 0x100000001b3ULL;
    h ^= (uint64_t)(uintptr_t)data; h *= 0x100000001b3ULL;
    return h;
}

static int select_cb(void *user, const char *name, int type, volatile void *data) {
    select_ctx_t *c = (select_ctx_t *)user;
    siglog_t *m = c->m;
    int hit = 0;

    c->fp = fp_add(c->fp, name, type, data);
    if (c->count_only || type == STMAK_HAL_PORT) return 0;
    c->total++;

    if (m->n_inc == 0) hit = 1;
    for (int i = 0; i < m->n_inc; i++)
        if (fnmatch(m->inc_pat[i], name, 0) == 0) { c->inc_hits[i]++; hit = 1; }
    if (!hit) return 0;
    for (int i = 0; i < m->n_exc; i++)
        if (fnmatch(m->exc_pat[i], name, 0) == 0) { c->exc_hits[i]++; hit = 0; }
    if (!hit) return 0;

    if (c->ntab == c->cap) {  // doubling: the walk holds the HAL mutex
        int cap = c->cap ? 2 * c->cap : 64;
        sl_sig_t *grown = realloc(c->tab, (size_t)cap * sizeof(*grown));
        if (!grown) { c->oom = 1; return 1; }
        c->tab = grown;
        c->cap = cap;
    }
    sl_sig_t *s = &c->tab[c->ntab++];
    snprintf(s->name, sizeof(s->name), "%s", name);
    s->type = type;
    s->data = data;
    return 0;
}

static uint64_t signal_set_fp(siglog_t *m) {
    select_ctx_t c;
    memset(&c, 0, sizeof(c));
    c.m = m;
    c.fp = 0xcbf29ce484222325ULL;
    c.count_only = 1;
    m->env.hal->signal_foreach(m->env.hal->ctx, select_cb, &c);
    return c.fp;
}

// Build the signal table and ring, write the header, then let RT run.
static int snapshot(siglog_t *m) {
    const stmak_hal_t *hal = m->env.hal;
    select_ctx_t c;
    memset(&c, 0, sizeof(c));
    c.m = m;
    c.fp = 0xcbf29ce484222325ULL;

    m->gen = hal->struct_generation(hal->ctx);
    int rc = hal->signal_foreach(hal->ctx, select_cb, &c);
    if (rc < 0 || c.oom) {
        stmak_logf(m->env.log, m->name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                   c.oom ? "out of memory for the signal table; not logging"
                         : "cannot enumerate signals; not logging");
        free(c.tab);
        return -1;
    }
    m->set_fp = c.fp;
    m->nsig = c.ntab;

    for (int i = 0; i < m->n_inc; i++)
        if (!c.inc_hits[i])
            stmak_logf(m->env.log, m->name, STMAK_LOG_WARN | STMAK_LOG_OPER,
                       "include pattern '%s' matches no signal", m->inc_pat[i]);
    for (int i = 0; i < m->n_exc; i++)
        if (!c.exc_hits[i])
            stmak_logf(m->env.log, m->name, STMAK_LOG_WARN | STMAK_LOG_OPER,
                       "exclude pattern '%s' matches no signal", m->exc_pat[i]);

    // Room for at least four full snapshots, so a resync can always land.
    uint64_t want = (uint64_t)m->depth;
    uint64_t need = 4 * ((uint64_t)m->nsig + 3);
    if (want < need) want = need;
    uint32_t size = 1;
    while (size < want && size < 0x80000000u) size <<= 1;
    const stmak_rtapi_t *rt = m->env.rtapi;
    m->sigs = rt->calloc(rt->ctx, ((size_t)m->nsig + 1) * sizeof(sl_sig_t));
    m->ring = rt->calloc(rt->ctx, (size_t)size * sizeof(sl_entry_t));
    m->shadow = rt->calloc(rt->ctx, ((size_t)m->nsig + 1) * sizeof(uint64_t));
    if (m->sigs && c.ntab)
        memcpy(m->sigs, c.tab, (size_t)c.ntab * sizeof(sl_sig_t));
    free(c.tab);
    if (!m->sigs || !m->ring || !m->shadow) {
        stmak_logf(m->env.log, m->name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                   "out of memory for %d signals; not logging", m->nsig);
        return -1;
    }
    m->mask = size - 1;

    long period = m->period > 0 ? m->period : 1000000;
    m->time_every = TIME_NS / period;
    if (m->time_every < 1) m->time_every = 1;
    m->time_due = 0;          // TIME in the first frame
    m->resync = 1;            // and every signal

    // Poll often enough to drain a quarter of the ring when every signal
    // changes every cycle, but no more often than that.
    uint64_t poll = (uint64_t)period * (size / ((uint64_t)m->nsig + 3)) / 4 / 1000;
    if (poll < IO_POLL_US) poll = IO_POLL_US;
    if (poll > IO_POLL_MAX_US) poll = IO_POLL_MAX_US;
    m->poll_us = (useconds_t)poll;

    FILE *f = m->fp;
    fwrite_unlocked(FILE_MAGIC, 1, 8, f);
    put_le(f, FILE_VERSION, 4);
    put_le(f, (uint64_t)period, 4);
    put_le(f, (uint64_t)clock_ns(CLOCK_REALTIME), 8);
    put_le(f, (uint64_t)clock_ns(CLOCK_MONOTONIC_RAW), 8);
    put_str(f, m->include);
    put_str(f, m->exclude);
    put_varint(f, (uint64_t)m->nsig);
    for (int n = 0; n < m->nsig; n++) {
        put_u8(f, (unsigned)m->sigs[n].type);
        put_str(f, m->sigs[n].name);
    }
    if (flush_checked(m) != 0) return -1;

    *(m->signals) = m->nsig;
    stmak_logf(m->env.log, m->name, STMAK_LOG_INFO,
               "logging %d of %d signals, period %ld ns, ring %u entries, poll %u us",
               m->nsig, c.total, period, size, (unsigned)m->poll_us);

    __sync_synchronize();     // publish the table before PHASE_RUN
    m->phase = PHASE_RUN;
    return 0;
}

// Warn once when signals were added or removed after the snapshot.
static void check_signal_set(siglog_t *m) {
    if (m->set_warned) return;
    unsigned gen = m->env.hal->struct_generation(m->env.hal->ctx);
    if (gen == m->gen) return;
    m->gen = gen;             // links bump it too; only the set matters
    if (signal_set_fp(m) == m->set_fp) return;
    m->set_warned = 1;
    stmak_logf(m->env.log, m->name, STMAK_LOG_WARN | STMAK_LOG_OPER,
               "HAL signals were added, deleted or re-created after logging "
               "started: new signals are not recorded, deleted ones keep their "
               "last value");
}

// ---------------------------------------------------------------------------
// I/O thread
// ---------------------------------------------------------------------------

static void drain(siglog_t *m) {
    FILE *f = m->fp;
    for (;;) {
        uint32_t wpos = m->wpos;
        __sync_synchronize();     // read the frame after seeing the index
        if (m->rpos == wpos) return;

        uint32_t r = m->rpos;
        sl_entry_t *mark = &m->ring[r & m->mask];
        uint64_t cycle = mark->val;
        uint32_t n = mark->aux;
        uint32_t i = 1, nvals;

        sl_entry_t *e = &m->ring[(r + i) & m->mask];
        if (i <= n && e->id == MARK_LOSS) {
            put_u8(f, REC_LOSS);
            put_varint(f, dcycle(m, e->val));
            put_varint(f, e->aux);
            e = &m->ring[(r + ++i) & m->mask];
        }
        if (i <= n && e->id == MARK_TIME) {
            put_u8(f, REC_TIME);
            put_varint(f, dcycle(m, cycle));
            put_le(f, e->val, 8);
            i++;
        }
        nvals = n + 1 - i;
        if (nvals > 0) {
            put_u8(f, REC_CYCLE);
            put_varint(f, dcycle(m, cycle));
            put_varint(f, nvals);
            for (; i <= n; i++) {
                e = &m->ring[(r + i) & m->mask];
                put_varint(f, e->id);
                put_value(f, m->sigs[e->id].type, e->val);
            }
        }
        __sync_synchronize();     // done reading before releasing the slots
        m->rpos = r + n + 1;
    }
}

static void *io_thread(void *arg) {
    siglog_t *m = (siglog_t *)arg;
    int64_t next_flush = 0;

    while (!m->io_stop) {
        if (m->phase == PHASE_SNAPSHOT) {
            __sync_synchronize();
            if (snapshot(m) != 0) break;
        }
        if (m->phase == PHASE_RUN) {
            drain(m);
            int64_t now = clock_ns(CLOCK_MONOTONIC);
            if (now >= next_flush) {
                if (flush_checked(m) != 0) break;
                check_signal_set(m);
                next_flush = now + FLUSH_NS;
            }
        }
        usleep(m->poll_us);
    }
    return NULL;
}

// ---------------------------------------------------------------------------
// Destroy
// ---------------------------------------------------------------------------

static void siglog_destroy(cmod_t *self) {
    siglog_t *m = (siglog_t *)self->priv;
    const stmak_rtapi_t *rt = m->env.rtapi;

    if (m->io_running) {
        m->io_stop = 1;
        pthread_join(m->io_tid, NULL);
    }
    if (m->fp) {
        if (m->phase == PHASE_RUN) {
            drain(m);
            put_u8(m->fp, REC_END);
            put_varint(m->fp, dcycle(m, m->cycle ? m->cycle - 1 : 0));
            flush_checked(m);
        }
        if (fclose(m->fp) != 0 && m->phase != PHASE_FAILED)
            stmak_logf(m->env.log, m->name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                       "closing the file failed: %s", strerror(errno));
    }
    if (m->comp_id > 0)
        m->env.hal->exit(m->env.hal->ctx, m->comp_id);
    for (int i = 0; i < m->n_inc; i++) free(m->inc_pat[i]);
    for (int i = 0; i < m->n_exc; i++) free(m->exc_pat[i]);
    free(m->include);
    free(m->exclude);
    if (m->sigs)   rt->free(rt->ctx, m->sigs);
    if (m->shadow) rt->free(rt->ctx, m->shadow);
    if (m->ring)   rt->free(rt->ctx, m->ring);
    rt->free(rt->ctx, m);
}

// ---------------------------------------------------------------------------
// New — constructor
// ---------------------------------------------------------------------------

// Split a comma-separated pattern list; empty items are skipped.
static int split_patterns(const char *list, char **out, int *n) {
    const char *p = list;
    while (*p) {
        const char *end = strchr(p, ',');
        size_t len = end ? (size_t)(end - p) : strlen(p);
        if (len > 0) {
            if (*n >= MAX_PATTERNS) return -E2BIG;
            out[*n] = strndup(p, len);
            if (!out[*n]) return -ENOMEM;
            (*n)++;
        }
        p += len;
        if (*p == ',') p++;
    }
    return 0;
}

int New(const cmod_env_t *env, const char *name,
        int argc, const char **argv, cmod_t **out)
{
    int retval;
    const char *file = NULL, *include = "", *exclude = "";
    int depth = DEFAULT_DEPTH;

    for (int i = 0; i < argc; i++) {
        if      (strncmp(argv[i], "file=", 5) == 0)    file    = argv[i] + 5;
        else if (strncmp(argv[i], "include=", 8) == 0) include = argv[i] + 8;
        else if (strncmp(argv[i], "exclude=", 8) == 0) exclude = argv[i] + 8;
        else if (strncmp(argv[i], "depth=", 6) == 0) {
            depth = atoi(argv[i] + 6);
            if (depth <= 0) depth = DEFAULT_DEPTH;
        }
        else {
            stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                       "unknown argument '%s'", argv[i]);
            return -EINVAL;
        }
    }

    if (!env->hal || !env->rtapi || !env->hal->signal_foreach) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                   "HAL/RTAPI API not available");
        return -EINVAL;
    }
    if (!file || !*file) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER, "siglog needs file=");
        return -EINVAL;
    }

    siglog_t *m = env->rtapi->calloc(env->rtapi->ctx, sizeof(siglog_t));
    if (!m) return -ENOMEM;
    m->env = *env;
    snprintf(m->name, sizeof(m->name), "%s", name);
    m->depth = depth;
    m->include = strdup(include);
    m->exclude = strdup(exclude);
    if (!m->include || !m->exclude) { retval = -ENOMEM; goto fail_early; }
    if ((retval = split_patterns(include, m->inc_pat, &m->n_inc)) != 0 ||
        (retval = split_patterns(exclude, m->exc_pat, &m->n_exc)) != 0) {
        if (retval == -E2BIG)
            stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                       "too many patterns (max %d per list)", MAX_PATTERNS);
        goto fail_early;
    }

    // strftime conversions in file= give every run its own file.
    char fname[4096];
    time_t now = time(NULL);
    struct tm tm;
    localtime_r(&now, &tm);
    if (strftime(fname, sizeof(fname), file, &tm) == 0) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                   "file '%s': name too long after expansion", file);
        retval = -ENAMETOOLONG; goto fail_early;
    }

    // file= is a write target: a relative name resolves under the config
    // directory only (stmak_path.h).  Opened now so a bad path fails the load.
    const char *err = NULL;
    const char *path = env->path->resolve(env->path->ctx, fname,
                                          STMAK_PATH_WRITE, &err);
    if (!path) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER, "file '%s': %s",
                   fname, err ? err : "cannot be resolved");
        retval = -ENOENT; goto fail_early;
    }
    m->fp = fopen(path, "wb");
    if (!m->fp) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER,
                   "cannot open file '%s': %s", fname, strerror(errno));
        retval = -ENOENT; goto fail_early;
    }
    setvbuf(m->fp, NULL, _IOFBF, 1 << 16);
    m->poll_us = IO_POLL_US;
    stmak_logf(env->log, name, STMAK_LOG_INFO, "writing to %s", path);

    m->comp_id = env->hal->init(env->hal->ctx, name, env->dl_handle,
                                STMAK_HAL_COMP_REALTIME);
    if (m->comp_id < 0) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER, "hal_init failed");
        retval = -1; goto fail_early;
    }

    retval = stmak_hal_pin_bit_newf(env->hal, STMAK_HAL_OUT,
                                    (stmak_hal_bit_t **)&m->active, m->comp_id,
                                    "%s.active", name);
    if (retval != 0) goto fail;
    retval = stmak_hal_pin_s32_newf(env->hal, STMAK_HAL_OUT,
                                    (stmak_hal_s32_t **)&m->signals, m->comp_id,
                                    "%s.signals", name);
    if (retval != 0) goto fail;
    retval = stmak_hal_pin_s32_newf(env->hal, STMAK_HAL_OUT,
                                    (stmak_hal_s32_t **)&m->overruns, m->comp_id,
                                    "%s.overruns", name);
    if (retval != 0) goto fail;
    *(m->active) = 0;
    *(m->signals) = 0;
    *(m->overruns) = 0;

    char fn[128];
    snprintf(fn, sizeof(fn), "%s.sample", name);
    retval = env->hal->export_funct(env->hal->ctx, fn, sample_funct, m,
                                    0, 0, m->comp_id);
    if (retval < 0) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER, "export .sample failed");
        goto fail;
    }

    if (pthread_create(&m->io_tid, NULL, io_thread, m) != 0) {
        stmak_logf(env->log, name, STMAK_LOG_ERROR | STMAK_LOG_OPER, "pthread_create failed");
        retval = -1; goto fail;
    }
    m->io_running = 1;

    env->hal->ready(env->hal->ctx, m->comp_id);

    m->mod.priv = m;
    m->mod.Init = NULL;
    m->mod.Start = NULL;
    m->mod.Stop = NULL;
    m->mod.Destroy = siglog_destroy;
    *out = &m->mod;
    return 0;

fail:
    env->hal->exit(env->hal->ctx, m->comp_id);
fail_early:
    if (m->fp) fclose(m->fp);
    for (int i = 0; i < m->n_inc; i++) free(m->inc_pat[i]);
    for (int i = 0; i < m->n_exc; i++) free(m->exc_pat[i]);
    free(m->include);
    free(m->exclude);
    env->rtapi->free(env->rtapi->ctx, m);
    return retval;
}
