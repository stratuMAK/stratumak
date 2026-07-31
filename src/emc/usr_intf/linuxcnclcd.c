/********************************************************************
* Description: linuxcnclcd.c
*   LinuxCNC user interface for LCDproc-driven character displays.
*
*   Ported from emclcd.cc to the gomc cmod API: machine status comes
*   from the emcstat GMI API, commands go out through emccmd, INI
*   access uses env->ini, and the LCDproc protocol loop runs in a
*   pthread instead of main().
*
*   Derived from a work by Fred Proctor & Will Shackleford
*
* Author: Eric H. Johnson (original), ported to the cmod API
* License: GPL Version 2
* System: Linux
*
* Copyright (c) 2007 All rights reserved.
********************************************************************/

/*******************************************************************
*
* This module implements a limited user interface on an LCD
* display and keypad, such as the MX4 series made by Matrix Orbital.
* http://www.matrixorbital.com
*
* The layout is for a 4 line by 20 character display.  The display is
* driven through an LCDproc server (LCDd), which this module talks to
* over TCP -- so serial, USB and every other display LCDd supports
* work unchanged.
*
* Module parameters (halcmd: load linuxcnclcd server=... port=...):
*
*   server=<host>              LCDd host           (default localhost)
*   port=<n>                   LCDd port           (default 13666)
*   delay=<seconds>            pause before each protocol send
*                                                  (default 0.005)
*   autostart=1                reset ESTOP and switch the machine on
*                              as soon as the display connects
*   nc_files=<dir>             directory listed in the File/Open menu
*                              (default [DISPLAY]PROGRAM_PREFIX, else
*                              the configured nc_files directory)
*   milltask_instance=<name>   GMI provider instance (default milltask)
*
********************************************************************/

/*******************************************************************
*
* Known gaps, deliberately left open.
*
* TODO: flatten the single-item menus.  File, View and Run each hold one
*   item now, and Setup only wraps Main Display.  Promoting them to the top
*   level would change each item's parent menu id, and menuSetInt() addresses
*   feed_slider, jog_speed and limits through that parent -- whether LCDd
*   accepts "{}" as the menu id in menu_set_item needs checking against a real
*   server first.  Getting it wrong breaks the feed-override and limits
*   readback silently (LCDd answers "huh?" and the item keeps a stale value).
*
* TODO: File/Delete.  The original offered it with no handler.  Deleting a
*   program needs a confirmation step, and four lines of twenty characters is
*   not somewhere to put an irreversible action without one.
*
* TODO: laser control.  The original had a Laser ring and a Laser Override
*   slider, both dead.  There is no laser concept in emccmd to bind them to;
*   this would want a HAL pin or an M-code handler, i.e. a design decision
*   rather than a port.
*
* TODO: only joints 0 and 1 are reachable from the keypad (Left/Right and
*   Up/Down).  Z and anything past it can be homed from the menu but not
*   jogged.  A modifier key or an axis-select ring would fix it; the original
*   had the same limit.
*
* TODO: no operator messages.  An error or a message from the controller
*   never reaches the display, so a refused command looks like a dead key.
*   emcstat carries no error channel today -- this needs one.
*
* TODO: sockRecvString() cannot tell an idle connection from a closed one:
*   both come back as zero bytes.  A vanished display is therefore only
*   noticed on the next write failure, which is why the send path sets
*   sockError.  A closed connection would be cheaper to spot with poll()
*   reporting POLLHUP.
*
* TODO: sockSend()/sockRecvString() busy-spin on EAGAIN rather than waiting
*   on the socket.  Harmless in the original standalone binary, merely untidy
*   on a non-RT thread here, but it does burn a core mid-line on a slow link.
*
* TODO: with nc_files= pointing outside the controller's program search path,
*   the File/Open menu lists files that then fail to open: the menu is filled
*   from nc_files, but openProgram() sends a bare relative name that the
*   controller resolves against [DISPLAY]PROGRAM_PREFIX and the allowed
*   program directories.  Sending the resolved absolute path would fix that
*   case, at the cost of sidestepping the controller's directory policy.
*
* Set Passwords and Set Language were dropped outright rather than listed
* here: neither has anything behind it to connect to, then or now.
*
********************************************************************/

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include <ctype.h>
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

#include "config.h"		// EMC2_NCFILES_DIR
#include "sockets.h"		// TCP/IP common socket functions

#include "gomc/pkg/cmodule/gomc_env.h"
#include "gomc/generated/gmi/emccmd/emccmd_api.h"
#include "gomc/generated/gmi/emcstat/emcstat_api.h"

#define DEFAULT_SERVER		"localhost"
#define DEFAULT_PORT            13666
#define MAX_NAME_LENGTH         21
#define MAX_PRIORITY_LENGTH     12
#define MAX_STR_LENGTH          21
#define MAX_KEY_LENGTH          12
#define SOCK_DELAY              0.005
#define RECONNECT_DELAY         2.0

// Joint jogging, as in the original.  The gomc jog API takes this as
// the jjogmode flag.
#define JOGJOINT                true

#define ARRAY_SIZE(a)           (sizeof(a) / sizeof((a)[0]))

typedef enum { unmm, uncm, unInch } unitType;

typedef enum { rsIdle, rsPause, rsRun } runType;

typedef enum {
  hbIgnore = -1, hbOn, hbOff, hbOpen} heartbeatType;

typedef enum {
  blIgnore = -1, blOn, blOff, blToggle, blOpen, blBlink, blFlash} backlightType;

typedef enum {
  cuIgnore = -1, cuOn, cuOff, cuUnder, cuBlock} cursorType;

typedef enum {
  wtString, wtTitle, wtHbar, wtVbar, wtIcon, wtScroller, wtFrame, wtNum} widgetType;

typedef enum {
  kmShared, kmExclusive} keyModeType;

typedef enum {
  rmConnect, rmSuccess, rmHuh, rmListen, rmIgnore, rmKey, rmMenuEvent, rmUnknown} respMsgType;

typedef enum {
  meSelect, meUpdate, mePlus, meMinus, meEnter, meLeave, meUnknown} menuEventType;

typedef enum {
  ktLeftPress, ktLeftRelease, ktRightPress, ktRightRelease, ktUpPress, ktUpRelease,
  ktDownPress, ktDownRelease, ktMenuPress, ktMenuRelease, ktStartPress, ktStartRelease,
  ktPausePress, ktPauseRelease, ktStopPress, ktStopRelease, ktStepPress, ktStepRelease,
  ktTestPress, ktTestRelease, ktHelpPress, ktHelpRelease,
  ktNextPress, ktNextRelease, ktPrevPress, ktPrevRelease,
  ktEnterPress, ktEnterRelease, ktEStopPress, ktEStopRelease,
  ktPowerPress, ktPowerRelease, ktUnknown} keyType;

typedef enum {
  cpVersion, cpProtocol, cpLCD, cpWidth, cpHeight, cpCellWidth, cpCellHeight, cpUnknown
  } connectParmType;

typedef enum {
  jtCont, jtStep1, jtStep01, jtStep001, jtStep0001, jtCount } jogModeType;

typedef struct screenDef {
  char name[MAX_NAME_LENGTH];
  int wid, hgt;
  char priority[MAX_PRIORITY_LENGTH];
  heartbeatType heartbeat;
  backlightType backlight;
  int duration;
  int timeout;
  cursorType cursor;
  int cursor_x;
  int cursor_y;
} screenDef;

typedef struct widgetDef {
  char screenName[MAX_NAME_LENGTH];
  char widgetName[MAX_NAME_LENGTH];
  widgetType type;
  int x, y;
  char text[MAX_STR_LENGTH];
} widgetDef;

typedef struct keyDef {
  char key[MAX_KEY_LENGTH];
  keyModeType mode;
  keyType Action;
} keyDef;

typedef struct connectRec {
  char version[12];
  char protocol[12];
  int lcdWidth, lcdHeight;
  int cellWidth, cellHeight;
} connectRec;

static const char attrs[][12] = {
  "-name", "-wid", "-hgt", "-priority", "-heartbeat", "-backlight",
  "-duration", "-timeout", "-cursor", "-cursor_x", "-cursor_y"
};

static const char hbStrs[][6] = {
  "on", "off", "open"
};

static const char blStrs[][8] = {
  "on", "off", "toggle", "open", "blink", "flash"
};

static const char cuStrs[][6] = {
  "on", "off", "under", "block"
};

static const char typeStrs[][10] = {
  "string", "title", "hbar", "vbar", "icon", "scroller", "frame", "num"
};

static const char keyModes[][8] = {
  "shared", "excl"
};

static const char rspMsgs[][10] = {
  "connect", "success", "huh?", "listen", "ignore", "key", "menuevent", ""
};

static const char eventMsgs[][10] = {
  "select", "update", "plus", "minus", "enter", "leave", " "
};

static const char connectStrs[][12] = {
  "LCDproc", "protocol", "lcd", "wid", "hgt", "cellwid", "cellhgt", ""};

// The stats / copy / copying screens of the original drove themselves from
// popen("df"), popen("top"), ifconfig / /etc/network/interfaces parsing and
// system("cp ... /media/usbdisk"). None of that survives a move into the
// gomc server process (forking from a process that owns RT threads), and the
// command output formats it parsed have not existed for over a decade.  The
// screens, their widgets and their menu entries are gone with them.
// The "open" screen went with them: nothing ever brought it to the
// foreground, and its four lines were hardcoded 2007 sample filenames that no
// code updated.  Programs are opened from the File/Open menu, which LCDproc
// keeps separate from screens.
typedef enum {
  stStartup, stMain, stMain2, stUnknown} screenType;

#define SCREEN_COUNT ((int)stUnknown)

static const screenDef screens[] = {
  { "startup",     -1, -1, "foreground", hbOff, blIgnore, -1, -1, cuIgnore, -1, -1 },
  { "main",        -1, -1, "background", hbOff, blIgnore, -1, -1, cuIgnore, -1, -1 },
  { "main2",       -1, -1, "background", hbOff, blIgnore, -1, -1, cuIgnore, -1, -1 }
};

// Widget indices.  These name the rows of widgets[] below; the display code
// addresses widgets through them, so the two must stay in step (checked by
// the static assertion after the table).
enum {
  W_SU1 = 0, W_SU2, W_SU3, W_SU4,
  W_MAIN_XLBL, W_MAIN_YLBL, W_MAIN_ZLBL,
  W_MAIN_XPOS, W_MAIN_YPOS, W_MAIN_ZPOS,
  W_MAIN_STATUS, W_MAIN_PROG, W_MAIN_FEED,
  W_MAIN_JOG, W_MAIN_JOGMODE, W_MAIN_PROGRESS,
  W_MAIN2_FILELBL, W_MAIN2_PROG,
  W_MAIN2_FEEDLBL, W_MAIN2_FEED,
  W_MAIN2_SPINLBL, W_MAIN2_SPIN,
  W_MAIN2_LINELBL, W_MAIN2_LINE,
  WIDGET_COUNT
};

// The jog increment display and the running line number share one field:
// the increment only means something in manual mode, the line number only
// while a program runs.
#define W_MAIN_LINENO   W_MAIN_JOGMODE

static const widgetDef widgets[] = {
  { "startup", "su1", wtString, 1, 1, ""},                 // W_SU1
  { "startup", "su2", wtString, 1, 2, "System Starting"},  // W_SU2
  { "startup", "su3", wtString, 1, 3, "Please Wait"},      // W_SU3
  { "startup", "su4", wtString, 14, 4, "V2.1.0"},          // W_SU4

  { "main",    "m1",  wtString, 1, 2, "X "},               // W_MAIN_XLBL
  { "main",    "m2",  wtString, 1, 3, "Y "},               // W_MAIN_YLBL
  { "main",    "m3",  wtString, 11, 2, " Z"},              // W_MAIN_ZLBL
  { "main",    "m4",  wtString, 3, 2, "   0.00"},          // W_MAIN_XPOS
  { "main",    "m5",  wtString, 3, 3, "   0.00"},          // W_MAIN_YPOS
  { "main",    "m6",  wtString, 14, 2, "   0.00"},         // W_MAIN_ZPOS
  { "main",    "m7",  wtString, 15, 1, " Idle"},           // W_MAIN_STATUS
  { "main",    "m8",  wtString, 1, 1, "<no program>"},     // W_MAIN_PROG
  { "main",    "m9",  wtString, 15, 3, "  100"},           // W_MAIN_FEED
  { "main",    "m10", wtString, 1, 4, "    "},             // W_MAIN_JOG
  { "main",    "m11", wtString, 6, 4, "    "},             // W_MAIN_JOGMODE
  { "main",    "m12", wtHbar,   11, 4, "0"},               // W_MAIN_PROGRESS

  // The summary screen's Speed / Power / Pieces rows were hardcoded "100%",
  // "100/100%" and "0" that no code ever touched.  They now carry values the
  // status actually provides; there is no source for a piece counter, so that
  // row is gone rather than faked.
  { "main2",   "ma1", wtString, 1, 1, "File: "},           // W_MAIN2_FILELBL
  { "main2",   "ma5", wtString, 7, 1, "<none>"},           // W_MAIN2_PROG
  // Value fields start blank, not at a plausible-looking number: the first
  // refresh is under a second away, and until then the panel should not be
  // showing a reading nothing has produced.
  { "main2",   "ma2", wtString, 1, 2, "Feed: "},           // W_MAIN2_FEEDLBL
  { "main2",   "ma6", wtString, 8, 2, " "},                // W_MAIN2_FEED
  { "main2",   "ma3", wtString, 1, 3, "Spindle: "},        // W_MAIN2_SPINLBL
  { "main2",   "ma7", wtString, 10, 3, " "},               // W_MAIN2_SPIN
  { "main2",   "ma4", wtString, 1, 4, "Line: "},           // W_MAIN2_LINELBL
  { "main2",   "ma8", wtString, 8, 4, " "}                 // W_MAIN2_LINE
};

_Static_assert(ARRAY_SIZE(widgets) == WIDGET_COUNT,
               "widgets[] and the widget index enum disagree");
_Static_assert(ARRAY_SIZE(screens) == SCREEN_COUNT,
               "screens[] and the screen type enum disagree");

static const keyDef keys[] = {
  { "Left", kmShared, ktLeftPress },        // Left Arrow
  { "LeftUp", kmShared, ktLeftRelease },
  { "Right", kmShared, ktRightPress },      // Right Arrow
  { "RightUp", kmShared, ktRightRelease },
  { "Up", kmShared, ktUpPress },            // Up Arrow
  { "UpUp", kmShared, ktUpRelease },
  { "Down", kmShared, ktDownPress },        // Down Arrow
  { "DownUp", kmShared, ktDownRelease },
  { "Esc", kmExclusive, ktMenuPress },      // Menu
  { "EscUp", kmExclusive, ktMenuRelease },
  { "Run", kmShared, ktStartPress },        // Start
  { "RunUp", kmShared, ktStartRelease },
  { "Pause", kmShared, ktPausePress },      // Pause / Resume
  { "PauseUp", kmShared, ktPauseRelease },
  { "Stop", kmShared, ktStopPress },        // Stop - Abort
  { "StopUp", kmShared, ktStopRelease },
  { "Step", kmShared, ktStepPress },        // Step - Increment
  { "StepUp", kmShared, ktStepRelease },
  { "Test", kmShared, ktTestPress },        // Test
  { "TestUp", kmShared, ktTestRelease },
  { "Help", kmShared, ktHelpPress },        // Help
  { "HelpUp", kmShared, ktHelpRelease },
  { "Next", kmShared, ktNextPress },        // Next
  { "NextUp", kmShared, ktNextRelease },
  { "Prev", kmShared, ktPrevPress },        // Previous
  { "PrevUp", kmShared, ktPrevRelease },
  { "Enter", kmExclusive, ktEnterPress },   // Enter
  { "EnterUp", kmExclusive, ktEnterRelease },
  { "EStop", kmShared, ktEStopPress },      // EStop
  { "EStopUp", kmShared, ktEStopRelease },
  { "Power", kmShared, ktPowerPress },      // Power
  { "PowerUp", kmShared, ktPowerRelease }
};

#define KEY_COUNT ((int)ARRAY_SIZE(keys))

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

// gomc environment, captured in New().
static const cmod_env_t  *the_env;
static const gomc_log_t  *the_log;
static const gomc_ini_t  *the_ini;
static const gomc_path_t *the_path;
static char               mod_name[GOMC_RTAPI_NAME_LEN + 1] = "linuxcnclcd";

// GMI provider callbacks, fetched in Start().
static const char              *milltask_instance = "milltask";
static const emccmd_callbacks_t *emccmd;
static const emcstat_callbacks_t *emcstat_cb;
static emcstat_stat_full_t       lcd_stat;

// Configuration.
static char   server[64] = DEFAULT_SERVER;
static int    port = DEFAULT_PORT;
static double delay = SOCK_DELAY;
static int    autoStart;
static int    autoStarted;		// autostart= has fired since Start()
static const char *nc_dir;

// LCDproc connection.
static int  sockfd = -1;
static int  sockError;
static char buffer[255];
// Sized for the longest protocol line the tables below can produce, with room
// to spare -- every send goes through here.
static char sockStr[4096];
static char *tokptr;			// strtok_r state, this thread only
static connectRec lcdParms;
static const char *delims = " \n\r";

// Display state.
static int         screensInitialized = -1;
static screenType  curScreen = stStartup;
static char        menu1[20] = "";
static char        menu2[20] = "";
static char        programName[64];
static char        lastProgramFile[256];
static int         totalSteps;
static int         programStartLine;
static runType     runStatus = rsIdle;
static unitType    units = unmm;
static int         coordRelative;	// View/Coord: 0 = machine, 1 = offsets applied
static double      conversion = 1.0;
static double      jogSpeed = 25.0;
static jogModeType jogMode = jtCont;
static int         jogging;
static int         feedOverride = 100;

static atomic_int done;

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

static void strCopy(char *dst, size_t dstlen, const char *src)
{
  if (dstlen == 0) return;
  snprintf(dst, dstlen, "%s", src ? src : "");
}

static int preciseSleep(double sleepTime)
{
  struct timespec tv;
  int rval;

  tv.tv_sec = (time_t) sleepTime;
  tv.tv_nsec = (long) ((sleepTime - (double)tv.tv_sec) * 1e+9);

  while (1) {
    rval = nanosleep(&tv, &tv);
    if (rval == 0)
      return 0;
    if (errno == EINTR)
      continue;
    else return(rval);
    }
}

// Sleep that gives up as soon as Stop() has been called, so unloading the
// module never has to wait out a reconnect backoff.
static void abortableSleep(double seconds)
{
  while (seconds > 0.0 && !atomic_load_explicit(&done, memory_order_relaxed)) {
    double slice = (seconds > 0.1) ? 0.1 : seconds;
    preciseSleep(slice);
    seconds -= slice;
    }
}

// Display units per machine unit.  gomc runs the motion controller in
// millimetres end to end, so a reported position is in mm regardless of
// [TRAJ]LINEAR_UNITS -- the NML client had this table the other way round
// because it assumed an inch-based machine.
static double unitConversion(void)
{
  switch (units) {
    case uncm:   return 0.1;
    case unInch: return 1.0 / 25.4;
    default:     return 1.0;
    }
}

// basename with the extension stripped -- what the File/Open menu shows and
// what selecting an entry turns back into a program name.
static void baseNameNoExt(const char *path, char *dst, size_t dstlen)
{
  const char *slash = strrchr(path, '/');
  const char *base = slash ? slash + 1 : path;
  const char *dot = strrchr(base, '.');
  size_t n = dot ? (size_t)(dot - base) : strlen(base);

  if (dstlen == 0) return;
  if (n >= dstlen) n = dstlen - 1;
  memcpy(dst, base, n);
  dst[n] = '\0';
}

// ---------------------------------------------------------------------------
// Machine status and commands (GMI)
// ---------------------------------------------------------------------------

// Free the malloc'd strings and slices in an emcstat_stat_full_t.  get_stat()
// hands ownership to us on every call, so this runs before each refresh.
static void emcstatFree(emcstat_stat_full_t *s)
{
  free((void *)s->task.file);
  free((void *)s->task.source_file);
  free((void *)s->task.command);
  free((void *)s->task.motion_file);
  free(s->joints);
  free(s->spindle);
  free(s->axis);
  free(s->active_gcodes);
  free(s->active_mcodes);
  free(s->active_settings);
  memset(s, 0, sizeof(*s));
}

static int updateStatus(void)
{
  if (!emcstat_cb) {
    gomc_log_errorf(the_log, mod_name, "%s: no emcstat API", __func__);
    return -1;
    }
  emcstatFree(&lcd_stat);
  lcd_stat = emcstat_cb->get_stat(emcstat_cb->ctx);
  return 0;
}

static int sendTaskState(emcstat_task_state_t state)
{
  return emccmd->set_state(emccmd->ctx, (int32_t)state) < 0 ? -1 : 0;
}

static int sendTaskMode(emcstat_task_mode_t mode)
{
  return emccmd->set_mode(emccmd->ctx, (int32_t)mode) < 0 ? -1 : 0;
}

static int sendAutoCmd(emccmd_auto_cmd_t cmd, int line)
{
  return emccmd->auto_cmd(emccmd->ctx, cmd, (int32_t)line) < 0 ? -1 : 0;
}

static int sendProgramOpen(const char *program)
{
  // Save it so a run request with nothing open can reopen the last program,
  // as sendProgramRun() in the NML client did.
  strCopy(lastProgramFile, sizeof(lastProgramFile), program);
  return emccmd->program_open(emccmd->ctx, program) < 0 ? -1 : 0;
}

static int sendProgramRun(int line)
{
  updateStatus();
  // First reopen the program if none is open.
  if (!lcd_stat.task.file || lcd_stat.task.file[0] == '\0') {
    if (lastProgramFile[0] == '\0') return -1;
    sendProgramOpen(lastProgramFile);
    }
  // Save the start line, to compare against the active line later.
  programStartLine = line;
  return sendAutoCmd(EMCCMD_AUTO_RUN, line);
}

// The jog speed knob is in units per minute; the GMI jog velocity is per
// second -- the same conversion the NML client's sendJogCont/Incr did.
static int sendJogCont(int jnum, double speed)
{
  return emccmd->jog(emccmd->ctx, EMCCMD_JOG_CONTINUOUS, JOGJOINT,
                     (int32_t)jnum, speed / 60.0, 0.0) < 0 ? -1 : 0;
}

static int sendJogIncr(int jnum, double speed, double incr)
{
  return emccmd->jog(emccmd->ctx, EMCCMD_JOG_INCREMENT, JOGJOINT,
                     (int32_t)jnum, speed / 60.0, incr) < 0 ? -1 : 0;
}

static int sendJogStop(int jnum)
{
  return emccmd->jog_stop(emccmd->ctx, JOGJOINT, (int32_t)jnum) < 0 ? -1 : 0;
}

// ---------------------------------------------------------------------------
// LCDproc protocol
// ---------------------------------------------------------------------------

static int sockSendStr(int fd, const char *string)
{
  int rc;

  if (fd < 0) return -1;
  preciseSleep(delay);
  rc = sockSend(fd, string, strlen(string));
  // A failed write means the display went away; the loop reconnects.
  if (rc < 0) sockError = 1;
  return rc;
}

static const char *widgetSetStr(int widgetNo, const char *newStr, const char *oldStr)
{
  if (strcmp(newStr, oldStr) == 0) return newStr;

  snprintf(sockStr, sizeof(sockStr), "widget_set %s %s %d %d {%s}\n",
    widgets[widgetNo].screenName,
    widgets[widgetNo].widgetName,
    widgets[widgetNo].x,
    widgets[widgetNo].y,
    newStr);
  sockSendStr(sockfd, sockStr);
  return newStr;
}

static int widgetSetHbar(int widgetNo, int newVal, int oldVal)
{
  if (oldVal == newVal) return newVal;

  snprintf(sockStr, sizeof(sockStr), "widget_set %s %s %d %d %d\n",
    widgets[widgetNo].screenName,
    widgets[widgetNo].widgetName,
    widgets[widgetNo].x,
    widgets[widgetNo].y,
    newVal);
  sockSendStr(sockfd, sockStr);
  return newVal;
}

static void intScreenSet(const char *screen, const char *attr, int value)
{
  if (value != -1) {
    snprintf(sockStr, sizeof(sockStr), "screen_set %s %s %d\n", screen, attr, value);
    sockSendStr(sockfd, sockStr);
    }
}

static void strScreenSet(const char *screen, const char *attr, const char *s)
{
  if (s != NULL) {
    snprintf(sockStr, sizeof(sockStr), "screen_set %s %s %s\n", screen, attr, s);
    gomc_log_debugf(the_log, mod_name, "screen set: %s %s %s", screen, attr, s);
    sockSendStr(sockfd, sockStr);
    }
}

static void heartbeatScreenSet(const char *screen, heartbeatType hb)
{
  if (hb != hbIgnore) {
    snprintf(sockStr, sizeof(sockStr), "screen_set %s %s %s\n", screen, attrs[4], hbStrs[hb]);
    sockSendStr(sockfd, sockStr);
    }
}

static void backlightScreenSet(const char *screen, backlightType bl)
{
  if (bl != blIgnore) {
    snprintf(sockStr, sizeof(sockStr), "screen_set %s %s %s\n", screen, attrs[5], blStrs[bl]);
    sockSendStr(sockfd, sockStr);
    }
}

static void cursorScreenSet(const char *screen, cursorType cu)
{
  if (cu != cuIgnore) {
    snprintf(sockStr, sizeof(sockStr), "screen_set %s %s %s\n", screen, attrs[8], cuStrs[cu]);
    sockSendStr(sockfd, sockStr);
    }
}

static int createScreens(void)
{
  int i;

  for (i=0;i<SCREEN_COUNT;i++) {
    snprintf(sockStr, sizeof(sockStr), "screen_add %s\n", screens[i].name);
    sockSendStr(sockfd, sockStr);
    intScreenSet(screens[i].name, attrs[1], screens[i].wid);
    intScreenSet(screens[i].name, attrs[2], screens[i].hgt);
    strScreenSet(screens[i].name, attrs[3], screens[i].priority);
    heartbeatScreenSet(screens[i].name, screens[i].heartbeat);
    backlightScreenSet(screens[i].name, screens[i].backlight);
    intScreenSet(screens[i].name, attrs[6], screens[i].duration);
    intScreenSet(screens[i].name, attrs[7], screens[i].timeout);
    cursorScreenSet(screens[i].name, screens[i].cursor);
    intScreenSet(screens[i].name, attrs[9], screens[i].cursor_x);
    intScreenSet(screens[i].name, attrs[10], screens[i].cursor_y);
    }
  return 0;
}

static int deleteScreens(void)
{
  int i;

  for (i=0;i<SCREEN_COUNT;i++) {
    snprintf(sockStr, sizeof(sockStr), "screen_del %s\n", screens[i].name);
    sockSendStr(sockfd, sockStr);
    }
  return 0;
}

static int createWidgets(void)
{
  int i;

  for (i=0; i<WIDGET_COUNT; i++) {
    snprintf(sockStr, sizeof(sockStr), "widget_add %s %s %s\n", widgets[i].screenName,
      widgets[i].widgetName, typeStrs[widgets[i].type]);
    sockSendStr(sockfd, sockStr);
    switch (widgets[i].type) {
      case wtString:
      case wtHbar:
        widgetSetStr(i, widgets[i].text, "");
        break;
      default:
        break;
      }
    }
  return 0;
}

static int createKeys(void)
{
  int i;

  for (i=0; i<KEY_COUNT; i++) {
    snprintf(sockStr, sizeof(sockStr), "client_add_key %s %s\n", keyModes[keys[i].mode], keys[i].key);
    sockSendStr(sockfd, sockStr);
    }
  return 0;
}

// The menu item id and its label go into the protocol line unquoted and
// brace-quoted respectively, so a name carrying whitespace or braces would
// corrupt the command.  Such files are skipped rather than mangled.
static int menuNameIsSafe(const char *s)
{
  const unsigned char *p = (const unsigned char *)s;

  if (*p == '\0') return 0;
  for (; *p; p++)
    if (isspace(*p) || *p == '{' || *p == '}' || *p == '\\') return 0;
  return 1;
}

static int loadFiles(void)
{
  DIR *dp;
  int dfd;
  struct dirent *entry;

  if (nc_dir == NULL) return -1;
  if ((dp = opendir(nc_dir)) == NULL) {
    gomc_log_warnf(the_log, mod_name, "cannot open nc files folder %s: %s",
                   nc_dir, strerror(errno));
    return -1;
    }
  dfd = dirfd(dp);

  while ((entry = readdir(dp)) != NULL) {
    struct stat statbuf;
    char namebuf[64];
    size_t len = strlen(entry->d_name);

    if (len <= 4 || strcasecmp(entry->d_name + len - 4, ".ngc") != 0) continue;
    if (fstatat(dfd, entry->d_name, &statbuf, AT_SYMLINK_NOFOLLOW) != 0) continue;
    if (!S_ISREG(statbuf.st_mode)) continue;

    baseNameNoExt(entry->d_name, namebuf, sizeof(namebuf));
    if (!menuNameIsSafe(namebuf)) {
      gomc_log_debugf(the_log, mod_name, "skipping %s: name not usable as a menu item",
                      entry->d_name);
      continue;
      }
    snprintf(sockStr, sizeof(sockStr), "menu_add_item open %s action {%s}\n", namebuf, namebuf);
    sockSendStr(sockfd, sockStr);
    snprintf(sockStr, sizeof(sockStr), "menu_set_item open %s -menu_result quit\n", namebuf);
    sockSendStr(sockfd, sockStr);
    }
  closedir(dp);
  return 0;
}

static void menuSetInt(const char *menu, const char *item, int value)
{
  snprintf(sockStr, sizeof(sockStr), "menu_set_item %s %s -value {%d}\n", menu, item, value);
  sockSendStr(sockfd, sockStr);
}

static void menuSetMin(const char *menu, const char *item, int value)
{
  snprintf(sockStr, sizeof(sockStr), "menu_set_item %s %s -minvalue {%d}\n", menu, item, value);
  sockSendStr(sockfd, sockStr);
}

static void menuSetMax(const char *menu, const char *item, int value)
{
  snprintf(sockStr, sizeof(sockStr), "menu_set_item %s %s -maxvalue {%d}\n", menu, item, value);
  sockSendStr(sockfd, sockStr);
}

static int createMenus(void)
{
  // Only items something acts on.  See the TODO block at the top of the file
  // for the ones the original offered that are still not implementable.
  sockSendStr(sockfd, "client_set name {Main Menu}\n");
  sockSendStr(sockfd, "menu_add_item {} File menu {File}\n");
  sockSendStr(sockfd, "menu_add_item File open menu {Open}\n");
  sockSendStr(sockfd, "menu_add_item File reload action {Reload}\n");
  sockSendStr(sockfd, "menu_set_item File reload -next _quit_\n");

  sockSendStr(sockfd, "menu_add_item {} View menu {View}\n");
  sockSendStr(sockfd,
    "menu_add_item View units_ring ring {Units} -strings {mm\tcm\tinch}\n");
  sockSendStr(sockfd,
    "menu_add_item View coord_ring ring {Coord} -strings {abs\trel}\n");

  sockSendStr(sockfd, "menu_add_item {} Run menu {Run}\n");
  sockSendStr(sockfd, "menu_add_item Run feed_slider slider {Feed Override} -mintext {0} -maxtext {125} -value {50}\n");

  sockSendStr(sockfd, "menu_add_item {} Jog menu {Jog}\n");
  sockSendStr(sockfd, "menu_add_item Jog jog_speed numeric {Speed}\n");
  sockSendStr(sockfd, "menu_add_item Jog jog_mode ring {Mode} -strings {Cont\t1.0\t0.1\t0.01\t0.001}\n");

  sockSendStr(sockfd, "menu_add_item {} setup menu {Setup}\n");
  sockSendStr(sockfd, "menu_add_item setup display menu {Main Display}\n");
  sockSendStr(sockfd, "menu_add_item display posdisplay action {Positions}\n");
  sockSendStr(sockfd, "menu_add_item display sumdisplay action {Summary}\n");
  sockSendStr(sockfd, "menu_set_item display posdisplay -next _quit_\n");
  sockSendStr(sockfd, "menu_set_item display sumdisplay -next _quit_\n");

  sockSendStr(sockfd, "menu_add_item {} utility menu {Utilities}\n");
  // Homing hangs off actions, not the original's ring: a ring reports every
  // step the operator scrolls through, so passing over "X" on the way to "Z"
  // would home X.  An action only fires when it is selected.
  sockSendStr(sockfd, "menu_add_item utility home menu {Home}\n");
  sockSendStr(sockfd, "menu_add_item home home_all action {All}\n");
  sockSendStr(sockfd, "menu_add_item home home_x action {X}\n");
  sockSendStr(sockfd, "menu_add_item home home_y action {Y}\n");
  sockSendStr(sockfd, "menu_add_item home home_z action {Z}\n");
  sockSendStr(sockfd, "menu_set_item home home_all -next _quit_\n");
  sockSendStr(sockfd, "menu_set_item home home_x -next _quit_\n");
  sockSendStr(sockfd, "menu_set_item home home_y -next _quit_\n");
  sockSendStr(sockfd, "menu_set_item home home_z -next _quit_\n");
  sockSendStr(sockfd, "menu_add_item utility limits ring {Limits} -strings {On\tOff}\n");

  loadFiles();
  menuSetInt("Jog", "jog_speed", (int)jogSpeed);
  menuSetMin("Jog", "jog_speed", 0);
  menuSetMax("Jog", "jog_speed", 100);
  sockSendStr(sockfd, "menu_set_main {}\n");
  return 0;
}

static screenType setNewScreen(screenType screenNo)
{
  static const char *background = "background";
  static const char *foreground = "foreground";

  if (screenNo >= stUnknown) return curScreen;

  gomc_log_debugf(the_log, mod_name, "screen %s -> %s",
                  screens[curScreen].name, screens[screenNo].name);
  strScreenSet(screens[curScreen].name, attrs[3], background);
  strScreenSet(screens[screenNo].name, attrs[3], foreground);
  return screenNo;
}

static screenType lookupScreen(const char *s)
{
  int i;

  for (i = 0; i < SCREEN_COUNT; i++)
    if (strcmp(screens[i].name, s) == 0) return (screenType)i;
  return stUnknown;
}

static respMsgType lookupRsp(const char *s)
{
  int i;

  for (i = 0; i < (int)rmUnknown; i++)
    if (strcmp(rspMsgs[i], s) == 0) return (respMsgType)i;
  return rmUnknown;
}

static menuEventType lookupEvent(const char *s)
{
  int i;

  for (i = 0; i < (int)meUnknown; i++)
    if (strcmp(eventMsgs[i], s) == 0) return (menuEventType)i;
  return meUnknown;
}

// Returns the key's action, not its table row: the two are not the same
// ordering, and reading the row back as a keyType silently mapped EStop and
// Power onto the wrong actions.
static keyType lookupKey(const char *s)
{
  int i;

  for (i = 0; i < KEY_COUNT; i++)
    if (strcmp(keys[i].key, s) == 0) return keys[i].Action;
  return ktUnknown;
}

static connectParmType lookupConnect(const char *s)
{
  int i;

  for (i = 0; i < (int)cpUnknown; i++)
    if (strcmp(connectStrs[i], s) == 0) return (connectParmType)i;
  return cpUnknown;
}

static int openProgram(const char *s)
{
  char fname[256];

  if (snprintf(fname, sizeof(fname), "%s.ngc", s) >= (int)sizeof(fname)) return -1;
  widgetSetStr(W_MAIN_PROG, s, "");
  widgetSetStr(W_MAIN2_PROG, s, "");
  if (sendTaskMode(EMCSTAT_AUTO) != 0) return -1;
  // The name stays relative: the controller resolves it against
  // [DISPLAY]PROGRAM_PREFIX and the other allowed program directories.
  return sendProgramOpen(fname);
}

// Reopen what is already loaded.  The controller's own idea of the open file
// wins over ours: another UI may have opened it.
static int reloadProgram(void)
{
  const char *file = (lcd_stat.task.file && lcd_stat.task.file[0])
                     ? lcd_stat.task.file : lastProgramFile;

  if (file[0] == '\0') {
    gomc_log_warnf(the_log, mod_name, "reload: no program is open");
    return -1;
    }
  if (sendTaskMode(EMCSTAT_AUTO) != 0) return -1;
  return sendProgramOpen(file);
}

static int doHome(int joint)
{
  gomc_log_infof(the_log, mod_name, "homing %s",
                 joint < 0 ? "all joints" : (joint == 0 ? "X" : (joint == 1 ? "Y" : "Z")));
  return emccmd->home(emccmd->ctx, (int32_t)joint) < 0 ? -1 : 0;
}

static void displayJogMode(jogModeType mode)
{
  if (lcd_stat.task.mode != EMCSTAT_MANUAL) {
    widgetSetStr(W_MAIN_JOGMODE, "    ", "");
    return;
    }
  switch (mode) {
    case jtCont: widgetSetStr(W_MAIN_JOGMODE, "Cont", ""); break;
    case jtStep1:
      widgetSetStr(W_MAIN_JOGMODE, (units == unmm) ? "10.0" : "1.0", "");
      break;
    case jtStep01:
      widgetSetStr(W_MAIN_JOGMODE, (units == unmm) ? "1.0" : "0.1", "");
      break;
    case jtStep001:
      widgetSetStr(W_MAIN_JOGMODE, (units == unmm) ? "0.1" : "0.01", "");
      break;
    case jtStep0001:
      widgetSetStr(W_MAIN_JOGMODE, (units == unmm) ? "0.01" : "0.001", "");
      break;
    default: widgetSetStr(W_MAIN_JOGMODE, "    ", "");
    }
}

// ---------------------------------------------------------------------------
// Menu and key events
// ---------------------------------------------------------------------------

static int selectEvent(void)
{
  char *pch;

  pch = strtok_r(NULL, delims, &tokptr);
  if (pch == NULL) return -1;
  gomc_log_debugf(the_log, mod_name, "menu select: menu %s item %s", menu2, pch);

  if (strcmp(menu2, "open") == 0) {
    if (openProgram(pch) == -1) {
      gomc_log_warnf(the_log, mod_name, "failed to open program %s", pch);
      return -1;
      }
    return 0;
    }
  if (strcmp(pch, "posdisplay") == 0)
    curScreen = setNewScreen(stMain);
  else if (strcmp(pch, "sumdisplay") == 0)
    curScreen = setNewScreen(stMain2);
  else if (strcmp(pch, "reload") == 0)
    return reloadProgram();
  else if (strcmp(pch, "home_all") == 0) return doHome(-1);
  else if (strcmp(pch, "home_x") == 0)   return doHome(0);
  else if (strcmp(pch, "home_y") == 0)   return doHome(1);
  else if (strcmp(pch, "home_z") == 0)   return doHome(2);

  return 0;
}

static int updateEvent(void)
{
  char *pch, *val;
  int value;

  pch = strtok_r(NULL, delims, &tokptr);
  if (pch == NULL) return -1;
  val = strtok_r(NULL, delims, &tokptr);
  if (val == NULL) return -1;
  value = atoi(val);

  if (strcmp(pch, "units_ring") == 0) {
    switch (value) {
      case 0: units = unmm; break;
      case 1: units = uncm; break;
      case 2: units = unInch; break;
      }
    // Jogging scales by this too, and a jog can arrive before the next
    // position refresh would have recomputed it.
    conversion = unitConversion();
    }
  if (strcmp(pch, "coord_ring") == 0)
    coordRelative = (value != 0);
  if (strcmp(pch, "limits") == 0) {
    // index 0 = "On" (normal limit checking), 1 = "Off" (overridden).  A
    // negative joint resumes checking; a non-negative one overrides, which is
    // what lets the operator jog off a tripped switch.
    emccmd->override_limits(emccmd->ctx, value ? 0 : -1);
    }
  if (strcmp(pch, "jog_mode") == 0) {
    jogMode = (value >= 0 && value < (int)jtCount) ? (jogModeType)value : jtCont;
    displayJogMode(jogMode);
    }
  if (strcmp(pch, "jog_speed") == 0)
    jogSpeed = value;

  return 0;
}

static int plusMinusEvent(void)
{
  char *pch, *val;

  pch = strtok_r(NULL, delims, &tokptr);
  if (pch == NULL) return -1;
  val = strtok_r(NULL, delims, &tokptr);
  if (val == NULL) return -1;

  if (strcmp(pch, "feed_slider") == 0)
    emccmd->set_feed_override(emccmd->ctx, ((double)atoi(val)) / 100.0);

  return 0;
}

static int enterEvent(void)
{
  char *pch;

  pch = strtok_r(NULL, delims, &tokptr);
  if (pch == NULL) return -1;
  strCopy(menu1, sizeof(menu1), menu2);
  strCopy(menu2, sizeof(menu2), pch);
  gomc_log_debugf(the_log, mod_name, "menu enter %s", pch);

  return 0;
}

static int leaveEvent(void)
{
  char *pch;

  pch = strtok_r(NULL, delims, &tokptr);
  gomc_log_debugf(the_log, mod_name, "menu leave %s", pch ? pch : "");

  // Pop what enterEvent pushed.  Without this, backing out of File/Open
  // leaves menu2 at "open", and the next sibling selection -- File/Reload --
  // is taken for a program name.  The menus are at most two levels deep, so
  // the one-slot stack covers every path.
  strCopy(menu2, sizeof(menu2), menu1);
  menu1[0] = '\0';

  return 0;
}

static void doMenuEvent(const char *s)
{
  switch (lookupEvent(s)) {
    case meSelect: selectEvent(); break;
    case meUpdate: updateEvent(); break;
    case mePlus:
    case meMinus:  plusMinusEvent(); break;
    case meEnter:  enterEvent(); break;
    case meLeave:  leaveEvent(); break;
    case meUnknown: break;
    }
}

static void parseConnect(void)
{
  char *pch, *val;

  pch = strtok_r(NULL, delims, &tokptr);
  while (pch != NULL) {
    switch (lookupConnect(pch)) {
      case cpVersion:
        val = strtok_r(NULL, delims, &tokptr);
        if (val) strCopy(lcdParms.version, sizeof(lcdParms.version), val);
        break;
      case cpProtocol:
        val = strtok_r(NULL, delims, &tokptr);
        if (val) strCopy(lcdParms.protocol, sizeof(lcdParms.protocol), val);
        break;
      case cpLCD: break;
      case cpWidth:
        val = strtok_r(NULL, delims, &tokptr);
        if (val) lcdParms.lcdWidth = atoi(val);
        break;
      case cpHeight:
        val = strtok_r(NULL, delims, &tokptr);
        if (val) lcdParms.lcdHeight = atoi(val);
        break;
      case cpCellWidth:
        val = strtok_r(NULL, delims, &tokptr);
        if (val) lcdParms.cellWidth = atoi(val);
        break;
      case cpCellHeight:
        val = strtok_r(NULL, delims, &tokptr);
        if (val) lcdParms.cellHeight = atoi(val);
        break;
      case cpUnknown:
        gomc_log_debugf(the_log, mod_name, "unknown connect parameter: %s", pch);
        break;
      }
    pch = strtok_r(NULL, delims, &tokptr);
    }
}

static int jog(int jnum, double speed)
{
  double stepSize;

  if (lcd_stat.task.mode != EMCSTAT_MANUAL) return 0;
  if ((jnum < 0) || (jnum > 5)) return -1;
  // The step is expressed in the units currently on the display; the jog
  // distance the controller wants is in machine units.
  stepSize = (units == unmm) ? 10.0 : 1.0;

  switch (jogMode) {
    case jtCont:
      if (jogging == 1) {  // toggle if driver does not support key down / key up
        jogging = 0;
        return sendJogStop(jnum);
        }
      jogging = 1;
      return sendJogCont(jnum, speed);
    case jtStep1:    return sendJogIncr(jnum, speed, stepSize / conversion);
    case jtStep01:   return sendJogIncr(jnum, speed, (stepSize / 10.0) / conversion);
    case jtStep001:  return sendJogIncr(jnum, speed, (stepSize / 100.0) / conversion);
    case jtStep0001: return sendJogIncr(jnum, speed, (stepSize / 1000.0) / conversion);
    default: return -1;
    }
}

static int jogStop(int jnum)
{
  if (lcd_stat.task.mode != EMCSTAT_MANUAL) return 0;
  if ((jnum < 0) || (jnum > 5)) return -1;
  jogging = 0;
  if (jogMode != jtCont) return 0;
  return sendJogStop(jnum);
}

static int startKey(void)
{
  if (runStatus == rsIdle)
    return sendProgramRun(0);
  return -1;
}

static int pauseKey(void)
{
  if (runStatus == rsRun)
    return sendAutoCmd(EMCCMD_AUTO_PAUSE, 0);
  if (runStatus == rsPause)
    return sendAutoCmd(EMCCMD_AUTO_RESUME, 0);
  return -1;
}

static int toggleMode(void)
{
  switch (lcd_stat.task.mode) {
    case EMCSTAT_MANUAL: sendTaskMode(EMCSTAT_AUTO); break;
    case EMCSTAT_AUTO:   sendTaskMode(EMCSTAT_MANUAL); break;
    case EMCSTAT_MDI:    sendTaskMode(EMCSTAT_AUTO); break;
    }
  return 0;
}

static int stepJogMode(int delta)
{
  int temp;

  if ((curScreen != stMain) || (lcd_stat.task.mode != EMCSTAT_MANUAL)) return 0;
  temp = ((int)jogMode + delta + (int)jtCount) % (int)jtCount;
  menuSetInt("Jog", "jog_mode", temp);
  jogMode = (jogModeType)temp;
  displayJogMode(jogMode);
  return 0;
}

static int enterKey(void)
{
  switch (curScreen) {
    case stMain:
      emccmd->home(emccmd->ctx, 0);
      emccmd->home(emccmd->ctx, 1);
      emccmd->home(emccmd->ctx, 2);
      break;
    default:
      break;
    }
  return 0;
}

static int doKey(keyType k)
{
  switch (k) {
    case ktLeftPress:    jog(0, jogSpeed); break;
    case ktLeftRelease:  jogStop(0); break;
    case ktRightPress:   jog(0, -jogSpeed); break;
    case ktRightRelease: jogStop(0); break;
    case ktUpPress:      jog(1, -jogSpeed); break;
    case ktUpRelease:    jogStop(1); break;
    case ktDownPress:    jog(1, jogSpeed); break;
    case ktDownRelease:  jogStop(1); break;
    case ktStartPress:   startKey(); break;
    case ktPausePress:   pauseKey(); break;
    case ktStopPress:    emccmd->abort(emccmd->ctx); break;
    case ktStepPress:    sendAutoCmd(EMCCMD_AUTO_STEP, 0); break;
    case ktTestPress:    toggleMode(); break;
    case ktNextPress:    stepJogMode(-1); break;
    case ktPrevPress:    stepJogMode(+1); break;
    case ktEnterPress:   enterKey(); break;
    case ktEStopPress:
      sendTaskState(lcd_stat.task.state == EMCSTAT_ESTOP
                    ? EMCSTAT_ESTOP_RESET : EMCSTAT_ESTOP);
      break;
    case ktPowerPress:
      sendTaskState(lcd_stat.task.state == EMCSTAT_ON ? EMCSTAT_OFF : EMCSTAT_ON);
      break;
    default:
      break;
    }
  return 0;
}

static int parseLine(void)
{
  char *pch;

  pch = strtok_r(buffer, delims, &tokptr);
  if (pch == NULL) return 0;
  switch (lookupRsp(pch)) {
    case rmConnect: parseConnect(); break;
    case rmSuccess: break;
    case rmHuh:
      pch = strtok_r(NULL, delims, &tokptr);
      gomc_log_debugf(the_log, mod_name, "LCDd rejected a command: %s", pch ? pch : "");
      break;
    case rmListen:
      pch = strtok_r(NULL, delims, &tokptr);
      if (pch) {
        screenType s = lookupScreen(pch);
        if (s != stUnknown) curScreen = s;
        }
      break;
    case rmIgnore:
      strtok_r(NULL, delims, &tokptr);
      break;
    case rmKey:
      pch = strtok_r(NULL, delims, &tokptr);
      if (pch) doKey(lookupKey(pch));
      break;
    case rmMenuEvent:
      pch = strtok_r(NULL, delims, &tokptr);
      if (pch) doMenuEvent(pch);
      break;
    case rmUnknown:
      gomc_log_debugf(the_log, mod_name, "unknown command %s", pch);
      break;
    }
  return 0;
}

static int initScreens(void)
{
  createScreens();
  createWidgets();
  createKeys();
  createMenus();
  curScreen = setNewScreen(stMain);
  gomc_log_infof(the_log, mod_name, "screens created");
  return 0;
}

// Number of lines in the running program; the denominator of the progress bar.
// The NML client shelled out to "wc -l" for this -- not something to fork from
// the gomc server process for.
static int stepCount(const char *fileName)
{
  FILE *f;
  int lines = 0, last = '\n', c;

  if ((f = fopen(fileName, "r")) == NULL) {
    gomc_log_warnf(the_log, mod_name, "cannot read %s: %s", fileName, strerror(errno));
    return 0;
    }
  while ((c = getc(f)) != EOF) {
    if (c == '\n') lines++;
    last = c;
    }
  if (last != '\n') lines++;
  fclose(f);
  return lines;
}

// ---------------------------------------------------------------------------
// Display update
// ---------------------------------------------------------------------------

// The last values sent to the display, kept to suppress redundant protocol
// traffic.  Cleared on every disconnect: a rebuilt screen starts from the
// widget defaults, and a cache surviving the reconnect would keep the real
// values from being repainted until they next change.
static char oldStatusStr[8];
static char oldXStr[12];
static char oldYStr[12];
static char oldZStr[12];
static int  oldFeedOverride = -1;
static int  oldSpindle = -1;
static int  oldOverrideLimits = -1;
static int  oldLineNo = -1;
static int  oldMain2Line = -1;
static int  oldPct = -1;

static void resetDisplayCache(void)
{
  oldStatusStr[0] = '\0';
  oldXStr[0] = oldYStr[0] = oldZStr[0] = '\0';
  oldFeedOverride = -1;
  oldSpindle = -1;
  oldOverrideLimits = -1;
  oldLineNo = -1;
  oldMain2Line = -1;
  oldPct = -1;
  programName[0] = '\0';
  totalSteps = 0;
}

static void slowLoop(void)
{
  char fname[64];
  char buf[8];

  if (lcd_stat.task.file && lcd_stat.task.file[0] != '\0') {
    baseNameNoExt(lcd_stat.task.file, fname, sizeof(fname));
    if (strcmp(fname, programName) != 0) {
      widgetSetStr(W_MAIN_PROG, fname, programName);
      widgetSetStr(W_MAIN2_PROG, fname, programName);
      strCopy(programName, sizeof(programName), fname);
      totalSteps = stepCount(lcd_stat.task.file);
      }
    }

  feedOverride = (int)floor(lcd_stat.motion.feedrate * 100.0 + 0.5);
  if (feedOverride != oldFeedOverride) {
    menuSetInt("Run", "feed_slider", feedOverride);
    oldFeedOverride = feedOverride;
    snprintf(buf, sizeof(buf), "%5d", feedOverride);
    widgetSetStr(W_MAIN_FEED, buf, "");
    snprintf(buf, sizeof(buf), "%d%%", feedOverride);
    widgetSetStr(W_MAIN2_FEED, buf, "");
    }

  {
    int rpm = (lcd_stat.spindle_len > 0)
              ? (int)floor(fabs(lcd_stat.spindle[0].speed) + 0.5) : 0;
    if (rpm != oldSpindle) {
      snprintf(buf, sizeof(buf), "%d", rpm);
      widgetSetStr(W_MAIN2_SPIN, buf, "");
      oldSpindle = rpm;
      }
  }

  // Keep the Limits ring showing what the controller actually has: the
  // override can be cleared from elsewhere (any mode change out of manual
  // does it), and a ring stuck on "Off" would misreport a live machine.
  {
    int overridden = (lcd_stat.joints_len > 0 && lcd_stat.joints[0].override_limits);
    if (overridden != oldOverrideLimits) {
      menuSetInt("utility", "limits", overridden ? 1 : 0);
      oldOverrideLimits = overridden;
      }
  }

  switch (lcd_stat.task.interp_state) {
      case EMCSTAT_READING:
      case EMCSTAT_WAITING:
        strCopy(oldStatusStr, sizeof(oldStatusStr), widgetSetStr(W_MAIN_STATUS, "  Run", oldStatusStr));
        if (runStatus != rsRun)
          widgetSetStr(W_MAIN_JOG, "Step", "");
        runStatus = rsRun;
        break;
      case EMCSTAT_PAUSED:
        strCopy(oldStatusStr, sizeof(oldStatusStr), widgetSetStr(W_MAIN_STATUS, "Pause", oldStatusStr));
        runStatus = rsPause;
        break;
      default:
        if (lcd_stat.task.state == EMCSTAT_ESTOP) {
          strCopy(oldStatusStr, sizeof(oldStatusStr), widgetSetStr(W_MAIN_STATUS, "EStop", oldStatusStr));
          widgetSetStr(W_MAIN_JOG, "    ", "");
          }
        else if (lcd_stat.task.state != EMCSTAT_ON) {
          strCopy(oldStatusStr, sizeof(oldStatusStr), widgetSetStr(W_MAIN_STATUS, "  Off", oldStatusStr));
          widgetSetStr(W_MAIN_JOG, "    ", "");
          }
        else if (lcd_stat.task.mode == EMCSTAT_MANUAL) {
          strCopy(oldStatusStr, sizeof(oldStatusStr), widgetSetStr(W_MAIN_STATUS, "  Man", oldStatusStr));
          widgetSetStr(W_MAIN_JOG, "Jog ", "");
          }
        else {
          strCopy(oldStatusStr, sizeof(oldStatusStr), widgetSetStr(W_MAIN_STATUS, " Idle", oldStatusStr));
          widgetSetStr(W_MAIN_JOG, "    ", "");
          }
        displayJogMode(jogMode);
        runStatus = rsIdle;
        break;
    }
}

// The position the DRO shows, in machine units.  With View/Coord on "rel"
// the active offsets come off in the order the AXIS DRO uses -- tool length,
// then G5x, then the G5x rotation, then G92 (rs274/glcanon.py).  Getting the
// order wrong only shows up once a rotated coordinate system is in use.
static void displayPosition(double *ox, double *oy, double *oz)
{
  double x = lcd_stat.actual_position.x;
  double y = lcd_stat.actual_position.y;
  double z = lcd_stat.actual_position.z;

  if (coordRelative) {
    double t, rx, ry;

    x -= lcd_stat.tool_offset.x;
    y -= lcd_stat.tool_offset.y;
    z -= lcd_stat.tool_offset.z;
    x -= lcd_stat.g5x_offset.x;
    y -= lcd_stat.g5x_offset.y;
    z -= lcd_stat.g5x_offset.z;

    t = -lcd_stat.rotation_xy * M_PI / 180.0;
    rx = x * cos(t) - y * sin(t);
    ry = x * sin(t) + y * cos(t);
    x = rx;
    y = ry;

    x -= lcd_stat.g92_offset.x;
    y -= lcd_stat.g92_offset.y;
    z -= lcd_stat.g92_offset.z;
    }

  *ox = x;
  *oy = y;
  *oz = z;
}

static void updatePositions(void)
{
  char numStr[12];
  double px, py, pz;
  int lineNo;
  int stepPct;

  conversion = unitConversion();
  displayPosition(&px, &py, &pz);

  snprintf(numStr, sizeof(numStr), (units == unmm) ? "%7.2f" : "%7.3f",
           px * conversion);
  strCopy(oldXStr, sizeof(oldXStr), widgetSetStr(W_MAIN_XPOS, numStr, oldXStr));
  snprintf(numStr, sizeof(numStr), (units == unmm) ? "%7.2f" : "%7.3f",
           py * conversion);
  strCopy(oldYStr, sizeof(oldYStr), widgetSetStr(W_MAIN_YPOS, numStr, oldYStr));
  snprintf(numStr, sizeof(numStr), "%6.2f", pz * conversion);
  strCopy(oldZStr, sizeof(oldZStr), widgetSetStr(W_MAIN_ZPOS, numStr, oldZStr));

  if ((programStartLine < 0) || (lcd_stat.task.read_line < programStartLine))
    lineNo = lcd_stat.task.read_line;
  else if (lcd_stat.task.current_line > 0) {
    if ((lcd_stat.task.motion_line > 0) &&
        (lcd_stat.task.motion_line < lcd_stat.task.current_line))
      lineNo = lcd_stat.task.motion_line;
    else lineNo = lcd_stat.task.current_line;
    }
  else lineNo = 0;

  if (runStatus == rsIdle)
    oldPct = widgetSetHbar(W_MAIN_PROGRESS, 0, oldPct);
  else if (lineNo != oldLineNo) {
    snprintf(numStr, sizeof(numStr), "%d", lineNo);
    widgetSetStr(W_MAIN_LINENO, numStr, "");
    oldLineNo = lineNo;

    stepPct = (totalSteps > 0)
              ? (int)(((double)lineNo / (double)totalSteps) * 50.0) : 0;
    oldPct = widgetSetHbar(W_MAIN_PROGRESS, stepPct, oldPct);
    }

  // The summary screen carries the line number too, and unlike the main
  // screen it has a field of its own for it rather than sharing with the jog
  // increment.
  if (lineNo != oldMain2Line) {
    snprintf(numStr, sizeof(numStr), "%d", lineNo);
    widgetSetStr(W_MAIN2_LINE, numStr, "");
    oldMain2Line = lineNo;
    }
}

static void update(void)
{
  static int tics = 0;

  // Wait for screens to initialize before updating
  if (screensInitialized == -1) return;

  if ((tics % 25) == 0) {
    updateStatus();
    updatePositions();
    }
  if (tics == 105) {
    slowLoop();
    tics = 0;
    }
  tics++;
}

// ---------------------------------------------------------------------------
// Connection handling
// ---------------------------------------------------------------------------

static void lcdDisconnect(int graceful)
{
  if (sockfd < 0) return;
  if (graceful && !sockError) deleteScreens();
  sockClose(sockfd);
  sockfd = -1;
  sockError = 0;
  screensInitialized = -1;
  curScreen = stStartup;
  runStatus = rsIdle;
  resetDisplayCache();
}

static int lcdConnect(void)
{
  // Retries run on a timer, so the failure is reported once and not again
  // until something changes -- otherwise a display that is simply not there
  // fills the log at a steady drip.
  static int failureLogged;

  errno = 0;
  sockfd = sockConnect(server, (unsigned short)port);
  if (sockfd < 0) {
    if (!failureLogged) {
      // errno stays 0 when the failure was the host lookup, which reports
      // through h_errno instead.
      gomc_log_warnf(the_log, mod_name, "cannot reach LCDd at %s:%d (%s), retrying",
                     server, port, errno ? strerror(errno) : "host lookup failed");
      failureLogged = 1;
      }
    return -1;
    }
  failureLogged = 0;
  sockError = 0;
  gomc_log_infof(the_log, mod_name, "connected to LCDd at %s:%d", server, port);

  // Once per Start(), not per connect: a reconnect must not clear an E-stop
  // the operator set while the display was away.
  if (autoStart && !autoStarted) {
    sendTaskState(EMCSTAT_ESTOP_RESET);
    sendTaskState(EMCSTAT_ON);
    autoStarted = 1;
    }
  sockSendStr(sockfd, "hello\n");
  return sockError ? -1 : 0;
}

static void *lcdLoop(void *arg)
{
  (void)arg;

  while (!atomic_load_explicit(&done, memory_order_relaxed)) {
    int len;

    if (sockfd < 0) {
      if (lcdConnect() != 0) {
        lcdDisconnect(0);
        abortableSleep(RECONNECT_DELAY);
        continue;
        }
      }

    len = sockRecvString(sockfd, buffer, sizeof(buffer) - 1);
    if (len < 0) {
      gomc_log_warnf(the_log, mod_name, "LCDd read failed, reconnecting");
      lcdDisconnect(0);
      continue;
      }
    if (len == 0) {
      preciseSleep(0.01);
      update();
      }
    else {
      parseLine();
      memset(buffer, 0, sizeof(buffer));
      if (screensInitialized == -1)
        screensInitialized = initScreens();
      }
    // A write failure is the only way a vanished display shows up: the
    // reader cannot tell an idle connection from a closed one.
    if (sockError) {
      gomc_log_warnf(the_log, mod_name, "LCDd write failed, reconnecting");
      lcdDisconnect(0);
      }
    }

  lcdDisconnect(1);
  return NULL;
}

// ---------------------------------------------------------------------------
// cmod lifecycle
// ---------------------------------------------------------------------------

struct lcd_module {
    cmod_t base;
    pthread_t loop_thread;
    atomic_int thread_started;
};

static struct lcd_module *the_mod;

static int lcd_start(cmod_t *self)
{
  struct lcd_module *m = (struct lcd_module *)self->priv;

  if (!the_env->api) {
    gomc_log_errorf(the_log, mod_name, "no API registry available");
    return -1;
    }
  emccmd = emccmd_api_get(the_env->api, milltask_instance);
  if (!emccmd) {
    gomc_log_errorf(the_log, mod_name,
                    "emccmd API not registered (instance '%s', is milltask loaded?)",
                    milltask_instance);
    return -1;
    }
  emcstat_cb = emcstat_api_get(the_env->api, milltask_instance);
  if (!emcstat_cb) {
    gomc_log_errorf(the_log, mod_name,
                    "emcstat API not registered (instance '%s', is milltask loaded?)",
                    milltask_instance);
    return -1;
    }

  updateStatus();

  autoStarted = 0;
  atomic_store(&done, 0);
  atomic_store(&m->thread_started, 1);
  if (pthread_create(&m->loop_thread, NULL, lcdLoop, m) != 0) {
    gomc_log_errorf(the_log, mod_name, "failed to create loop thread");
    atomic_store(&m->thread_started, 0);
    return -1;
    }
  return 0;
}

static void lcd_stop(cmod_t *self)
{
  struct lcd_module *m = (struct lcd_module *)self->priv;

  atomic_store(&done, 1);
  if (atomic_exchange(&m->thread_started, 0))
    pthread_join(m->loop_thread, NULL);
  emcstatFree(&lcd_stat);
}

static void lcd_destroy(cmod_t *self)
{
  struct lcd_module *m = (struct lcd_module *)self->priv;

  the_mod = NULL;
  free(m);
}

// Resolve the directory the File/Open menu lists.  It comes from
// configuration, so it goes through the launcher's path resolver rather than
// straight into opendir().
static const char *resolveNcDir(const char *raw)
{
  const char *err = NULL;
  const char *resolved;

  if (raw == NULL || raw[0] == '\0') return NULL;
  resolved = the_path->resolve(the_path->ctx, raw, GOMC_PATH_DIR, &err);
  if (!resolved) {
    gomc_log_warnf(the_log, mod_name, "nc files directory %s: %s",
                   raw, err ? err : "cannot be resolved");
    return NULL;
    }
  return resolved;
}

int New(const cmod_env_t *env, const char *name,
        int argc, const char **argv, cmod_t **out)
{
  struct lcd_module *m;
  const char *nc_files = NULL;
  int i;

  if (the_mod != NULL) {
    gomc_log_errorf(env->log, name, "only one linuxcnclcd instance is supported");
    return -1;
    }

  the_env  = env;
  the_log  = env->log;
  the_ini  = env->ini;
  the_path = env->path;
  if (name && name[0]) strCopy(mod_name, sizeof(mod_name), name);

  for (i = 0; i < argc; i++) {
    if (!strncmp(argv[i], "server=", 7))
      strCopy(server, sizeof(server), argv[i] + 7);
    else if (!strncmp(argv[i], "port=", 5))
      port = atoi(argv[i] + 5);
    else if (!strncmp(argv[i], "delay=", 6))
      delay = atof(argv[i] + 6);
    else if (!strncmp(argv[i], "autostart=", 10))
      autoStart = atoi(argv[i] + 10);
    else if (!strncmp(argv[i], "nc_files=", 9))
      nc_files = argv[i] + 9;
    else if (!strncmp(argv[i], "milltask_instance=", 18))
      milltask_instance = argv[i] + 18;
    else {
      gomc_log_errorf(the_log, mod_name, "unknown parameter '%s'", argv[i]);
      return -1;
      }
    }

  if (port <= 0 || port > 65535) {
    gomc_log_errorf(the_log, mod_name, "invalid port %d", port);
    return -1;
    }
  if (delay < 0.0 || delay > 1.0) {
    gomc_log_errorf(the_log, mod_name, "invalid delay %f", delay);
    return -1;
    }

  if (nc_files == NULL) nc_files = the_ini->get(the_ini->ctx, "DISPLAY", "PROGRAM_PREFIX");
  if (nc_files == NULL) nc_files = EMC2_NCFILES_DIR;
  nc_dir = resolveNcDir(nc_files);

  // The launcher may keep the .so mapped across an unload/reload, so the file
  // statics survive: start every instance from the same display state.
  screensInitialized = -1;
  curScreen = stStartup;
  runStatus = rsIdle;
  units = unmm;
  conversion = unitConversion();
  jogMode = jtCont;
  jogging = 0;
  feedOverride = 100;
  programStartLine = 0;
  menu1[0] = menu2[0] = lastProgramFile[0] = '\0';
  resetDisplayCache();

  m = calloc(1, sizeof(*m));
  if (m == NULL) return -1;

  m->base.Start   = lcd_start;
  m->base.Stop    = lcd_stop;
  m->base.Destroy = lcd_destroy;
  m->base.priv    = m;

  the_mod = m;
  *out = &m->base;
  return 0;
}
