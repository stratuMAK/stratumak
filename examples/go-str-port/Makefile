# Makefile for str-port gomod example (sender + receiver)
#
# This example module registers TWO HAL components:
#   strsender  - writes a test string to a port pin
#   strreceiver - reads strings from a port pin
#
# Usage:
#   make            - verify the module compiles locally
#   make install    - copy into stmak, register, and rebuild stmakd
#   make uninstall  - remove from stmak and rebuild stmakd
#   make clean      - remove local build artifacts

MODULE_NAME ?= strport

MODCOMPILE := $(shell command -v modcompile 2>/dev/null)
ifneq ($(MODCOMPILE),)
$(eval $(shell $(MODCOMPILE) --print-make-inc))
endif

.PHONY: all compile install uninstall clean check-env

all: compile

check-env:
	@if [ -z "$(MODCOMPILE)" ]; then \
		echo "Error: modcompile not found in PATH."; \
		echo "Source rip-environment or install LinuxCNC."; \
		exit 1; \
	fi

compile: check-env go.work
	@echo "Compiling $(MODULE_NAME)..."
	CGO_ENABLED=1 \
	CGO_LDFLAGS="-Wl,-rpath,$(STMAK_LIB_DIR)" \
	GOTOOLCHAIN=local \
	$(STMAK_GO) build ./...
	@echo "$(MODULE_NAME) compiles OK"

go.work: go.mod
	@rm -f go.work go.work.sum
	GOTOOLCHAIN=local $(STMAK_GO) work init
	GOTOOLCHAIN=local $(STMAK_GO) work use .
	GOTOOLCHAIN=local $(STMAK_GO) work edit \
		-replace github.com/stratuMAK/stratumak/src/stmak=$(STMAK_DIR)

install: check-env compile
	$(MODCOMPILE) add-gomod .

uninstall: check-env
	$(MODCOMPILE) rm-gomod $(notdir $(CURDIR))

clean:
	rm -f go.work go.work.sum
