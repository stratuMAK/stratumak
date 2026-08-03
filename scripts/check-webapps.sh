#!/bin/sh
# check-webapps.sh — the gomc webapp gate: ESLint + cold vue-tsc type-check +
# vitest, over the seven non-deferred apps.
#
# Self-contained: uses npm/npx directly, needs no RIP build or ./configure, so
# it runs as a fast stand-alone CI job (and locally on a clean checkout). It is
# the CI counterpart of the local `make webapp-lint` / `make webapp-test`
# targets — keep the app lists here in sync with webapp/Submakefile.
set -eu

cd "$(dirname "$0")/../src/webapp" || exit 1

# Apps with a hand-written source tree. Type-check covers all seven; only six
# carry request-level tests (latency has none — see the Phase-6 ruling).
TYPECHECK_APPS="tooledit emccalib halshow halscope latency classicladder linuxcnctop"
TEST_APPS="tooledit emccalib halshow halscope classicladder linuxcnctop"

# The per-app src/generated/*.ts REST clients are gitignored build artifacts
# produced by modcompile during `make` (gmi/codegen/Submakefile). They are
# absent on a clean checkout, so this gate must run AFTER the build. Fail with
# a pointer rather than a wall of cryptic "Cannot find module" type errors.
if [ ! -f tooledit/src/generated/tools_client.ts ]; then
	echo "check-webapps: generated TS clients are missing." >&2
	echo "  Run 'make' first (they are built by the gmi codegen targets)." >&2
	exit 1
fi

# 1. ESLint correctness gate — the shared toolchain/config lints every app at
#    once (only each app's src/generated/** is excluded in the config).
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
