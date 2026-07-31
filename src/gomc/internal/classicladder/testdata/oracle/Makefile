# Build the LinuxCNC 2.9 ClassicLadder engine as a headless oracle binary.
#
# The gomc RT engine is a port of this code; the differential test in
# oracle_test.go feeds both the same project and script and compares the
# resulting variable state. Test-only — nothing here is installed.
#
# The reference sources live in src/hal/classicladder, which is no longer part
# of the LinuxCNC build.

CL_DIR := ../../../../../hal/classicladder

CC ?= gcc
CFLAGS ?= -O1 -g
CFLAGS += -Wall -Wno-unused-result -Wno-format-truncation
CFLAGS += -DSTANDALONE_MODE
CFLAGS += -DSEQUENTIAL_SUPPORT -DDYNAMIC_PLCSIZE -DOLD_TIMERS_MONOS_SUPPORT
CFLAGS += -DHAL_SUPPORT -DMODBUS_IO_MASTER
CFLAGS += -I$(CL_DIR) -I$(CL_DIR)/standalone_stubs -I.

SRCS = \
	$(CL_DIR)/arithm_eval.c \
	$(CL_DIR)/arrays.c \
	$(CL_DIR)/calc.c \
	$(CL_DIR)/calc_sequential.c \
	$(CL_DIR)/files.c \
	$(CL_DIR)/files_project.c \
	$(CL_DIR)/files_sequential.c \
	$(CL_DIR)/manager.c \
	$(CL_DIR)/symbols.c \
	$(CL_DIR)/vars_access.c \
	$(CL_DIR)/vars_names.c \
	oracle_main.c

TARGET = cl-oracle

all: $(TARGET)

$(TARGET): $(SRCS)
	$(CC) $(CFLAGS) -o $@ $(SRCS)

clean:
	rm -f $(TARGET)

.PHONY: all clean
