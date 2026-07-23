#!/bin/sh
# check-webapps.sh — the gomc webapp gate: ESLint + cold vue-tsc type-check +
# vitest, over the five non-deferred apps (classicladder is frozen/excluded).
#
# Self-contained: uses npm/npx directly, needs no RIP build or ./configure, so
# it runs as a fast stand-alone CI job (and locally on a clean checkout). It is
# the CI counterpart of the local `make webapp-lint` / `make webapp-test`
# targets — keep the app lists here in sync with webapp/Submakefile.
set -eu

cd "$(dirname "$0")/../src/webapp" || exit 1

# Apps with a hand-written source tree. Type-check covers all five; only four
# carry request-level tests (latency has none — see the Phase-6 ruling).
TYPECHECK_APPS="tooledit emccalib halshow halscope latency"
TEST_APPS="tooledit emccalib halshow halscope"

# 1. ESLint correctness gate — the shared toolchain/config lints every app at
#    once (classicladder and src/generated/** are excluded in the config).
echo "== webapps: eslint =="
npm install --no-audit --no-fund
npx eslint .

# 2. Per-app cold type-check (the vue-tsc gate) — install first so each app's
#    own devDependencies (vitest/happy-dom, vue-tsc) are present.
for app in $TYPECHECK_APPS; do
	echo "== $app: npm install + vue-tsc =="
	( cd "$app" && npm install --no-audit --no-fund \
		&& rm -f tsconfig.tsbuildinfo && npx vue-tsc -b --force )
done

# 3. Request-level write-path tests (deps already installed above).
for app in $TEST_APPS; do
	echo "== $app: vitest =="
	( cd "$app" && npm test )
done

echo "== webapps: all checks passed =="
