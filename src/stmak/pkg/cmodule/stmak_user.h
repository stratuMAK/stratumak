/*
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: LGPL Version 2.1
 */
// stmak_user.h — Userspace module helpers for stmak C modules.
//
// Provides an eventfd-based exit notification mechanism that integrates
// cleanly with select()/poll()/epoll() main loops.
//
// Usage in a simple polling loop:
//   void user_mainloop(void) {
//       while (!STMAK_SHOULD_EXIT()) {
//           // ... do work ...
//           usleep(100000);
//       }
//   }
//
// Usage in a select()-based loop:
//   void user_mainloop(void) {
//       int efd = STMAK_EXIT_FD();
//       int devfd = open("/dev/mydevice", O_RDONLY);
//       while (1) {
//           fd_set rfds;
//           FD_ZERO(&rfds);
//           FD_SET(efd, &rfds);
//           FD_SET(devfd, &rfds);
//           int nfds = (efd > devfd ? efd : devfd) + 1;
//           if (select(nfds, &rfds, NULL, NULL, NULL) < 0) break;
//           if (FD_ISSET(efd, &rfds)) break;  // exit requested
//           if (FD_ISSET(devfd, &rfds)) { /* handle device data */ }
//       }
//       close(devfd);
//   }

#ifndef STMAK_USER_H
#define STMAK_USER_H

#include <sys/eventfd.h>
#include <unistd.h>
#include <poll.h>
#include <stdint.h>
#include <errno.h>

#ifdef __cplusplus
extern "C" {
#endif

// stmak_should_exit — non-blocking check whether the exit eventfd is signalled.
// Returns non-zero if the module should exit.
static inline int stmak_should_exit(int exit_fd) __attribute__((unused));
static inline int stmak_should_exit(int exit_fd) {
    struct pollfd pfd = { .fd = exit_fd, .events = POLLIN };
    return poll(&pfd, 1, 0) > 0;
}

// stmak_signal_exit — signal the exit eventfd (called by generated Stop()).
static inline void stmak_signal_exit(int exit_fd) __attribute__((unused));
static inline void stmak_signal_exit(int exit_fd) {
    uint64_t val = 1;
    ssize_t rc;
    do {
        rc = write(exit_fd, &val, sizeof(val));
    } while (rc < 0 && errno == EINTR);
    // No further handling needed: an eventfd write only fails once the
    // counter is saturated (EAGAIN), and a saturated counter already reads
    // as POLLIN — the exit condition stmak_should_exit() polls for is set
    // either way.
    (void)rc;
}

#ifdef __cplusplus
}
#endif

#endif // STMAK_USER_H
