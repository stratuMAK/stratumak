/********************************************************************
* Description: tc.h
*   Discriminate-based trajectory planning
*
*   Derived from a work by Fred Proctor & Will Shackleford
*
* Author:
* License: GPL Version 2
* System: Linux
*    
* Copyright (c) 2004 All rights reserved.
*
* Last change:
********************************************************************/
#ifndef TC_H
#define TC_H

#include "spherical_arc.h"
#include "posemath.h"
#include "emcpos.h"
#include "emcmotcfg.h"
#include "tc_types.h"
#include "tp_types.h"

double tcGetMaxTargetVel(TC_STRUCT const * const tc,
        double max_scale) RTAPI_NONBLOCKING;

double tcGetOverallMaxAccel(TC_STRUCT const * tc);
double tcGetTangentialMaxAccel(TC_STRUCT const * const tc) RTAPI_NONBLOCKING;

int tcSetKinkProperties(TC_STRUCT *prev_tc, TC_STRUCT *tc, double kink_vel, double accel_reduction) RTAPI_NONBLOCKING;
int tcInitKinkProperties(TC_STRUCT *tc);
int tcRemoveKinkProperties(TC_STRUCT *prev_tc, TC_STRUCT *tc) RTAPI_NONBLOCKING;
int tcGetEndpoint(TC_STRUCT const * const tc, EmcPose * const out) RTAPI_NONBLOCKING;
int tcGetStartpoint(TC_STRUCT const * const tc, EmcPose * const out);
int tcGetPos(TC_STRUCT const * const tc,  EmcPose * const out) RTAPI_NONBLOCKING;
int tcGetPosReal(TC_STRUCT const * const tc, int of_endpoint,  EmcPose * const out);
int tcGetEndAccelUnitVector(TC_STRUCT const * const tc, PmCartesian * const out) RTAPI_NONBLOCKING;
int tcGetStartAccelUnitVector(TC_STRUCT const * const tc, PmCartesian * const out) RTAPI_NONBLOCKING;
int tcGetEndTangentUnitVector(TC_STRUCT const * const tc, PmCartesian * const out,
        const void *log, const char *log_comp) RTAPI_NONBLOCKING;
int tcGetStartTangentUnitVector(TC_STRUCT const * const tc, PmCartesian * const out,
        const void *log, const char *log_comp) RTAPI_NONBLOCKING;

double tcGetDistanceToGo(TC_STRUCT const * const tc, int direction) RTAPI_NONBLOCKING;
double tcGetTarget(TC_STRUCT const * const tc, int direction) RTAPI_NONBLOCKING;

int tcGetIntersectionPoint(TC_STRUCT const * const prev_tc,
        TC_STRUCT const * const tc, PmCartesian * const point) RTAPI_NONBLOCKING;

int tcCanConsume(TC_STRUCT const * const tc) RTAPI_NONBLOCKING;

int tcSetTermCond(TC_STRUCT * prev_tc, TC_STRUCT * tc, int term_cond) RTAPI_NONBLOCKING;

int tcConnectBlendArc(TC_STRUCT * const prev_tc, TC_STRUCT * const tc,
        PmCartesian const * const circ_start,
        PmCartesian const * const circ_end) RTAPI_NONBLOCKING;

int tcIsBlending(TC_STRUCT * const tc) RTAPI_NONBLOCKING;


int tcFindBlendTolerance(TC_STRUCT const * const prev_tc,
        TC_STRUCT const * const tc, double * const T_blend, double * const nominal_tolerance) RTAPI_NONBLOCKING;

int pmCircleTangentVector(PmCircle const * const circle,
        double angle_in, PmCartesian * const out) RTAPI_NONBLOCKING;

int tcFlagEarlyStop(TC_STRUCT * const tc,
        TC_STRUCT * const nexttc) RTAPI_NONBLOCKING;

double pmLine9Target(PmLine9 * const line9) RTAPI_NONBLOCKING;

int pmLine9Init(PmLine9 * const line9,
        EmcPose const * const start,
        EmcPose const * const end,
        const void *log, const char *log_comp) RTAPI_NONBLOCKING;

double pmCircle9Target(PmCircle9 const * const circ9) RTAPI_NONBLOCKING;

int pmCircle9Init(PmCircle9 * const circ9,
        EmcPose const * const start,
        EmcPose const * const end,
        PmCartesian const * const center,
        PmCartesian const * const normal,
        int turn,
        const void *log, const char *log_comp) RTAPI_NONBLOCKING;

int pmRigidTapInit(PmRigidTap * const tap,
        EmcPose const * const start,
        EmcPose const * const end,
        double reversal_scale) RTAPI_NONBLOCKING;

double pmRigidTapTarget(PmRigidTap * const tap, double uu_per_rev) RTAPI_NONBLOCKING;

int tcInit(TC_STRUCT * const tc,
        int motion_type,
        int canon_motion_type,
        double cycle_time,
        unsigned char enables,
        char atspeed) RTAPI_NONBLOCKING;

int tcSetupFromTP(TC_STRUCT * const tc, TP_STRUCT const * const tp);

int tcSetupMotion(TC_STRUCT * const tc,
        double vel,
        double ini_maxvel,
        double acc) RTAPI_NONBLOCKING;

int tcSetupState(TC_STRUCT * const tc, TP_STRUCT const * const tp) RTAPI_NONBLOCKING;

int tcUpdateCircleAccRatio(TC_STRUCT * tc);

int tcFinalizeLength(TC_STRUCT * const tc) RTAPI_NONBLOCKING;

int tcClampVelocityByLength(TC_STRUCT * const tc) RTAPI_NONBLOCKING;

int tcPureRotaryCheck(TC_STRUCT const * const tc) RTAPI_NONBLOCKING;

int tcSetCircleXYZ(TC_STRUCT * const tc, PmCircle const * const circ,
        const void *log, const char *log_comp) RTAPI_NONBLOCKING;

int tcClearFlags(TC_STRUCT * const tc) RTAPI_NONBLOCKING;
#endif				/* TC_H */
