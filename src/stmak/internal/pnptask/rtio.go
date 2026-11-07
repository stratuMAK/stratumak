// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

/*
#cgo CFLAGS: -I${SRCDIR}/../../pkg/cmodule -I${SRCDIR}/../../generated/gmi/mot -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include

#include "pnp_rt.h"

static void pnp_rt_link(void *blk, void *to_go, void *to_rt, void *in, void *out,
                        void *dz, void *bell, void *start_job) {
    pnp_rt_t *r = (pnp_rt_t *)blk;
    r->to_go     = (stmak_tribuf_t *)to_go;
    r->to_rt     = (stmak_tribuf_t *)to_rt;
    r->in        = (stmak_pinmap_t *)in;
    r->out       = (stmak_pinmap_t *)out;
    r->dz        = (pnp_dz_t *)dz;
    r->bell      = (stmak_doorbell_t *)bell;
    r->start_job = (void *volatile *)start_job;
}

static size_t pnp_rt_size(void) { return sizeof(pnp_rt_t); }

// pnp_rt_invoke runs the cycle function once without a HAL thread: the unit
// tests drive it from the control loop's tick hook.
static void pnp_rt_invoke(void *blk, long period) { pnp_rt_cycle(blk, period); }
*/
import "C"

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motctl"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/motstat"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// cycleFunct is the HAL function name suffix of the cycle function:
//
//	addf <instance>.cycle servo-thread
//
// after motion-controller (the dead-zone check reads this cycle's position) and
// before the first component that reads pnptask's outputs.
const cycleFunct = "cycle"

// The frame limits of pnp_rt.h, which config.go enforces.
const (
	maxPickers = C.PNP_RT_MAX_PICKERS
	maxTrays   = C.PNP_RT_MAX_TRAYS
	maxProcs   = C.PNP_RT_MAX_PROCS
	maxAxes    = C.PNP_RT_MAX_AXES
)

// firstFrameTimeout bounds Start's wait for the cycle function's first run.
// The servo thread runs it every millisecond; a second without it means it is
// on no thread. A variable so the tests can shorten it.
var firstFrameTimeout = time.Second

type (
	inFrame  = C.pnp_rt_in_t
	outFrame = C.pnp_rt_out_t
)

// ---------------------------------------------------------------------------
// Output handles
// ---------------------------------------------------------------------------

// outBit, outU32 and outF64 are the module's output pins as the control loop
// sees them: Set and Get work on a field of the Go-side output frame (rtIO.out),
// and the cycle function copies the whole frame to the pins at once after a
// commit. The HAL pin itself is kept for the binding and for naming.
//
// Only the control goroutine touches the frame (and the factory and the tests'
// prep, before the loop exists), so the handles need no lock.
type outBit struct {
	pin *hal.Pin[bool]
	v   *C.uint8_t
}

func (o *outBit) Set(b bool) {
	if b {
		*o.v = 1
	} else {
		*o.v = 0
	}
}

func (o *outBit) Get() bool { return *o.v != 0 }

type outU32 struct {
	pin *hal.Pin[uint32]
	v   *C.uint32_t
}

func (o *outU32) Set(u uint32) { *o.v = C.uint32_t(u) }
func (o *outU32) Get() uint32  { return uint32(*o.v) }

type outF64 struct {
	pin *hal.Pin[float64]
	v   *C.double
}

func (o *outF64) Set(f float64) { *o.v = C.double(f) }
func (o *outF64) Get() float64  { return float64(*o.v) }

func mkOutBit(b *pinBuilder, name string, v *C.uint8_t) *outBit {
	return &outBit{pin: mkPin[bool](b, name, hal.Out), v: v}
}

func mkOutU32(b *pinBuilder, name string, v *C.uint32_t) *outU32 {
	return &outU32{pin: mkPin[uint32](b, name, hal.Out), v: v}
}

func mkOutF64(b *pinBuilder, name string, v *C.double) *outF64 {
	return &outF64{pin: mkPin[float64](b, name, hal.Out), v: v}
}

// ---------------------------------------------------------------------------
// rtIO
// ---------------------------------------------------------------------------

// rtIO is the module's side of the cycle function: the two triple buffers, the
// two pin maps, and the Go-side frames.
//
// out is the working copy of the output frame. The control loop writes it
// through the output handles at any time; commit publishes it whole. in is the
// last input frame read.
type rtIO struct {
	out outFrame
	in  inFrame

	toGo   *hal.TriBuf[inFrame]
	toRT   *hal.TriBuf[outFrame]
	inMap  *hal.PinMap
	outMap *hal.PinMap
	bell   *hal.Doorbell
	blk    unsafe.Pointer

	// sent is the output frame of the last commit, sentCycle the doorbell's
	// cycle count right after it: a run that starts after the commit -- two
	// counts later -- has applied it (applyBeforeMotion).
	sent      outFrame
	sentCycle uint32

	// runRT, when set, runs the cycle function in place of the servo thread
	// (unit tests: driveRT); applyBeforeMotion then needs no thread to wait
	// for.
	runRT func()

	// dispatchCycle is the doorbell's cycle count right after the last motion
	// command returned (committedMotion). motctl returns only once motmod has
	// taken the command, so a frame of a later cycle includes it; rtStatus
	// refuses older ones.
	dispatchCycle uint32
}

// build allocates the buffers and maps, binds every pin of the set, and links
// the cycle function's arg block. dz is the dead-zone block the function runs.
func (r *rtIO) build(p *pinSet, dz *deadzoneRT) error {
	var err error
	if r.toGo, err = hal.NewTriBuf[inFrame](); err != nil {
		return err
	}
	if r.toRT, err = hal.NewTriBuf[outFrame](); err != nil {
		return err
	}
	if r.inMap, err = hal.NewPinMap(hal.In, unsafe.Sizeof(inFrame{}), 256); err != nil {
		return err
	}
	if r.outMap, err = hal.NewPinMap(hal.Out, unsafe.Sizeof(outFrame{}), 256); err != nil {
		return err
	}
	if r.bell, err = hal.NewDoorbell(); err != nil {
		return err
	}
	if err := r.bindInputs(p); err != nil {
		return fmt.Errorf("binding the input pins: %w", err)
	}
	if err := r.bindOutputs(p); err != nil {
		return fmt.Errorf("binding the output pins: %w", err)
	}
	if r.blk = hal.RTCalloc(uintptr(C.pnp_rt_size())); r.blk == nil {
		return errors.New("rtapi_calloc for the cycle function failed")
	}
	var dzBlk unsafe.Pointer
	if dz != nil {
		dzBlk = dz.blk
	}
	C.pnp_rt_link(r.blk, r.toGo.RTPtr(), r.toRT.RTPtr(), r.inMap.RTPtr(), r.outMap.RTPtr(),
		dzBlk, r.bell.RTPtr(), p.startJob.RTDataPtr())
	return nil
}

func (r *rtIO) bindInputs(p *pinSet) error {
	f := &r.in
	base := unsafe.Pointer(f)
	m := r.inMap
	errs := []error{
		hal.BindBit(m, p.estopOn, base, &f.estop_on),
		hal.BindBit(m, p.machineOn, base, &f.machine_on),
		hal.BindBit(m, p.autoEnable, base, &f.auto_enable),
		hal.BindBit(m, p.waitAbort, base, &f.wait_abort),
		hal.BindBit(m, p.park, base, &f.park),
		hal.BindBit(m, p.home, base, &f.home),
		hal.BindBit(m, p.errorReset, base, &f.error_reset),
		hal.BindBit(m, p.startJob, base, &f.start_job),
		hal.BindBit(m, p.stayNear, base, &f.stay_near),
		hal.BindBit(m, p.sceneHold, base, &f.scene_hold),
		hal.BindU32(m, p.processStep, base, &f.process_step),
		hal.BindU32(m, p.originID, base, &f.origin_id),
		hal.BindU32(m, p.destID, base, &f.dest_id),
		hal.BindU32(m, p.deadzoneSelect, base, &f.deadzone_select),
		hal.BindFloat(m, p.jogSpeed, base, &f.jog_speed),
	}
	for i := range p.jog {
		errs = append(errs,
			hal.BindBit(m, p.jog[i].pos, base, &f.jog[i].pos),
			hal.BindBit(m, p.jog[i].neg, base, &f.jog[i].neg))
	}
	for i := range p.pickers {
		pk, fp := &p.pickers[i], &f.picker[i]
		errs = append(errs,
			hal.BindBit(m, pk.manualOpen, base, &fp.manual_open),
			hal.BindBit(m, pk.manualClose, base, &fp.manual_close),
			hal.BindBit(m, pk.opened, base, &fp.opened),
			hal.BindBit(m, pk.closed, base, &fp.closed))
	}
	for i := range p.trays {
		t, ft := &p.trays[i], &f.tray[i]
		errs = append(errs,
			hal.BindU32(m, t.trayID, base, &ft.tray_id),
			hal.BindU32(m, t.step, base, &ft.step),
			hal.BindBit(m, t.setFull, base, &ft.set_full),
			hal.BindBit(m, t.setEmpty, base, &ft.set_empty),
			hal.BindFloat(m, t.zOffset, base, &ft.z_offset))
	}
	for i := range p.procs {
		s, fs := &p.procs[i], &f.proc[i]
		errs = append(errs,
			hal.BindFloat(m, s.zOffset, base, &fs.z_offset),
			hal.BindBit(m, s.busy, base, &fs.busy),
			hal.BindBit(m, s.released, base, &fs.released),
			hal.BindBit(m, s.setHasMaterial, base, &fs.set_has_material),
			hal.BindBit(m, s.setEmpty, base, &fs.set_empty))
	}
	return errors.Join(errs...)
}

// bindOutputs binds every output handle at the offset of the field it already
// points at.
func (r *rtIO) bindOutputs(p *pinSet) error {
	base := unsafe.Pointer(&r.out)
	m := r.outMap
	bit := func(o *outBit) error { return hal.BindBit(m, o.pin, base, o.v) }
	u32 := func(o *outU32) error { return hal.BindU32(m, o.pin, base, o.v) }
	f64 := func(o *outF64) error { return hal.BindFloat(m, o.pin, base, o.v) }

	errs := []error{
		bit(p.machineIsOn), bit(p.homed), bit(p.parked), bit(p.busy),
		bit(p.sceneBusy), bit(p.errorFlag),
		u32(p.cycleCount), u32(p.errorID), u32(p.errorStation),
		f64(p.planTime),
	}
	for i := range p.pickers {
		pk := &p.pickers[i]
		errs = append(errs,
			bit(pk.close), bit(pk.missing), bit(pk.holds), u32(pk.originID),
			f64(pk.posX), f64(pk.posY), f64(pk.posXMu), f64(pk.posYMu))
	}
	for i := range p.trays {
		t := &p.trays[i]
		errs = append(errs, bit(t.empty), bit(t.avail), bit(t.full), u32(t.count))
	}
	for i := range p.procs {
		s := &p.procs[i]
		errs = append(errs, bit(s.hasMaterial), bit(s.release))
	}
	return errors.Join(errs...)
}

// export registers the cycle function on comp; from the factory, before Ready.
func (r *rtIO) export(comp *hal.Component) error {
	// usesFP: the dead-zone check is double arithmetic. One thread only.
	return comp.ExportFunct(cycleFunct, hal.CFunct(unsafe.Pointer(C.pnp_rt_cycle_fp)), r.blk, true, false)
}

// commit publishes the output frame: everything written since the last commit
// reaches the pins together, in the cycle function's next run.
func (r *rtIO) commit() {
	r.out.seq++
	r.toRT.Publish(&r.out)
	r.sent = r.out
	r.sentCycle = r.bell.Cycle()
}

// applyWait bounds applyBeforeMotion's wait. Two servo cycles normally; the
// bound only matters if the function stopped running, and then the command
// goes out anyway rather than hanging the loop.
const applyWait = 20 * time.Millisecond

// applyBeforeMotion makes sure what the control loop wrote is on the pins
// before a motion command goes out: commit what changed since the last commit,
// and wait until the cycle function has started a run after the last commit
// (two cycle counts) -- for a commit from an earlier tick that is long past. Motion commands reach motmod at once,
// through motctl; without this an output written before a command -- scene-busy
// dropped before a descent -- could read its old value while the head already
// moved, to a reader outside the servo thread even within the cycle the
// command is taken in (motion-controller runs before the cycle function).
// Nothing changed, nothing to wait for: most commands of a leg go straight
// out.
func (r *rtIO) applyBeforeMotion() {
	if r.out != r.sent {
		r.commit()
	}
	deadline := time.Now().Add(applyWait)
	for r.bell.Cycle()-r.sentCycle < 2 {
		if r.runRT != nil {
			r.runRT()
			continue
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// clearStartJob asks the cycle function to clear the start-job pin, in the run
// that applies this commit (pnp_rt.h, step 3). start-job is an IO pin the
// requester raises, so it is not one of the outputs the function writes every
// cycle.
func (r *rtIO) clearStartJob() { r.out.start_job_clears++ }

// read takes the latest input frame into r.in.
func (r *rtIO) read() { r.toGo.Read(&r.in) }

// waitFirstFrame waits for the cycle function's first run, so the first read
// sees the pins and not the zero frame.
func (r *rtIO) waitFirstFrame(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		r.read()
		if r.in.cycle != 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// invoke runs the cycle function once, without a HAL thread (tests).
func (r *rtIO) invoke() { C.pnp_rt_invoke(r.blk, C.long(time.Millisecond)) }

// free releases everything; from Destroy, past the launcher's RT barrier.
func (r *rtIO) free() {
	if r.toGo != nil {
		r.toGo.Free()
	}
	if r.toRT != nil {
		r.toRT.Free()
	}
	if r.inMap != nil {
		r.inMap.Free()
	}
	if r.outMap != nil {
		r.outMap.Free()
	}
	if r.bell != nil {
		r.bell.Free()
	}
	hal.RTFree(r.blk)
	r.blk = nil
}

// Input frame accessors for the control loop: plain Go values out of the C
// fields.

func b8(v C.uint8_t) bool { return v != 0 }

// trayIDs is each tray station's tray-id in the last input frame.
func (r *rtIO) trayIDs(n int) []uint32 {
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = uint32(r.in.tray[i].tray_id)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Motion commands after the outputs
// ---------------------------------------------------------------------------

// committedMotion commits the output frame before every motion command.
//
// Motion commands reach motmod at once, through motctl; the outputs reach the
// pins at the next commit. Without this, an output written before a command —
// scene-busy dropped before a descent, a release before a travel — could still
// read its old value while the head already moved. Committed first, it reaches
// the pins in the same servo cycle motmod takes the command (the cycle function
// runs after the command handler in the thread), so pins and motion change
// together.
//
// It wraps the motion stack once the configuration push is done, and is used
// on the control goroutine only, like the frame it commits.
type committedMotion struct {
	motionControl
	rt *rtIO
}

// note records, after a command returned, from which cycle on a frame
// includes it (rtIO.dispatchCycle), and passes the command's error through.
func (m committedMotion) note(err error) error {
	m.rt.dispatchCycle = m.rt.bell.Cycle()
	return err
}

func (m committedMotion) Abort() error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.Abort())
}
func (m committedMotion) Enable() error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.Enable())
}
func (m committedMotion) Disable() error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.Disable())
}
func (m committedMotion) SetFree() error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.SetFree())
}
func (m committedMotion) SetTeleop() error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.SetTeleop())
}
func (m committedMotion) SetCoord() error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.SetCoord())
}
func (m committedMotion) JointHome(joint int32) error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.JointHome(joint))
}
func (m committedMotion) JogCont(num int32, vel float64, isTeleop int32) error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.JogCont(num, vel, isTeleop))
}
func (m committedMotion) JogAbort(num int32, isTeleop int32) error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.JogAbort(num, isTeleop))
}
func (m committedMotion) SetLine(pos motctl.Pose, vel, iniMaxvel, acc float64,
	motionType, id int32, feedMmPerMin float64, indexerJnum int32) error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.SetLine(pos, vel, iniMaxvel, acc, motionType, id, feedMmPerMin, indexerJnum))
}
func (m committedMotion) SetCircle(pos motctl.Pose, center, normal motctl.Cartesian, turn int32,
	vel, iniMaxvel, acc float64, motionType, id int32, feedMmPerMin float64) error {
	m.rt.applyBeforeMotion()
	return m.note(m.motionControl.SetCircle(pos, center, normal, turn, vel, iniMaxvel, acc, motionType, id, feedMmPerMin))
}

// ---------------------------------------------------------------------------
// Motion status from the frame
// ---------------------------------------------------------------------------

var (
	errNoSnapshot  = errors.New("no motion snapshot: the motion instance is not resolved yet")
	errStaleStatus = errors.New("the motion snapshot predates the last motion command")
)

// rtStatus is the control loop's motion status in production: motmod's state
// as the cycle function read it, in the same frame as the pins (pnp_rt.h, step
// 5). It replaces the motstat snapshot, whose read took a mutex, could fail,
// and came from another moment than the pins.
//
// A frame from before the last motion command is refused with
// errStaleStatus. The loop treats a failed read as it always has -- the last
// good status, counted against the stale tolerance -- and the first read that
// succeeds after a dispatch therefore already describes the machine after it:
// what dispatchSettleTicks used to guess at.
//
// It reads rtIO.in, so it describes the frame the current sample() took.
type rtStatus struct {
	rt        *rtIO
	numJoints int
}

func (s *rtStatus) GetStatus() (motstat.MotionStatus, error) {
	f := &s.rt.in
	if f.mot_ok == 0 {
		return motstat.MotionStatus{}, errNoSnapshot
	}
	if int32(uint32(f.cycle)-s.rt.dispatchCycle) <= 0 {
		return motstat.MotionStatus{}, errStaleStatus
	}
	m := &f.mot
	st := motstat.MotionStatus{
		CommandNumEcho: int32(m.command_num_echo),
		CommandStatus:  int32(m.command_status),
		MotionState:    int32(m.motion_state),
		Enabled:        int32(m.enabled),
		Inpos:          int32(m.inpos),
		QueueDepth:     int32(m.queue_depth),
		Id:             int32(m.id),
		DistanceToGo:   float64(m.distance_to_go),
		CurrentVel:     float64(m.current_vel),
		CartePosCmd:    snapPose(&m.carte_pos_cmd),
		CartePosFb:     snapPose(&m.carte_pos_fb),
		CartePosCmdOk:  int32(m.carte_pos_cmd_ok),
		CartePosFbOk:   int32(m.carte_pos_fb_ok),
	}
	for j := 0; j < s.numJoints && j < len(st.Joints); j++ {
		if uint32(m.joints_homed)&(1<<j) != 0 {
			st.Joints[j].Homed = 1
		}
		if uint32(m.joints_homing)&(1<<j) != 0 {
			st.Joints[j].Homing = 1
		}
	}
	return st, nil
}

// GetPosFb and GetPosCmd return the positions whatever their _ok flags say,
// as motstat's did; a frame without a snapshot is the only failure.
func (s *rtStatus) GetPosFb() (motstat.Pose, error) {
	if s.rt.in.mot_ok == 0 {
		return motstat.Pose{}, errNoSnapshot
	}
	return snapPose(&s.rt.in.mot.carte_pos_fb), nil
}

func (s *rtStatus) GetPosCmd() (motstat.Pose, error) {
	if s.rt.in.mot_ok == 0 {
		return motstat.Pose{}, errNoSnapshot
	}
	return snapPose(&s.rt.in.mot.carte_pos_cmd), nil
}

func snapPose(p *C.mot_pose_t) motstat.Pose {
	return motstat.Pose{
		X: float64(p.x), Y: float64(p.y), Z: float64(p.z),
		A: float64(p.a), B: float64(p.b), C: float64(p.c),
		U: float64(p.u), V: float64(p.v), W: float64(p.w),
	}
}
