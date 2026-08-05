#!/usr/bin/env python3
"""Self-test for lib/python/stmak_test.py.

The whole suite now synchronises through stmak_test, so a bug in its waiters
would not fail loudly — it would quietly reintroduce the sleep-then-assert
flakiness the helpers exist to remove (a waiter that returns early, or one that
never raises, both read as "the test passed"). These checks are pure: no server,
no HAL, no motion. They run anywhere runtests runs.
"""

import io
import os
import sys
import tempfile
import threading
import time
import urllib.error

import stmak_test

failures = []


def check(name, fn):
    try:
        fn()
    except Exception as e:
        failures.append("%s: %s" % (name, e))


def expect(cond, msg):
    if not cond:
        raise AssertionError(msg)


def test_returns_predicate_value_immediately():
    start = time.monotonic()
    got = stmak_test.wait_until(lambda: "value", "an already-true condition")
    expect(got == "value", "wait_until must return the predicate's value, got %r" % got)
    expect(time.monotonic() - start < 1.0, "an already-true predicate must not wait")


def test_raises_on_deadline_and_names_what_it_waited_for():
    try:
        stmak_test.wait_until(lambda: False, "the thing that never happens",
                             timeout=0.1)
    except stmak_test.Timeout as e:
        # The whole point of the helper is a failure that says what it wanted.
        expect("the thing that never happens" in str(e),
               "Timeout must name the condition, got: %s" % e)
        return
    raise AssertionError("wait_until must raise Timeout when the deadline expires")


def test_detail_is_included_in_the_failure():
    try:
        stmak_test.wait_until(lambda: False, "a condition", timeout=0.1,
                             detail=lambda: "observed=42")
    except stmak_test.Timeout as e:
        expect("observed=42" in str(e), "Timeout must include detail, got: %s" % e)
        return
    raise AssertionError("expected Timeout")


def test_broken_detail_does_not_mask_the_timeout():
    def boom():
        raise RuntimeError("diagnostic is broken")

    try:
        stmak_test.wait_until(lambda: False, "a condition", timeout=0.1,
                             detail=boom)
    except stmak_test.Timeout:
        return
    raise AssertionError("a broken detail callback must not swallow the Timeout")


def test_predicate_is_evaluated_at_least_once():
    calls = []

    def pred():
        calls.append(1)
        return True

    stmak_test.wait_until(pred, "a condition", timeout=0)
    expect(len(calls) >= 1, "predicate must run even with a zero timeout")


def test_predicate_becoming_true_late_is_seen():
    t0 = time.monotonic()
    stmak_test.wait_until(lambda: time.monotonic() - t0 > 0.2,
                         "a condition that becomes true during the wait",
                         timeout=10)


def test_scale_stretches_the_deadline():
    os.environ["STMAK_TEST_TIMEOUT_SCALE"] = "5"
    try:
        expect(stmak_test.scale() == 5.0, "scale() must read the env var")
        start = time.monotonic()
        try:
            stmak_test.wait_until(lambda: False, "a condition", timeout=0.1)
        except stmak_test.Timeout:
            pass
        waited = time.monotonic() - start
        # 0.1 * 5 = 0.5s. Assert it clearly outlasted the unscaled deadline
        # rather than pinning an exact duration.
        expect(waited > 0.3, "scale must stretch the deadline, waited only %.2fs" % waited)
    finally:
        del os.environ["STMAK_TEST_TIMEOUT_SCALE"]


def test_scale_ignores_garbage():
    os.environ["STMAK_TEST_TIMEOUT_SCALE"] = "not-a-number"
    try:
        expect(stmak_test.scale() == 1.0, "a malformed scale must fall back to 1.0")
    finally:
        del os.environ["STMAK_TEST_TIMEOUT_SCALE"]


def test_wait_file_contains_sees_a_late_write():
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "log")
        with open(path, "w") as f:
            f.write("starting\n")

        def append_later():
            time.sleep(0.2)
            with open(path, "a") as f:
                f.write("test RAN\n")

        t = threading.Thread(target=append_later)
        t.start()
        try:
            body = stmak_test.wait_file_contains(path, "test RAN", timeout=10)
            expect("test RAN" in body, "must return the file contents")
        finally:
            t.join()


def test_wait_file_contains_honours_count():
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "log")
        with open(path, "w") as f:
            f.write("hit\n")
        try:
            stmak_test.wait_file_contains(path, "hit", timeout=0.2, count=2)
        except stmak_test.Timeout:
            return
        raise AssertionError("count=2 must not be satisfied by a single occurrence")


def test_wait_file_stable_waits_for_the_writer_to_finish():
    # The writer's gap (0.05s) must sit comfortably inside the quiet window
    # (settle * (samples-1) = 0.2s) — that is wait_file_stable's contract. A
    # writer slower than the quiet window is outside what it can detect, and
    # parameterising the test that way would only be testing a coin flip.
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "log")
        with open(path, "w") as f:
            f.write("line\n")

        stop = threading.Event()

        def grow():
            for _ in range(4):
                if stop.is_set():
                    return
                time.sleep(0.05)
                with open(path, "a") as f:
                    f.write("line\n")

        t = threading.Thread(target=grow)
        t.start()
        try:
            body = stmak_test.wait_file_stable(path, timeout=10, settle=0.1)
            # The writer appends 5 lines total; a truncated read is exactly the
            # bug wait_file_stable exists to prevent.
            expect(body.count("line") == 5,
                   "expected the complete file, got %d lines" % body.count("line"))
        finally:
            stop.set()
            t.join()


def test_missing_file_times_out_rather_than_raising_oserror():
    try:
        stmak_test.wait_file_contains("/nonexistent/nope", "x", timeout=0.1)
    except stmak_test.Timeout:
        return
    raise AssertionError("a missing file must surface as Timeout, not OSError")


def test_wait_complete_raises_on_a_failed_wait():
    # A wait that did not happen used to arrive as -1 in a normal HTTP 200 body,
    # so a caller that ignored the return proceeded against a machine that never
    # settled. The server now reports it as an HTTP error; stmak_test turns that
    # into a Timeout naming the deadline and the machine's reason, because
    # urllib's "HTTP Error 500" tells a failing test nothing. Construction does
    # no I/O, so stubbing the transport keeps this test serverless.
    def boom(path, data=None, timeout=None):
        raise urllib.error.HTTPError(
            "http://x/wait-complete", 500, "Internal Server Error", {},
            io.BytesIO(b'{"error":"task not ready"}'))

    c = stmak_test.Command.__new__(stmak_test.Command)
    c._post = boom
    try:
        c.wait_complete(timeout=1)
    except stmak_test.Timeout as e:
        expect("unsynchronised" in str(e) or "settle" in str(e),
               "the failure must explain the consequence, got: %s" % e)
        expect("not ready" in str(e),
               "the failure must carry the machine's reason, got: %s" % e)
        return
    raise AssertionError("wait_complete must raise Timeout on a failed wait")


def test_wait_complete_passes_through_rcs_codes():
    for rc in (1, 3):  # RCS_DONE, RCS_ERROR
        c = stmak_test.Command.__new__(stmak_test.Command)
        c._post = lambda path, data=None, timeout=None, _rc=rc: _rc
        got = c.wait_complete(timeout=1)
        expect(got == rc,
               "wait_complete must pass RCS codes through unchanged (tests "
               "deliberately issue erroring commands); rc=%d became %r" % (rc, got))


def test_wait_complete_sizes_the_socket_to_outlive_the_wait():
    # /wait-complete blocks server-side and sends nothing until it resolves, so
    # a socket timeout at or below the requested wait makes a long wait_complete
    # raise a socket error instead of ever delivering its result.
    seen = {}

    c = stmak_test.Command.__new__(stmak_test.Command)

    def fake_post(path, data=None, timeout=None):
        seen["wait"] = data["timeout"]
        seen["socket"] = timeout
        return 1

    c._post = fake_post
    c.wait_complete(timeout=30)
    expect(seen["socket"] > seen["wait"],
           "socket timeout (%r) must outlive the server-side wait (%r)"
           % (seen["socket"], seen["wait"]))


for name, fn in sorted(globals().items()):
    if name.startswith("test_"):
        check(name, fn)

if failures:
    for f in failures:
        print("FAIL %s" % f, file=sys.stderr)
    sys.exit(1)

print("stmak_test helper self-test: all checks passed")
