#!/bin/bash
# gmi Python shim contract tests (docs/dev/GMI_PYTHON_REVIEW_FINDINGS.md) against stub
# REST/WS servers — no stmakd needed.
here=$(dirname "$0")
exec python3 "$here/test.py"
