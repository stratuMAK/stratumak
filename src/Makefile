# Some useful targets:
#
#     default (if no make target is specified): Build the software and
#         the docs (if enabled via configure).
#
#     build-software: Build all the realtime and non-realtime software
#         libraries & executables.
#
#     docs: Build all the documentation that's enabled through configure.
#
#
#     install: Install everything (realtime software, non-realtime
#         software, and docs).
#
#     install-software: Install all architecture-specific things (realtime
#         and non-realtime libraries and executables).
#
#     install-docs: Install all enabled documentation into $(DESTDIR).
#

SHELL:=/bin/bash

default: build-software

install: install-software install-docs

# The two halves were once kernel-dependent vs kernel-independent, back when
# the realtime layer was RTAI kernel modules. Nothing here is kernel-related
# any more: install-modules places the loadable HAL/motion modules (cmods) in
# their own directory, install-common everything else. Both always run.
install-software: install-modules install-common install-menu

ifeq ($(origin KERNELRELEASE), undefined)
#MAKEFLAGS += --warn-undefined-variables
endif

EXTRAFLAGS :=
EXTRA_DEBUG ?=

ifndef DESTDIR
DESTDIR :=
endif

# see http://www.cmcrossroads.com/ask-mr-make/6535-tracing-rule-execution-in-gnu-make
# to trace make execution of make in more detail:
# make VV=1
ifeq ("$(origin VV)", "command line")
    OLD_SHELL := $(SHELL)
    SHELL = $(warning Building $@$(if $<, (from $<))$(if $?, ($? newer)))$(OLD_SHELL)
endif

# Delete the default suffix rules
.SUFFIXES:
.PHONY: default build-software userspace modules clean modclean depclean install install-software install-docs python pythonclean cscope cscopeclean

# A "trivial build" is one which should not include dependency information
# either because it should be usable before dependency information can be
# generated or when it is invalid (clean, docclean) or when running as root
# when the user must guarantee in advance that everything is built
# (setuid, install)
ifeq ($(MAKECMDGOALS),)
TRIVIAL_BUILD=no
else
ifeq ($(filter-out docclean clean setuid install tags swish,$(MAKECMDGOALS)),)
TRIVIAL_BUILD=yes
else
TRIVIAL_BUILD=no
endif
endif

# Used to enable x86 only HAL parts
ifeq ($(shell uname -m),i686)
X86ARCH = true
endif
ifeq ($(shell uname -m),x86_64)
X86ARCH = true
endif

# Beautify output
# ---------------------------------------------------------------------------
#
# A simple variant is to prefix commands with $(Q) - that's useful
# for commands that shall be hidden in non-verbose mode.
#
#	$(Q)ln $@ :<
#
# If BUILD_VERBOSE equals 0 then the above command will be hidden.
# If BUILD_VERBOSE equals 1 then the above command is displayed.

ifeq ("$(origin V)", "command line")
  BUILD_VERBOSE = $(V)
endif
ifndef BUILD_VERBOSE
  BUILD_VERBOSE = 0
endif

ifeq ($(BUILD_VERBOSE),1)
  Q =
else
  Q = @
endif

ifeq "$(findstring s,$(filter-out --%, $(MAKEFLAGS)))" ""
ECHO=@echo
VECHO=echo
else
ECHO=@true
VECHO=true
endif

ifeq ($(BASEPWD),)
BASEPWD := $(shell pwd)
export BASEPWD
include Makefile.inc
ifeq ($(origin PYTHONPATH),undefined)
PYTHONPATH:=$(EMC2_HOME)/lib/python
else
PYTHONPATH:=$(EMC2_HOME)/lib/python:$(PYTHONPATH)
endif
export PYTHONPATH
else
include $(BASEPWD)/Makefile.inc
endif
ifeq ($(RTPREFIX),)
$(error Makefile.inc must specify RTPREFIX and other variables)
endif

# ---------------------------------------------------------------------------
# Coverage instrumentation (opt-in)
# ---------------------------------------------------------------------------
#
# COVERAGE=1 instruments the C build so the runtests suite reports what it
# actually reaches. It is a make variable and not a configure option on
# purpose: a package build never passes it, so an instrumented object cannot
# reach a shipped artifact by any path. Do not give it a default here.
#
# It hangs off CC/CXX rather than CFLAGS because the C in this tree is compiled
# through half a dozen separate flag lists -- CMOD_COMP_CFLAGS, CMOD_KINS_CFLAGS,
# CMOD_HOME_CFLAGS, HOMEMOD_CMOD_CFLAGS, TPMOD_CMOD_CFLAGS and the main CFLAGS --
# and every one of them is handed to $(CC). Putting the flag in the compiler
# itself covers all of them, the shared libraries, and the link step (--coverage
# pulls in -lgcov), and it reaches the cgo half of stmakd for free, since
# stmak/Submakefile passes CC="$(CC)" through STMAK_CGOENV.
#
# -fprofile-update=atomic is required, not tidiness: HAL runs cyclic components
# on SCHED_FIFO threads while the Go side runs its own, and the default
# non-atomic counter updates would race and lose counts. It is the C
# counterpart of -covermode=atomic on the Go build.
ifeq ($(COVERAGE),1)
COVERAGE_FLAGS := --coverage -fprofile-update=atomic
CC += $(COVERAGE_FLAGS)
CXX += $(COVERAGE_FLAGS)
endif


cc-option = $(shell if $(CC) $(CFLAGS) $(1) -S -o /dev/null -xc /dev/null \
	     > /dev/null 2>&1; then echo "$(1)"; fi ;)
cxx-option = $(shell if $(CXX) $(CXXFLAGS) $(1) -S -o /dev/null -xc++ /dev/null \
            > /dev/null 2>&1; then echo "$(1)"; else echo "$(2)"; fi ;)

ifeq ($(origin KERNELRELEASE),undefined)
# When KERNELRELEASE is not defined, this is the userspace build.
# The "modules" target is the gateway to the kernel module build.
build-software: userspace modules
ifeq ($(RUN_IN_PLACE),yes)
	@if [ -f ../bin/stmakd ]; then if ! getcap ../bin/stmakd 2>/dev/null | grep -q cap_sys_nice; then $(VECHO) "You now need to run 'sudo make setuid' in order to run in place with access to hardware."; fi; fi
endif

VERSION=$(shell cat ../VERSION)
dist:
	mkdir ../linuxcnc-$(VERSION)
	(cd ../ && git archive master) | (cd ../linuxcnc-$(VERSION) && tar xf -)
	(cd ../linuxcnc-$(VERSION) && ./debian/configure)
	(cd ../linuxcnc-$(VERSION)/src && ./autogen.sh)
	(cd .. && tar zcf linuxcnc-$(VERSION).tar.gz linuxcnc-$(VERSION))
	rm -rf ../linuxcnc-$(VERSION)

# Print 'entering' all the time
MAKEFLAGS += w

# Create the variables with := so that subsequent += alterations keep it
# as a "substitute at assignment time" variable
TARGETS :=
PYTARGETS :=
GENERATED_MANPAGES :=

# EtherCAT master submodule paths (needed before Submakefile includes)
ECMASTER_DIR := hal/drivers/ethercat/master
ECMASTER_STAMPDIR := $(BASEPWD)/.ecmaster-stamps
ECMASTER_PREFIX := $(dir $(BASEPWD))

# Submakefiles from each of these directories will be included if they exist
SUBDIRS := \
    inifile posemath \
    \
    gmi/lib \
    \
    rtapi \
    \
    hal/components hal/drivers hal/drivers/mesa-hostmot2 \
    hal/user_comps/devices hal/user_comps/mb2hal \
    hal/user_comps hal/user_comps/vismach hal/user_comps/vfs11_vfd hal/utils hal \
    hal/user_comps/vfdb_vfd hal/user_comps/wj200_vfd \
    hal/user_comps/pi500_vfd \
    hal/user_comps/huanyang-vfd \
	hal/user_comps/xhc-whb04b-6 \
	hal/hal-ads-server \
	hal/drivers/ethercat \
    \
    cnc/usr_intf/axis cnc/usr_intf/touchy cnc/usr_intf/stepconf cnc/usr_intf/pncconf \
    cnc/usr_intf/gremlin cnc/usr_intf/gscreen cnc/usr_intf/pyui cnc/usr_intf/qtvcp \
    cnc/usr_intf/gmoccapy cnc/usr_intf/qtplasmac cnc/usr_intf/mdro\
    cnc/usr_intf/stmakui \
    cnc/usr_intf cnc/nml_intf cnc/task cnc/iotask cnc/kinematics cnc/tp \
    cnc/motion cnc/motion-logger cnc/ini cnc/rs274ngc cnc/sai \
    cnc \
    \
    stmak \
    gmi/codegen \
    webapp \
    \
    po \
    \
    ../docs/src ../docs/src/source-highlight \
    \
    ../tests \



ULAPISRCS := rtapi/$(RTPREFIX)_ulapi.c

# Each item in INCLUDES is transformed into a -I directive later on
# The top directory is always included
INCLUDES := .

USERSRCS :=
PROGRAMS :=

# When used like $(call TOxxx, ...) these turn a list of source files
# into the corresponding list of object files, dependency files,
# or both.  When a source file has to be compiled with special flags,
# TOOBJSDEPS is used.  Confusingly, TOOBJSDEPS includes preprocessed source
# file names, but this is what allows 'make src.i' to produce proper
# preprocessed source when src.c needs a customized compile flag.
# See Submakefile.skel for an example.
TOOBJS = $(patsubst %.cc,objects/%.o,$(patsubst %.c,objects/%.o,$(1)))
TODEPS = $(patsubst %.cc,objects/%.d,$(patsubst %.c,objects/%.d,$(1)))
TOOBJSDEPS = $(call TOOBJS,$(1)) $(call TODEPS, $(1)) $(patsubst %.cc,%.ii,$(patsubst %.c,%.i,$(1)))

SUBMAKEFILES := $(patsubst %,%/Submakefile,$(SUBDIRS))
-include $(wildcard $(SUBMAKEFILES))

# This checks that all the things listed in USERSRCS are either C files
# or C++ files
ASSERT_EMPTY = $(if $(1), $(error "Should be empty but is not: $(1)"))
$(call ASSERT_EMPTY,$(filter-out %.c %.cc, $(USERSRCS)))

$(call TOOBJS,$(PYSRCS)) : EXTRAFLAGS += -fPIC -fno-strict-aliasing
USERSRCS += $(PYSRCS)

# Find the list of object files for each type of source file
CUSERSRCS := $(filter %.c,$(USERSRCS))
CXXUSERSRCS := $(filter %.cc,$(USERSRCS))
CUSEROBJS := $(call TOOBJS,$(CUSERSRCS))
CXXUSEROBJS += $(call TOOBJS,$(CXXUSERSRCS))

ifeq ($(TRIVIAL_BUILD),no)
# Find the dependency filenames, then include them all
DEPS := $(sort $(patsubst %.o,%.d,$(CUSEROBJS) $(CXXUSEROBJS)))
READ_DEPS = $(wildcard $(DEPS))
$(shell $(VECHO) 1>&2 Reading $(words $(READ_DEPS))/$(words $(DEPS)) dependency files)
-include $(READ_DEPS)
UNREAD_DEPS = $(filter-out $(READ_DEPS), $(DEPS))
$(shell $(VECHO) 1>&2 Done reading dependencies)
endif

# Each directory in $(INCLUDES) is passed as a -I directory when compiling.
INCLUDE := $(patsubst %,-I%, $(INCLUDES)) -I$(RTDIR)/include
INCLUDE += $(PYTHON_CPPFLAGS)
INCLUDE += $(LIBTIRPC_CFLAGS)

# Compilation options.	Perhaps some of these should come from Makefile.inc? (CXXFLAGS now does)
INTEGER_OVERFLOW_FLAGS := -fwrapv
OPT := -Os $(INTEGER_OVERFLOW_FLAGS)
# -U first: Ubuntu's gcc predefines _FORTIFY_SOURCE (=3) at -O; without the
# undef a plain -D would be a redefinition. Level 3 so local Debian builds get
# the same fortify diagnostics (e.g. warn_unused_result on write()) as the
# ubuntu-24.04 CI runners.
DEBUG := $(DEBUG) -g -Wall -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=3
# $(CPPFLAGS) ahead of $(DEBUG), not after it. dpkg-buildflags puts
# -D_FORTIFY_SOURCE=2 in CPPFLAGS; trailing it meant that -D landed after the
# -U/-D=3 pair above and won, so a Debian build silently compiled at level 2
# while claiming 3 -- and gcc warned "redefined" on every translation unit
# (171 lines of it). Ahead of the pair, the -U clears it and the intended
# level 3 stands.
CFLAGS := $(INCLUDE) $(CPPFLAGS) $(OPT) $(DEBUG) $(EXTRA_DEBUG) -D_GNU_SOURCE -std=gnu11 -Werror=implicit-function-declaration $(CFLAGS)
CXXFLAGS := $(INCLUDE) $(CPPFLAGS) $(EXTRA_DEBUG) $(DEBUG) $(OPT) -Werror=overloaded-virtual $(CXXFLAGS)
CXXFLAGS += -std=gnu++17
# In Debian 11, any inclusion of <boost/python.hpp> leads to several
# diagnostics from included headers about deprecated features. LinuxCNC does
# not directly use these deprecated features, but it does use boost::python.
# Silence the warnings just when they occur in files using boost/python.hpp by
# adding SILENCE_BOOST_INTERNAL_DIAGNOSTICS_FLAGS to the object's EXTRAFLAGS
SILENCE_BOOST_INTERNAL_DIAGNOSTICS_FLAGS = -DBOOST_ALLOW_DEPRECATED_HEADERS=1 -DBOOST_BIND_GLOBAL_PLACEHOLDERS=1
ifeq ($(RUN_IN_PLACE),yes)
LDFLAGS := -L$(LIB_DIR) -Wl,-rpath,$(LIB_DIR) $(LIBTIRPC_LIBS) $(LDFLAGS)
else
# A -L as well as -rpath-link: -rpath-link only tells the linker where to find
# the transitive dependencies OF shared libraries, it is not a search path for
# -lfoo. Without it the installed build cannot resolve any tree library named
# by a bare -lfoo (hal_parport's -lrtapi_parport, the hostmot2 cmods'
# -lrtapi_core/-lrtapi_pci), while the run-in-place build could. -L leaves no
# trace in the output, so this adds no build path to a shipped binary the way
# an -rpath would.
#
# Absolute, like the run-in-place branch above: LDFLAGS reaches sub-builds that
# run from their own directory (the EtherCAT master's autotools tree), where a
# relative ../lib names nothing and libtool refuses it outright. LIB_DIR is no
# use here — it is the install location (/usr/lib) in this branch — so derive
# the build tree's lib from BASEPWD, which is this src/ directory.
LDFLAGS := -L$(abspath $(BASEPWD)/../lib) -Wl,-rpath-link,../lib $(LIBTIRPC_LIBS) $(LDFLAGS)
endif

# Rules to make .o (object) files
$(sort $(CUSEROBJS)) : objects/%.o: %.c
	$(ECHO) Compiling $<
	@mkdir -p $(dir $@)
	@rm -f $@
	$(Q)$(CC) -c $(CFLAGS) $(EXTRAFLAGS) \
		-MP -MD -MF "${@:.o=.d}" -MT "$@" \
		$< -o $@

$(sort $(CXXUSEROBJS)) : objects/%.o: %.cc
	$(ECHO) Compiling $<
	@mkdir -p $(dir $@)
	@rm -f $@
	$(Q)$(CXX) -c $(CXXFLAGS) $(EXTRAFLAGS) \
		-MP -MD -MF "${@:.o=.d}" -MT "$@" \
		$< -o $@

# Rules to make .i (preprocessed) files
$(sort $(patsubst %.c,%.i,$(CUSERSRCS))): %.i: %.c
	$(ECHO) Preprocessing $< to $@
	$(Q)$(CC) -dD $(CFLAGS) $(EXTRAFLAGS) -E $< -o $@

$(sort $(patsubst %.cc,%.ii,$(CXXUSERSRCS))): %.ii: %.cc
	$(ECHO) Preprocessing $< to $@
	$(Q)$(CXX) -dD $(CXXFLAGS) $(EXTRAFLAGS) -E $< -o $@

ifeq ($(TRIVIAL_BUILD),no)
configure: configure.ac
	AUTOGEN_TARGET=configure ./autogen.sh

config.h.in: configure.ac
	AUTOGEN_TARGET=config.h.in ./autogen.sh

config.status: configure
	if [ -f config.status ]; then ./config.status --recheck; else \
	    echo 1>&2 "*** linuxcnc is not configured.	Run './configure' with appropriate flags."; \
	    exit 1; \
	fi
endif

Makefile: config.h
config.h: config.h.in config.status
	@./config.status -q --header=$@

INFILES = \
	../scripts/linuxcnc_info ../scripts/setup_designer \
	../scripts/rtapi.conf Makefile.inc Makefile.modinc \
	../tcl/linuxcnc.tcl ../scripts/rip-environment \
	../scripts/linuxcncmkdesktop \
	../lib/python/nf.py \
	../share/desktop-directories/linuxcnc-doc.directory \
	../share/desktop-directories/linuxcnc-ref.directory \
	../share/desktop-directories/linuxcnc-cnc.directory \
	../share/menus/CNC.menu \
	../share/applications/linuxcnc.desktop \
	../share/applications/linuxcnc-latency.desktop \
	../share/applications/linuxcnc-pncconf.desktop \
	../share/applications/linuxcnc-stepconf.desktop \
	../scripts/linuxcnc_var \
	../scripts/runtests \

$(INFILES): %: %.in config.status
	@./config.status --file=$@

build-software: $(INFILES)

# For each file to be copied to ../include, its location in the source tree
# is listed here.  Note that due to $(INCLUDE), defined above, the include
# files in the source tree are the ones used when building linuxcnc.  The copy
# in ../include is used when building external components of linuxcnc.
SRCHEADERS := \
    config.h \
    cnc/linuxcnc.h \
    cnc/kinematics/cubic.h \
    cnc/kinematics/kinematics.h \
    cnc/kinematics/genhexkins.h \
    cnc/kinematics/genserkins.h \
    cnc/kinematics/pentakins.h \
    cnc/kinematics/pumakins.h \
    cnc/tp/tc.h \
    cnc/tp/tc_types.h \
    cnc/tp/tcq.h \
    cnc/tp/tp_types.h \
    cnc/tp/spherical_arc.h \
    cnc/tp/blendmath.h \
    cnc/tp/tp_debug.h \
    cnc/motion/emcmotcfg.h \
    cnc/motion/motion.h \
    cnc/motion/homing.h \
    cnc/motion/simple_tp.h \
    cnc/motion/state_tag.h \
    cnc/motion/usrmotintf.h \
    cnc/nml_intf/canon.hh \
    cnc/nml_intf/canon_position.hh \
    cnc/nml_intf/emctool.h \
    cnc/nml_intf/emcpos.h \
    cnc/nml_intf/emcpose.h \
    cnc/nml_intf/interp_return.hh \
    cnc/nml_intf/motion_types.h \
    cnc/nml_intf/debugflags.h \
    cnc/rs274ngc/interp_internal.hh \
    cnc/rs274ngc/interp_fwd.hh \
    cnc/rs274ngc/interp_base.hh \
    cnc/rs274ngc/canon_interface.hh \
    cnc/rs274ngc/interp_ext.h \
    cnc/rs274ngc/interp_inspection.hh \
    cnc/rs274ngc/interp_parameter_def.hh \
    cnc/rs274ngc/interp_parameter_io.hh \
    cnc/rs274ngc/interp_shim.h \
    cnc/rs274ngc/modal_state.hh \
    cnc/rs274ngc/rs274ngc.hh \
    cnc/rs274ngc/rs274ngc_interp.hh \
    cnc/sai/saicanon.hh \
    hal/hal.h \
    hal/hal_priv.h \
    hal/drivers/mesa-hostmot2/hostmot2-serial.h \
    inifile/inifile.h \
    inifile/inifile.hh \
    inifile/iniparse.h \
    posemath/posemath.h \
    posemath/gotypes.h \
    posemath/gomath.h \
    posemath/sincos.h \
    rtapi/rtapi.h \
    rtapi/rtapi_app.h \
    rtapi/rtapi_atomic.h \
    rtapi/rtapi_bitops.h \
    rtapi/rtapi_bool.h \
    rtapi/rtapi_byteorder.h \
    rtapi/rtapi_firmware.h \
    rtapi/rtapi_io.h \
    rtapi/rtapi_limits.h \
    rtapi/rtapi_list.h \
    rtapi/rtapi_math.h \
    rtapi/rtapi_math64.h \
    rtapi/rtapi_mutex.h \
    rtapi/rtapi_parport.h \
    rtapi/rtapi_pci.h \
    rtapi/rtapi_rt_check.h \
    rtapi/rtapi_stdint.h \
    rtapi/rtapi_ctype.h \
    rtapi/rtapi_errno.h \
    rtapi/rtapi_string.h \
    rtapi/rtapi_task.h \
    rtapi/uspace_common.h

# C files that are #included rather than compiled on their own.
#
# The stmak packages wrapping the trajectory planner and pose maths pull these
# in textually (`#include "cnc/tp/tc.c"`), so to a tree that has to be
# rebuildable they are headers in everything but name, and are installed
# alongside them. Nothing compiles them as translation units.
SRCINCLUDED_SOURCES := \
    cnc/nml_intf/emcpose.c \
    cnc/tp/blendmath.c \
    cnc/tp/spherical_arc.c \
    cnc/tp/tc.c

# ---------------------------------------------------------------------------
# Generated GMI API headers reach the flat include directory
# ---------------------------------------------------------------------------
#
# A .comp that talks to a GMI service includes that service's generated header
# by basename — mcode_handler_api.h, tp_api.h — and out-of-tree components are
# compiled with the include path modcompile reports, which is the flat
# directory and the cmod headers, nothing else.
#
# Only the two headers a shipped header happened to include were being copied
# there, so every other service's header existed on disk, was shipped in the
# -dev package under share/stratumak/stmak/generated/, and still could not be
# included: nothing put its directory on any include path. Building such a
# component failed on "mcode_handler_api.h: No such file or directory" while
# the file sat in the installed package.
#
# All of them are copied now. Each GMI module adds its own header to TARGETS,
# so the list follows codegen automatically and a new service needs no change
# here. The rules are generated per header rather than as one pattern rule
# because the directory is not derivable from the basename —
# hal_sampler/hal_sampler_stream_api.h is not hal_sampler_stream/.
GMI_GEN_API_HEADERS := $(filter stmak/generated/gmi/%_api.h,$(TARGETS))

define GMI_FLAT_API_HEADER_RULE
../include/$(notdir $(1)): $(1)
	$$(Q)cp $$< $$@
endef
$(foreach h,$(GMI_GEN_API_HEADERS),$(eval $(call GMI_FLAT_API_HEADER_RULE,$(h))))

EXTRA_FLAT_HEADERS += $(patsubst %,../include/%,$(notdir $(GMI_GEN_API_HEADERS)))

# the "headers" target installs all the header files in ../include
#
# EXTRA_FLAT_HEADERS carries the ones that must reach the flat include
# directory without being SRCHEADERS entries: the libgmi public API, and the
# generated GMI headers. They stay out of SRCHEADERS because that list also
# drives the source-relative install and the basename-collision check below —
# and a generated header already lives inside the stmak tree that check
# compares against, so listing it there would report a file as colliding with
# itself. Submakefiles append; this composes.
.PHONY: headers
HEADERS := $(patsubst %,../include/%,$(foreach h,$(SRCHEADERS),$(notdir $h))) \
	$(EXTRA_FLAT_HEADERS)
headers: $(HEADERS) check-header-collisions

# install header files as part of the build
TARGETS += headers

# ---------------------------------------------------------------------------
# No two installed headers may share a basename
# ---------------------------------------------------------------------------
#
# $(includedir)/stratumak is a flat namespace: every SRCHEADERS entry lands in
# it under its basename alone, and that directory is on the include path of
# everything compiled against LinuxCNC. A stmak package whose own header shares
# a basename with one of them does not win by being adjacent -- when cgo
# compiles it the flat copy is found first, and the package fails on types
# declared in the file lying next to it. cnc/rs274ngc/interp_shim.h and
# stmak/internal/task/interp_shim.h were exactly that, down to sharing an
# include guard, and it cost an afternoon to find because every error pointed
# at the innocent file.
#
# So the build refuses the situation rather than waiting for someone to hit
# it. Cheap: two sorts over a list of names.
STMAK_INSTALLED_HEADERS = $(shell cd stmak 2>/dev/null && find . \
	-path './internal/gmi/kinstest' -prune -o \
	-path './internal/halscope/testrt' -prune -o \
	-type f \( -name '*.h' -o -name '*.hh' \) -print 2>/dev/null)

.PHONY: check-header-collisions
# Order-only on the generated GMI headers: STMAK_INSTALLED_HEADERS finds
# whatever exists when the recipe runs, so on a fresh tree the check must not
# run before codegen has produced them (a collision involving a generated
# header would pass once and only fail on the rebuild).
check-header-collisions: | $(filter stmak/generated/%,$(TARGETS))
check-header-collisions:
	@flat=`for h in $(SRCHEADERS) $(SRCINCLUDED_SOURCES); do basename $$h; done | sort -u`; \
	dup=`for h in $(SRCHEADERS) $(SRCINCLUDED_SOURCES); do basename $$h; done | sort | uniq -d`; \
	if [ -n "$$dup" ]; then \
	    echo "*** Two SRCHEADERS entries share a basename, so only one of them can" >&2; \
	    echo "*** reach \$$(includedir)/stratumak:" >&2; \
	    for d in $$dup; do \
	        echo "***   $$d:" >&2; \
	        for h in $(SRCHEADERS) $(SRCINCLUDED_SOURCES); do \
	            case $$h in */$$d|$$d) echo "***     $$h" >&2 ;; esac; \
	        done; \
	    done; \
	    exit 1; \
	fi; \
	stmakh=`for h in $(STMAK_INSTALLED_HEADERS); do basename $$h; done | sort -u`; \
	clash=`printf '%s\n' $$flat $$stmakh | sort | uniq -d`; \
	if [ -n "$$clash" ]; then \
	    echo "*** A header installed into the flat \$$(includedir)/stratumak shares a" >&2; \
	    echo "*** basename with one inside the stmak tree. The flat copy wins when cgo" >&2; \
	    echo "*** compiles that package, whichever one it meant:" >&2; \
	    for c in $$clash; do \
	        echo "***   $$c:" >&2; \
	        for h in $(SRCHEADERS) $(SRCINCLUDED_SOURCES); do \
	            case $$h in */$$c|$$c) echo "***     $$h" >&2 ;; esac; \
	        done; \
	        for h in $(STMAK_INSTALLED_HEADERS); do \
	            case $$h in */$$c|$$c) echo "***     stmak/`echo $$h | sed 's|^\./||'`" >&2 ;; esac; \
	        done; \
	    done; \
	    echo "*** Rename one of them. See src/stmak/internal/task/task_interp_shim.h" >&2; \
	    echo "*** for the one that had to be renamed and why." >&2; \
	    exit 1; \
	fi

# Add converting of %.po files
TARGETS += $(patsubst po/%.po, ../share/locale/%/LC_MESSAGES/linuxcnc.mo, $(wildcard po/*.po))
TARGETS += $(patsubst po/gmoccapy/%.po, ../share/locale/%/LC_MESSAGES/gmoccapy.mo, $(wildcard po/gmoccapy/*.po))
TARGETS += $(patsubst po/%.po, objects/%.msg, $(wildcard po/*.po))

# ngcgui as standalone app:
TARGETS += ../bin/ngcgui
../bin/ngcgui: Makefile
	$(Q)rm -f $@
	$(Q)(echo "#!$(TCLSH)")>|$@
	$(Q)(echo "source $(EMC2_TCL_LIB_DIR)/ngcgui.tcl")>>$@
	$(Q)(echo "::ngcgui::standalone_ngcgui")>>$@
	$(Q)chmod +x $@

# pyngcgui as standalone app:
TARGETS += ../bin/pyngcgui
../bin/pyngcgui: Makefile
	$(Q)rm -f $@
	$(Q)(echo "#!$(PYTHON)")>|$@
	$(Q)(echo "import pyngcgui")>>$@
	$(Q)(echo "pyngcgui.standalone_pyngcgui()")>>$@
	$(Q)chmod +x $@

# gremlin_view as standalone app:
TARGETS += ../bin/gremlin_view
../bin/gremlin_view: Makefile
	$(Q)rm -f $@
	$(Q)(echo "#!$(PYTHON)")>|$@
	$(Q)(echo "import gremlin_view")>>$@
	$(Q)(echo "gremlin_view.standalone_gremlin_view()")>>$@
	$(Q)chmod +x $@

# halshow: now a stmakui web app (symlink created in stmakui/Submakefile)

# And make userspace depend on $(TARGETS)
userspace: $(TARGETS)

pythonclean:
	rm -f $(PYTARGETS)
	find ../lib/python -name '*.so' -exec rm {} +
	find . -name __pycache__ | xargs -r rm -r
python: $(PYTARGETS)
userspace: python
inclean:
	$(RM) $(INFILES)
headersclean:
	$(RM) $(HEADERS)
clean: docclean pythonclean cscopeclean headersclean genclean depclean
distclean: clean inclean ecmaster-distclean

depclean:
	rm -rf depends

# =========================================================================
# EtherCAT Master (submodule: src/hal/drivers/ethercat/master)
# =========================================================================

# Override ETHERCAT flags to use locally-built master from submodule
ETHERCAT_CFLAGS := -I$(ECMASTER_PREFIX)/include
ETHERCAT_LIBS := -L$(ECMASTER_PREFIX)/lib -lethercat
ifeq ($(RUN_IN_PLACE),yes)
# RIP only. libethercat lives in the build tree, so the loader needs to be told
# where. An installed package puts it in $(libdir), which ldconfig already
# covers -- baking the build tree's absolute path into a shipped ethercat.so
# would name a directory that does not exist on the target machine.
ETHERCAT_LIBS += -Wl,-rpath,$(ECMASTER_PREFIX)/lib
endif

.PHONY: ecmaster-check ecmaster-bootstrap ecmaster-configure ecmaster-build \
        ecmaster-install ecmaster-clean ecmaster-distclean

ecmaster-check:
	@if [ ! -f $(ECMASTER_DIR)/configure.ac ]; then \
		echo "ERROR: EtherCAT master submodule not initialized."; \
		echo "  Run: git submodule update --init"; \
		exit 1; \
	fi

$(ECMASTER_STAMPDIR)/.bootstrapped: | ecmaster-check
	@echo "--- EtherCAT master: bootstrap ---"
	@mkdir -p $(ECMASTER_STAMPDIR)
	cd $(ECMASTER_DIR) && ./bootstrap
	@touch $@

ecmaster-bootstrap: ecmaster-check $(ECMASTER_STAMPDIR)/.bootstrapped

$(ECMASTER_STAMPDIR)/.configured: $(ECMASTER_STAMPDIR)/.bootstrapped
	@echo "--- EtherCAT master: configure ---"
	cd $(ECMASTER_DIR) && ./configure \
		--prefix=$(ECMASTER_PREFIX) \
		--enable-uspace-master \
		--disable-kernel \
		--disable-eoe \
		--disable-tool \
		--mandir=$(ECMASTER_PREFIX)/docs/man \
		--docdir=$(ECMASTER_PREFIX)/docs/ethercat
	@touch $@

ecmaster-configure: $(ECMASTER_STAMPDIR)/.configured

$(ECMASTER_STAMPDIR)/.built: $(ECMASTER_STAMPDIR)/.configured
	@echo "--- EtherCAT master: build ---"
	$(MAKE) -C $(ECMASTER_DIR)
	@touch $@

ecmaster-build: $(ECMASTER_STAMPDIR)/.built

$(ECMASTER_STAMPDIR)/.installed: $(ECMASTER_STAMPDIR)/.built
	@echo "--- EtherCAT master: install ---"
	$(MAKE) -C $(ECMASTER_DIR) install
	@touch $@

ecmaster-install: $(ECMASTER_STAMPDIR)/.installed

ecmaster-clean:
	@if [ -f $(ECMASTER_DIR)/Makefile ]; then \
		$(MAKE) -C $(ECMASTER_DIR) clean; \
	fi
	@rm -f $(ECMASTER_STAMPDIR)/.built

ecmaster-distclean:
	@if [ -f $(ECMASTER_DIR)/Makefile ]; then \
		$(MAKE) -C $(ECMASTER_DIR) distclean; \
	fi
	@rm -rf $(ECMASTER_STAMPDIR)

# =========================================================================
genclean:
	find . -name '*.o' |xargs rm -f
	-rm -rf objects
	-rm -f $(TARGETS)
	-rm -f $(GENERATED_MANPAGES)
	-rm -f hal/components/conv_*.comp

pycheck-python-files:
	@echo 'Checking *.py files for python3 compatibility...'
	@for d in ./hal; do \
		filelist=$(shell mktemp) && \
		find $$d -name '*.py' -type f > $$filelist && \
		cat $$filelist | xargs /usr/bin/env python3 -m py_compile && \
		rm -f $$filelist; \
	done

pycheck-python-script:
	@echo 'Checking "text/x-python" and "text/x-script.python" (by MIME type) files python3 compatibility...'
	@for d in ../tests; do \
		filelist=$(shell mktemp) && \
		find $$d -type f -exec file -F ':' -i {} \; | grep -e 'text/x-python' -e 'text/x-script.python' | awk -F ':' '{print $$1}' > $$filelist && \
		cat $$filelist | xargs /usr/bin/env python3 -m py_compile && \
		rm -f $$filelist; \
	done

pycheck: pycheck-python-files pycheck-python-script

# So that nothing is built as root, this rule does not depend on the touched
# files (Note that files in depends/ might be rebuilt, and there's little that
# can be done about it)
# Capabilities required by stmakd:
#   cap_net_admin  - EtherCAT: manage network interfaces and settings
#   cap_net_raw    - EtherCAT: send/receive raw Ethernet frames
#   cap_ipc_lock   - lock shared memory (mlock) for realtime operation
#   cap_sys_resource - raise resource limits (e.g. RLIMIT_MEMLOCK, RLIMIT_RTPRIO)
#   cap_sys_nice   - set realtime scheduling priorities (SCHED_FIFO/SCHED_RR)
#   cap_sys_rawio  - direct hardware I/O port access (ioperm/iopl for parport, PCI BARs)
#   cap_perfmon    - the BPF verifier's pointer-leak gate (perfmon_capable()),
#                    which libxdp's dispatcher program has to pass
#   cap_sys_admin  - the ID-based BPF lookups (BPF_BTF_GET_FD_BY_ID,
#                    BPF_PROG_GET_FD_BY_ID) libxdp uses to inspect an
#                    already-attached dispatcher; CAP_BPF deliberately does not
#                    cover those
#   cap_dac_override - writing root-owned files as a process that is not root:
#                    /proc/irq/$n/smp_affinity (IRQ pinning),
#                    /dev/cpu_dma_latency (PM QoS floor),
#                    /sys/bus/pci/devices/*/resource0 (CCAT BAR)
#
# The long-standing "TODO: check what's actually needed" here is answered: it
# was audited in 2026-08 and every entry has a caller. Two of them are demanded
# by the kernel on behalf of a program shipped inside libxdp rather than by any
# call this tree makes, so reading the source is not enough to re-derive this --
# removing cap_perfmon or cap_sys_admin fails EtherCAT startup on a real machine
# (6.12 RT, xdp-native), each with its own error. See
# docs/dev/EXTERNAL_MODULE_INSTALL_DESIGN.md section 7.
setuid:
	setcap cap_net_admin,cap_net_raw,cap_bpf,cap_ipc_lock,cap_sys_resource,cap_sys_nice,cap_sys_rawio,cap_perfmon,cap_sys_admin,cap_dac_override=ep ../bin/stmakd

# These rules allows a header file from this directory to be installed into
# ../include.  A pair of rules like these will exist in the Submakefile
# of each file that contains headers.
$(patsubst %,../include/%,$(wildcard *.h)): ../include/%.h: %.h
	$(Q)-cp $^ $@
$(patsubst %,../include/%,$(wildcard *.hh)): ../include/%.hh: %.hh
	$(Q)-cp $^ $@

INSTALL=install -o root
DIR=$(INSTALL) -d -m 0755
FILE=$(INSTALL) -m 0644
TREE=cp -dR
CONFIGFILE=$(INSTALL) -m 0644
EXE=$(INSTALL) -m 0755
SETUID=$(INSTALL) -m 4755
GLOB=$(wildcard $(1))

XDGDESKTOP = \
    ../share/applications/linuxcnc.desktop \
    ../share/applications/linuxcnc-latency.desktop \
    ../share/applications/linuxcnc-pncconf.desktop \
    ../share/applications/linuxcnc-stepconf.desktop \

ifeq ($(RUN_IN_PLACE),yes)
define ERROR_MESSAGE
You configured run-in-place, but are trying to install.
For an installable version, run configure without --enable-run-in-place
and rebuild
endef
install install-software install-docs:
	$(error $(ERROR_MESSAGE))

MENUS = ../share/menus/CNC.menu \
    ../share/desktop-directories/cnc.directory
rip-install-menus rip-install-menu: $(MENUS) $(XDGDESKTOP)
	mkdir -p $(HOME)/.config/menus/applications-merged
	cp $< $(HOME)/.config/menus/applications-merged
else

DOCS=README.adoc INSTALL.adoc AUTHORS

# FIXME figure out how to install translated docs in subdirs
DOCS_HELP=$(call GLOB,../docs/help/*.adoc)

NC_FILES=$(filter-out %/butterfly.ngc,$(call GLOB,../nc_files/*))
TCL=$(call GLOB,../tcl/*.tcl)
TCL_BIN=$(call GLOB,../tcl/bin/*.tcl) ../tcl/bin/popimage

install-docs:
	$(DIR) $(DESTDIR)$(docsdir)
	$(FILE) $(addprefix ../docs/,$(DOCS)) $(DESTDIR)$(docsdir)
	$(FILE) $(DOCS_HELP) $(DESTDIR)$(docsdir)
	$(FILE) cnc/usr_intf/axis/etc/axis_light_background $(DESTDIR)$(docsdir)
	$(FILE) cnc/usr_intf/axis/README $(DESTDIR)$(docsdir)/README.axis

install-dirs:
	$(DIR) $(DESTDIR)$(EMC2_CMOD_DIR) \
		$(DESTDIR)$(bindir) $(DESTDIR)$(libdir) \
		$(DESTDIR)$(includedir)/stratumak \
		$(DESTDIR)$(docsdir) $(DESTDIR)$(ncfilesdir) \
		$(DESTDIR)$(tcldir)/bin \
		$(DESTDIR)$(mandir)/man1 \
		$(DESTDIR)$(mandir)/man3 \
		$(DESTDIR)$(mandir)/man9 \
		$(DESTDIR)$(tcldir)/msgs \
		$(DESTDIR)$(localedir)/de/LC_MESSAGES \
		$(DESTDIR)$(datadir)/axis/images \
		$(DESTDIR)$(datadir)/axis/tcl \
		$(DESTDIR)$(datadir)/gscreen/images \
		$(DESTDIR)$(datadir)/gscreen/skins \
		$(DESTDIR)$(datadir)/qtvcp/images \
		$(DESTDIR)$(datadir)/qtvcp/screens \
		$(DESTDIR)$(datadir)/qtvcp/panels \
		$(DESTDIR)$(datadir)/qtvcp/widgets_ui \
		$(DESTDIR)$(datadir)/gmoccapy/icons \
		$(DESTDIR)$(datadir)/glade/catalogs \
		$(DESTDIR)$(datadir)/glade/pixmaps/hicolor/22x22/actions \
		$(DESTDIR)$(datadir)/gtksourceview-4/language-specs \
		$(DESTDIR)$(datadir)/stratumak/stepconf \
		$(DESTDIR)$(datadir)/stratumak/pncconf \
		$(DESTDIR)$(datadir)/stratumak/pncconf/pncconf-help \
		$(DESTDIR)$(datadir)/stratumak/hallib \
		$(DESTDIR)$(datadir)/stratumak/webapp

install-common: install-dirs
	$(FILE) ../docs/man/man1/*.1 $(DESTDIR)$(mandir)/man1
	$(FILE) $(filter-out %/skeleton.3hal, $(wildcard ../docs/man/man3/*.3hal)) $(DESTDIR)$(mandir)/man3
	$(FILE) $(filter-out %/skeleton.3rtapi, $(wildcard ../docs/man/man3/*.3rtapi)) $(DESTDIR)$(mandir)/man3
	$(FILE) $(filter-out %/skeleton.9, $(wildcard ../docs/man/man9/*.9)) $(DESTDIR)$(mandir)/man9
	$(FILE) objects/*.msg $(DESTDIR)$(tcldir)/msgs
	# Branding images. The source directory moved out of the repo root; the
	# installed layout deliberately did not follow it, and stays flat in
	# share/stratumak.
	#
	# Two reasons. It is the packaged interface -- stepconf and pncconf carry
	# absolute /usr/share/stratumak fallbacks, and anything outside this tree
	# that refers to these files refers to them there. And INTRO_GRAPHIC, which
	# stepconf and pncconf write into every config they generate and which the
	# shipped sim configs set, names linuxcnc.gif with no directory. Nothing
	# in this tree reads that key today -- tcl/bin/popimage takes a path
	# argument and no caller resolves the setting -- so it constrains nothing
	# at the moment, but the configs carrying it are already in the field.
	#
	# In a run-in-place tree the wizards read straight out of images/; there is
	# no copy of these staged anywhere.
	#
	# This covers linuxcnc-wizard.gif too, which install-python used to install
	# separately -- install-common is the only path to install-python, so the
	# two ran together and the separate line was a second copy of this one.
	# The .xcf master beside them is deliberately not matched by this glob.
	$(FILE) ../share/stratumak/images/*.png ../share/stratumak/images/*.gif $(DESTDIR)$(datadir)/stratumak
	$(FILE) ../lib/hallib/*   $(DESTDIR)$(datadir)/stratumak/hallib

	# install all the sample configs, including subdirs (tar is required on debian systems, and common on others)
	$(DIR) $(DESTDIR)$(sampleconfsdir)
	((cd ../configs && tar --exclude CVS --exclude .cvsignore --exclude .gitignore -cf - .) | (cd $(DESTDIR)$(sampleconfsdir) && tar -xf -))

	# install the built web applications, including subdirs. stmakd serves
	# these from EMC2_WEBAPP_DIR at /app/<name>/; without them every web tool
	# (halshow, halscope, tooledit, emccalib, latency, classicladder,
	# linuxcnctop-ui) is a 404 on an installed system. The .build-stamp files
	# are make bookkeeping, not part of an app.
	((cd ../share/stmak/webapp && tar --exclude .build-stamp -cf - .) | (cd $(DESTDIR)$(datadir)/stratumak/webapp && tar -xf -))

	# stmakd is excluded: stmak-install owns it entirely (libexec copy +
	# bindir symlink); installing a flat copy here would race that symlink
	# under a parallel install.
	$(EXE) $(filter-out ../bin/test_rtapi_vsnprintf ../bin/stmakd, $(filter ../bin/%,$(TARGETS))) $(DESTDIR)$(bindir)
	# Several bin/ entries are symlinks onto one binary that picks its app
	# from argv[0] (the stmakui web-UI front-ends). install(1) dereferences, so
	# the loop above ships a full copy per link; re-create them as links.
	$(Q)for f in $(filter ../bin/%,$(TARGETS)); do \
		if [ -L "$$f" ]; then \
			n=`basename "$$f"`; t=`readlink "$$f"`; \
			rm -f "$(DESTDIR)$(bindir)/$$n"; \
			ln -s "$$t" "$(DESTDIR)$(bindir)/$$n"; \
		fi; \
	done
	$(EXE) ../scripts/linuxcnc_info $(DESTDIR)$(bindir)
	$(EXE) ../scripts/setup_designer $(DESTDIR)$(bindir)
	$(EXE) ../scripts/linuxcnc_var $(DESTDIR)$(bindir)
	$(EXE) ../scripts/latency-test $(DESTDIR)$(bindir)
	$(EXE) ../scripts/hal-histogram $(DESTDIR)$(bindir)
	$(EXE) ../scripts/xhc-hb04-accels $(DESTDIR)$(bindir)
	$(EXE) ../scripts/pyvcp_demo $(DESTDIR)$(bindir)
	$(EXE) ../scripts/gladevcp_demo $(DESTDIR)$(bindir)
	$(EXE) ../scripts/linuxcncmkdesktop $(DESTDIR)$(bindir)
	$(EXE) ../bin/update_ini $(DESTDIR)$(bindir)
	$(EXE) ../scripts/halreport $(DESTDIR)$(bindir)
	$(FILE) $(filter ../lib/%.a ../lib/%.so.0,$(TARGETS)) $(DESTDIR)$(libdir)
	cp --no-dereference $(wildcard ../lib/*.so) $(DESTDIR)$(libdir)
	# Shared libraries whose soname is not .0 are in neither of the two lines
	# above: the EtherCAT master's libethercat.so.2.x comes from the submodule,
	# not from TARGETS. Without it the package ships ../lib/libethercat.so as a
	# dangling symlink and the ethercat cmod cannot load. cp -a keeps the
	# soname symlink a symlink. The .so.0 libraries are filtered out: the
	# $(FILE) line above already installed them with install(1) semantics.
	cp -a $(filter-out %.so.0,$(wildcard ../lib/*.so.[0-9]*)) $(DESTDIR)$(libdir)
	-ldconfig $(DESTDIR)$(libdir)
	$(FILE) $(HEADERS) $(DESTDIR)$(includedir)/stratumak/
	# The same headers again, keeping their source-relative path.
	#
	# The flat copies above are what out-of-tree cmods have always used, and
	# stay. But the stmak packages include a good many of them the way the
	# source tree does -- `#include "hal/hal_priv.h"`, `#include
	# "cnc/rs274ngc/interp_parameter_io.hh"` -- which resolves against
	# -Isrc in a build tree and against nothing at all in a flat include
	# directory. That is why the installed stmak tree could not be rebuilt:
	# not a missing file, a missing shape.
	#
	# Both spellings therefore resolve under the one -I that cgoFlags already
	# passes ($(includedir)/stratumak), and no include path had to be invented.
	#
	# `|| exit 1` on the install, and it is load-bearing: without it a failing
	# iteration is not the loop's exit status, so a name that no longer exists
	# passes silently. The flat copies above reach this file through a `-cp`
	# rule that ignores errors, which is how cnc/rs274ngc/saicanon.hh stayed
	# listed for years after moving to cnc/sai/ -- installed nowhere, missed by
	# nobody until something needed it.
	$(Q)for h in $(SRCHEADERS) $(SRCINCLUDED_SOURCES); do \
		d=$$(dirname $$h); \
		if [ "$$d" != "." ]; then \
			$(DIR) $(DESTDIR)$(includedir)/stratumak/$$d || exit 1; \
		fi; \
		$(FILE) $$h $(DESTDIR)$(includedir)/stratumak/$$h || exit 1; \
	done
	$(TREE) $(NC_FILES) $(DESTDIR)$(ncfilesdir)
	$(EXE) ../nc_files/M101 $(DESTDIR)$(ncfilesdir)
	$(FILE) Makefile.modinc $(DESTDIR)$(datadir)/stratumak
	$(EXE) $(TCL) $(DESTDIR)$(tcldir)
	$(FILE) ../tcl/pkgIndex.tcl $(DESTDIR)$(tcldir)
	$(EXE) $(TCL_BIN) $(DESTDIR)$(tcldir)/bin
	$(FILE) ../share/stratumak/*.glade $(DESTDIR)$(prefix)/share/stratumak
	$(FILE) ../share/stratumak/stepconf/*.glade $(DESTDIR)$(prefix)/share/stratumak/stepconf
	$(FILE) ../share/stratumak/touchy.glade $(DESTDIR)$(prefix)/share/stratumak
	$(FILE) ../share/stratumak/gscreen.glade $(DESTDIR)$(prefix)/share/stratumak
	$(FILE) ../share/stratumak/gscreen2.glade $(DESTDIR)$(prefix)/share/stratumak
	$(FILE) ../share/gmoccapy/gmoccapy.glade $(DESTDIR)$(prefix)/share/gmoccapy
	$(FILE) ../share/stratumak/pncconf/*.glade $(DESTDIR)$(prefix)/share/stratumak/pncconf
	$(FILE) ../share/stratumak/gremlin_view.ui $(DESTDIR)$(prefix)/share/stratumak
	$(FILE) ../share/stratumak/popupkeyboard.ui $(DESTDIR)$(prefix)/share/stratumak
	$(FILE) ../src/cnc/usr_intf/pncconf/pncconf-help/*.txt $(DESTDIR)$(prefix)/share/stratumak/pncconf/pncconf-help
	$(FILE) ../src/cnc/usr_intf/pncconf/pncconf-help/*.png $(DESTDIR)$(prefix)/share/stratumak/pncconf/pncconf-help

	$(FILE) ../lib/python/gladevcp/hal_python.xml $(DESTDIR)$(datadir)/glade/catalogs/
	$(FILE) ../share/glade/pixmaps/hicolor/22x22/actions/widget*.png  $(DESTDIR)$(datadir)/glade/pixmaps/hicolor/22x22/actions/

	$(FILE) ../share/gtksourceview-4/language-specs/*.lang  $(DESTDIR)$(datadir)/gtksourceview-4/language-specs/

install-common: install-python
install-python: install-dirs
	$(DIR) $(DESTDIR)$(SITEPY) $(DESTDIR)$(SITEPY)/rs274
	$(DIR) $(DESTDIR)$(SITEPY)/touchy
	$(DIR) $(DESTDIR)$(SITEPY)/gscreen
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/designer/
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/writer
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/writer/ext
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/qtplasmac
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/qt_vismach
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/qt_ngcgui
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/qt_pdf
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/lib/gcode_utility
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/plugins
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/widgets
	$(DIR) $(DESTDIR)$(SITEPY)/qtvcp/widgets/widget_icons
	$(DIR) $(DESTDIR)$(SITEPY)/gmoccapy
	$(DIR) $(DESTDIR)$(SITEPY)/gladevcp
	$(DIR) $(DESTDIR)$(SITEPY)/stepconf
	$(DIR) $(DESTDIR)$(SITEPY)/pncconf
	$(DIR) $(DESTDIR)$(SITEPY)/pyui
	$(DIR) $(DESTDIR)$(SITEPY)/plasmac
	$(DIR) $(DESTDIR)$(SITEPY)/gmi
	$(FILE) ../lib/python/*.py ../lib/python/*.so $(DESTDIR)$(SITEPY)
	$(FILE) ../lib/python/rs274/*.py $(DESTDIR)$(SITEPY)/rs274
	$(FILE) ../lib/python/touchy/*.py $(DESTDIR)$(SITEPY)/touchy
	$(FILE) ../lib/python/gscreen/*.py $(DESTDIR)$(SITEPY)/gscreen
	$(FILE) ../lib/python/qtvcp/*.{py,ui,txt} $(DESTDIR)$(SITEPY)/qtvcp
	$(FILE) ../lib/python/qtvcp/designer/*.txt $(DESTDIR)$(SITEPY)/qtvcp/designer
	$(FILE) ../lib/python/qtvcp/widgets/*.py $(DESTDIR)$(SITEPY)/qtvcp/widgets
	$(FILE) ../lib/python/qtvcp/widgets/widget_icons/*.{png,gif} $(DESTDIR)$(SITEPY)/qtvcp/widgets/widget_icons
	$(FILE) ../lib/python/qtvcp/plugins/*.py $(DESTDIR)$(SITEPY)/qtvcp/plugins
	$(TREE) ../lib/python/qtvcp/lib/* $(DESTDIR)$(SITEPY)/qtvcp/lib
	$(FILE) ../lib/python/gmoccapy/*.py $(DESTDIR)$(SITEPY)/gmoccapy
	$(FILE) ../lib/python/gladevcp/*.{py,png,glade} $(DESTDIR)$(SITEPY)/gladevcp
	$(FILE) ../lib/python/stepconf/*.py $(DESTDIR)$(SITEPY)/stepconf
	$(FILE) ../lib/python/pncconf/*.py $(DESTDIR)$(SITEPY)/pncconf
	$(FILE) ../lib/python/pyui/*.py $(DESTDIR)$(SITEPY)/pyui
	$(FILE) ../lib/python/plasmac/*.py $(DESTDIR)$(SITEPY)/plasmac
	$(FILE) ../lib/python/gmi/*.py $(DESTDIR)$(SITEPY)/gmi
	$(EXE) ../bin/stepconf $(DESTDIR)$(bindir)
	$(EXE) ../bin/pncconf $(DESTDIR)$(bindir)
	$(EXE) ../bin/pyui $(DESTDIR)$(bindir)
	$(EXE) ../bin/pyvcp $(DESTDIR)$(bindir)
	$(EXE) ../bin/gladevcp $(DESTDIR)$(bindir)
	$(EXE) ../bin/axis $(DESTDIR)$(bindir)
	$(EXE) ../bin/axis-remote $(DESTDIR)$(bindir)
	$(EXE) ../bin/linuxcnctop $(DESTDIR)$(bindir)
	$(EXE) ../bin/mdi $(DESTDIR)$(bindir)
	$(EXE) ../bin/manualtoolchange_ui $(DESTDIR)$(bindir)
	$(EXE) ../bin/image-to-gcode $(DESTDIR)$(bindir)
	$(EXE) ../bin/touchy $(DESTDIR)$(bindir)
	$(EXE) ../bin/gscreen $(DESTDIR)$(bindir)
	$(EXE) ../bin/qtvcp $(DESTDIR)$(bindir)
	$(EXE) ../lib/python/qtvcp/designer/install_script $(DESTDIR)$(SITEPY)/qtvcp/designer/
	$(EXE) ../bin/gmoccapy $(DESTDIR)$(bindir)
	$(EXE) ../bin/teach-in $(DESTDIR)$(bindir)
	$(EXE) $(patsubst %.py,../bin/%,$(VISMACH_PY)) $(DESTDIR)$(bindir)
	$(EXE) $(patsubst %.py,../bin/%,$(QTPLASMAC_PY)) $(DESTDIR)$(bindir)
	$(FILE) ../share/axis/images/*.{png,gif,xbm,ngc} $(DESTDIR)$(datadir)/axis/images
	$(FILE) ../share/axis/tcl/*.tcl $(DESTDIR)$(datadir)/axis/tcl
	$(FILE) ../share/gscreen/images/*.gif $(DESTDIR)$(datadir)/gscreen/images
	$(TREE) ../share/gscreen/skins/* $(DESTDIR)$(datadir)/gscreen/skins
	$(TREE) ../share/qtvcp/images/* $(DESTDIR)$(datadir)/qtvcp/images
	$(TREE) ../share/qtvcp/screens/* $(DESTDIR)$(datadir)/qtvcp/screens
	$(TREE) ../share/qtvcp/panels/* $(DESTDIR)$(datadir)/qtvcp/panels
	$(TREE) ../share/qtvcp/widgets_ui/* $(DESTDIR)$(datadir)/qtvcp/widgets_ui
	$(TREE) ../share/gmoccapy/icons/* $(DESTDIR)$(datadir)/gmoccapy/icons

install-modules:
	$(DIR)	$(DESTDIR)$(bindir) \
		$(DESTDIR)$(EMC2_CMOD_DIR)
	# Install the C plugin modules, but keep test-only cmods out of the
	# package: the test_*.so handlers, mcode_coord_log.so and hm2_test.so are
	# built into ../cmod for the regression suite only, and modcompile-*.so are
	# transient artifacts left by `modcompile --install` when tests have run.
	# (mesa_pktgyro_test.so is a real, documented component and is kept.)
	$(FILE) $(filter-out ../cmod/test_%.so ../cmod/modcompile-%.so \
		../cmod/mcode_coord_log.so ../cmod/hm2_test.so, \
		$(wildcard ../cmod/*.so)) $(DESTDIR)$(EMC2_CMOD_DIR)
endif # RUN_IN_PLACE

CONF=../configs
COMMON=$(CONF)/common
CONFILES=$(addsuffix /$(1), $(filter-out $(COMMON) $(CONF),\
                                         ${shell find ${CONF} -type d -print}))
install-menu: $(XDGDESKTOP)
	$(DIR) $(DESTDIR)/usr/share/applications/
	for d in $(XDGDESKTOP); do \
	    $(FILE) $$d $(DESTDIR)/usr/share/applications/ ; \
	done

endif # userspace

# ---------------------------------------------------------------------------
# GMI home module (cmod)
# ---------------------------------------------------------------------------

HOMEMOD_CMOD_SRCS := \
    cnc/motion/homing.c

HOMEMOD_CMOD_CFLAGS := \
    -fPIC -g -Os -Wall -Wextra \
    -DUSPACE -D_GNU_SOURCE -Drealtime \
    -fno-strict-aliasing -fwrapv \
    -I. -I../include \
    -Istmak/pkg/cmodule \
    -Istmak/generated/gmi/home \
    -Istmak/generated/gmi/mot \
    -Icnc/motion

HOMEMOD_CMOD_DEPS := \
    stmak/generated/gmi/home/home_api.h \
    stmak/generated/gmi/mot/mot_api.h \
    $(wildcard stmak/pkg/cmodule/stmak_*.h) \
    $(wildcard cnc/motion/homing.h) \
    $(wildcard cnc/motion/motion.h)

../cmod/homemod.so: $(HOMEMOD_CMOD_SRCS) $(HOMEMOD_CMOD_DEPS) | headers
	$(ECHO) "Compiling cmod homemod.so"
	@mkdir -p $(dir $@)
	$(Q)$(CC) -shared $(HOMEMOD_CMOD_CFLAGS) \
	    -o $@ $(HOMEMOD_CMOD_SRCS) \
	    $(CMOD_LDFLAGS) -lm

TARGETS += ../cmod/homemod.so
modules: ../cmod/homemod.so

# ---------------------------------------------------------------------------
# LinMot homing module (cmod) — drive-internal homing for CiA 402 drives
# ---------------------------------------------------------------------------

HOMEMOD_CIA402_CMOD_SRCS := \
    cnc/motion/homemod_cia402.c

../cmod/homemod_cia402.so: $(HOMEMOD_CIA402_CMOD_SRCS) $(HOMEMOD_CMOD_DEPS) | headers
	$(ECHO) "Compiling cmod homemod_cia402.so"
	@mkdir -p $(dir $@)
	$(Q)$(CC) -shared $(HOMEMOD_CMOD_CFLAGS) \
	    -o $@ $(HOMEMOD_CIA402_CMOD_SRCS) \
	    $(CMOD_LDFLAGS) -lm

TARGETS += ../cmod/homemod_cia402.so
modules: ../cmod/homemod_cia402.so

# ---------------------------------------------------------------------------
# GMI trajectory planner module (cmod)
# ---------------------------------------------------------------------------

TPMOD_CMOD_SRCS := \
    cnc/tp/tp.c \
    cnc/tp/tc.c \
    cnc/tp/tcq.c \
    cnc/tp/spherical_arc.c \
    cnc/tp/blendmath.c \
    cnc/nml_intf/emcpose.c

TPMOD_CMOD_CFLAGS := \
    -fPIC -g -Os -Wall -Wextra \
    -DUSPACE -D_GNU_SOURCE -Drealtime \
    -fno-builtin-sin -fno-builtin-cos -fno-builtin-sincos \
    -fno-strict-aliasing -fwrapv \
    -I. -I../include \
    -Istmak/pkg/cmodule \
    -Istmak/generated/gmi/tp \
    -Istmak/generated/gmi/mot \
    -Icnc/motion \
    -Icnc/tp \
    -Iposemath

TPMOD_CMOD_DEPS := \
    stmak/generated/gmi/tp/tp_api.h \
    stmak/generated/gmi/mot/mot_api.h \
    $(wildcard stmak/pkg/cmodule/stmak_*.h) \
    $(wildcard cnc/tp/*.h) \
    $(wildcard cnc/motion/motion.h)

../cmod/tpmod.so: $(TPMOD_CMOD_SRCS) $(TPMOD_CMOD_DEPS) | headers ../lib/libposemath.so
	$(ECHO) "Compiling cmod tpmod.so"
	@mkdir -p $(dir $@)
	$(Q)$(CC) -shared $(TPMOD_CMOD_CFLAGS) \
	    -o $@ $(TPMOD_CMOD_SRCS) \
	    $(CMOD_LDFLAGS) -L../lib -lposemath -lm

TARGETS += ../cmod/tpmod.so
modules: ../cmod/tpmod.so

# ---------------------------------------------------------------------------
# Every cmod depends on the cmod environment headers
# ---------------------------------------------------------------------------
#
# cmods are built by a dozen Submakefiles, in pattern rules and explicit ones,
# from .comp sources and hand-written .c alike, and almost none of them listed
# stmak/pkg/cmodule/*.h as a prerequisite. Editing cmod_env_t therefore left
# most of ../cmod untouched -- the .comp-derived ones only rebuilt because
# ../bin/modcompile happened to be newer.
#
# That was survivable while a mismatch merely meant undefined behaviour nobody
# could detect. It is not survivable now that stmak_env.h carries an ABI stamp
# the launcher enforces: after a bump, every cmod the build skipped would be
# refused at load with no hint that a rebuild was all it needed.
#
# One line rather than a prerequisite added to each of those rules, because
# the next cmod rule someone writes would not get one.
CMOD_ENV_HEADERS := $(wildcard stmak/pkg/cmodule/stmak_*.h)
$(filter ../cmod/%.so,$(TARGETS)): $(CMOD_ENV_HEADERS)

# Phony so that it is always rebuilt when requested, not because it
# shouldn't exist as a file
.PHONY: tags
tags:
	ctags-exuberant \
		--extra=+fq \
		--exclude=depends --exclude=objects --exclude=.mod.c \
		'--langmap=make:+(Submakefile),make:+(Makefile.inc),c:+.comp' \
		-I EXPORT_SYMBOL+,RTAPI_MP_INT+,RTAPI_MP_LONG+,RTAPI_MP_STRING+ \
		-I RTAPI_MP_ARRAY_INT+,RTAPI_MP_ARRAY_LONG+,RTAPI_MP_ARRAY_STRING+ \
		-I MODULE_AUTHOR+,MODULE_DESCRIPTION+,MODULE_LICENSE+ \
		-R . ../tcl ../scripts ../share/axis/tcl
	rm -f TAGS
	find . -type f -name '*.[ch]' -printf '%P\0' |xargs -0 etags -l c --append
	find . -type f -name '*.cc' -printf '%P\0' |xargs -0 etags -l c++ --append
	find . -type f -name '*.hh' -printf '%P\0' |xargs -0 etags -l c++ --append

etags:
	etags	--extra=+fq \
		--exclude=depends --exclude=objects --exclude=.mod.c \
		'--langmap=make:+(Submakefile),make:+(Makefile.inc),c:+.comp' \
		-I EXPORT_SYMBOL+,RTAPI_MP_INT+,RTAPI_MP_LONG+,RTAPI_MP_STRING+ \
		-I RTAPI_MP_ARRAY_INT+,RTAPI_MP_ARRAY_LONG+,RTAPI_MP_ARRAY_STRING+ \
		-I MODULE_AUTHOR+,MODULE_DESCRIPTION+,MODULE_LICENSE+ \
		-R . ../tcl ../scripts ../share/axis/tcl
	find . -type f -name '*.[ch]' |xargs etags --language-force=C --append
	find . -type f -name '*.cc' |xargs etags --language-force=C++ --append
	find . -type f -name '*.hh' |xargs etags --language-force=C++ --append

.PHONY: swish
swish:
	swish-e -c .swish_config -v 0 -i $(BASEPWD) \
		$(dir $(BASEPWD))tcl \
		$(dir $(BASEPWD))share/axis/tcl \
		$(dir $(BASEPWD))lib/python \
		$(dir $(BASEPWD))scripts \
		$(dir $(BASEPWD))configs \
		$(dir $(BASEPWD))docs/src \
		$(dir $(BASEPWD))docs/man/man1

# When you depend on objects/var-ZZZ you are depending on the contents of the
# variable ZZZ, which is assumed to depend on a Makefile, a Submakefile, or
# Makefile.inc
objects/var-%: Makefile $(wildcard $(SUBMAKEFILES)) Makefile.inc
	@mkdir -p $(dir $@)
	@echo $($*) > $@.tmp
	@sh move-if-change $@.tmp $@

../lib/%.so: ../lib/%.so.0
	ln -sf $(notdir $<) $@

cscope:
	cscope -Rb

cscopeclean:
	bash -c 'for f in `find ./ -name "cscope.*out"`;do rm $$f;done'
# vim:ts=8:sts=8:sw=8:noet:
