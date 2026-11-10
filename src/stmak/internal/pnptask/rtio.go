// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package pnptask

/*
#cgo CFLAGS: -I${SRCDIR}/../../pkg/cmodule -I${SRCDIR}/../../generated/gmi/mot -I${SRCDIR}/../../../hal -I${SRCDIR}/../../.. -I${SRCDIR}/../../../rtapi -I${SRCDIR}/../../../../include

#include "pnp_rt.h"

static void pnp_rt_link(void *blk, void *to_go, void *to_rt, void *in, void *out,
                        void *dz, void *bell, void *feed, int pickers, int trays, int procs) {
    pnp_rt_t *r = (pnp_rt_t *)blk;
    r->to_go     = (stmak_tribuf_t *)to_go;
    r->to_rt     = (stmak_tribuf_t *)to_rt;
    r->in        = (stmak_pinmap_t *)in;
    r->out       = (stmak_pinmap_t *)out;
    r->dz        = (pnp_dz_t *)dz;
    r->bell      = (stmak_doorbell_t *)bell;
    r->feed      = (pnp_rt_feed_rec_t *)feed;
    r->n_pickers = pickers;
    r->n_trays   = trays;
    r->n_procs   = procs;
}

static size_t pnp_rt_size(void) { return sizeof(pnp_rt_t); }
static size_t pnp_rt_feed_size(void) { return sizeof(pnp_rt_feed_rec_t) * PNP_RT_FEED_CAP; }

// pnp_rt_invoke runs the cycle function once without a HAL thread: the unit
// tests drive it from the control loop's tick hook.
static void pnp_rt_invoke(void *blk, long period) { pnp_rt_cycle(blk, period); }
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
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

// rtTaskStalled is the error id the cycle function latches itself
// (errTaskStalled; a test pins the two together).
const rtTaskStalled = C.PNP_RT_TASK_STALLED

// The frame limits of pnp_rt.h, which config.go enforces.
const (
	maxPickers = C.PNP_RT_MAX_PICKERS
	maxTrays   = C.PNP_RT_MAX_TRAYS
	maxProcs   = C.PNP_RT_MAX_PROCS
	maxAxes    = C.PNP_RT_MAX_AXES
)

// feedCap is the leg feed ring's size in records (pnp_rt.h, the leg feed).
const feedCap = C.PNP_RT_FEED_CAP

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

// rtBit and rtU32 are output pins the cycle function owns (busy, the error
// latch, picker.N.missing, cycle): the control loop never writes them -- it
// reads their state from the input frame -- so they have no Set. The pointer
// is only where the binding puts the pin in the output frame, which the
// function fills in before the frame goes to the pins.
type rtBit struct {
	pin *hal.Pin[bool]
	v   *C.uint8_t
}

type rtU32 struct {
	pin *hal.Pin[uint32]
	v   *C.uint32_t
}

type rtF64 struct {
	pin *hal.Pin[float64]
	v   *C.double
}

func mkRTF64(b *pinBuilder, name string, v *C.double) *rtF64 {
	return &rtF64{pin: mkPin[float64](b, name, hal.Out), v: v}
}

func mkRTBit(b *pinBuilder, name string, v *C.uint8_t) *rtBit {
	return &rtBit{pin: mkPin[bool](b, name, hal.Out), v: v}
}

func mkRTU32(b *pinBuilder, name string, v *C.uint32_t) *rtU32 {
	return &rtU32{pin: mkPin[uint32](b, name, hal.Out), v: v}
}

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
	// feed is the leg feed ring (pnp_rt.h): feedCap records in RT memory,
	// written by the loop, read by the cycle function.
	feed unsafe.Pointer

	// sent is the output frame of the last commit, sentCycle the doorbell's
	// cycle count right after it: a run that starts after the commit -- two
	// counts later -- has applied it (applyBeforeMotion).
	sent      outFrame
	sentCycle uint32

	// runRT, when set, runs the cycle function in place of the servo thread
	// (unit tests: driveRT); applyBeforeMotion then needs no thread to wait
	// for, and the control loop runs it while it waits (control.wait).
	// wakeEveryCycle, with it, ends every wait after one cycle: the unit
	// tests' motion is a fake the cycle function does not see, so its
	// progress rings nothing.
	runRT          func()
	wakeEveryCycle bool
	// afterInvoke, when set, runs after every invoke (tests): it hands what
	// the fake mot provider was fed to the scripted motion.
	afterInvoke func()

	// cmds counts the motion commands sent (committedMotion.note).
	cmds uint32

	// dispatchCycle is the doorbell's cycle count right after the last motion
	// command returned (committedMotion). motctl returns only once motmod has
	// taken the command, so a frame of a later cycle includes it; rtStatus
	// refuses older ones while dispatchPending: until read has seen such a
	// frame. Only until then -- the cycle count wraps, and a comparison with
	// a command long past would turn around after 2^31 cycles (24.9 days at
	// 1 kHz) and refuse every frame, with no command coming to end it.
	dispatchCycle   uint32
	dispatchPending bool

	// feedErrTaken is the leg feed's refusal count the loop has acted on
	// (takeFeedErr).
	feedErrTaken uint32
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
	if r.feed = hal.RTCalloc(uintptr(C.pnp_rt_feed_size())); r.feed == nil {
		return errors.New("rtapi_calloc for the leg feed failed")
	}
	var dzBlk unsafe.Pointer
	if dz != nil {
		dzBlk = dz.blk
	}
	C.pnp_rt_link(r.blk, r.toGo.RTPtr(), r.toRT.RTPtr(), r.inMap.RTPtr(), r.outMap.RTPtr(),
		dzBlk, r.bell.RTPtr(), r.feed, C.int(len(p.pickers)), C.int(len(p.trays)), C.int(len(p.procs)))
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
	rtBit := func(o *rtBit) error { return hal.BindBit(m, o.pin, base, o.v) }
	rtU32 := func(o *rtU32) error { return hal.BindU32(m, o.pin, base, o.v) }
	rtF64 := func(o *rtF64) error { return hal.BindFloat(m, o.pin, base, o.v) }

	errs := []error{
		bit(p.machineIsOn), bit(p.homed), bit(p.parked), rtBit(p.busy),
		rtBit(p.sceneBusy), rtBit(p.errorFlag),
		rtU32(p.cycle), u32(p.sync), rtU32(p.errorID), rtU32(p.errorStation),
		f64(p.planTime),
	}
	for i := range p.pickers {
		pk := &p.pickers[i]
		errs = append(errs,
			bit(pk.close), rtBit(pk.missing), bit(pk.holds), u32(pk.originID),
			rtF64(pk.posX), rtF64(pk.posY), rtF64(pk.posXMu), rtF64(pk.posYMu))
	}
	for i := range p.trays {
		t := &p.trays[i]
		errs = append(errs, bit(t.empty), bit(t.avail), bit(t.full), u32(t.count), u32(t.resets))
	}
	for i := range p.procs {
		s := &p.procs[i]
		errs = append(errs, bit(s.hasMaterial), bit(s.release), u32(s.resets))
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

// applyWait bounds waitApplied. Two servo cycles normally; the bound only
// matters if the function stopped running, and then the caller goes on -- a
// motion command goes out anyway -- rather than hanging the loop.
const applyWait = 20 * time.Millisecond

// applyBeforeMotion makes sure what the control loop wrote is on the pins
// before a motion command goes out: commit what changed since the last commit,
// and wait until the cycle function has applied it (waitApplied). Motion
// commands reach motmod at once, through motctl; without this an output
// written before a command -- scene-busy dropped before a descent -- could
// read its old value while the head already moved, to a reader outside the
// servo thread even within the cycle the command is taken in
// (motion-controller runs before the cycle function).
// Nothing changed, nothing to wait for: most commands of a leg go straight
// out.
func (r *rtIO) applyBeforeMotion() {
	if r.out != r.sent {
		r.commit()
	}
	r.waitApplied()
}

// waitApplied waits until the cycle function has started a run after the last
// commit (two cycle counts), so that commit is on the pins -- for a commit from
// an earlier tick that is long past. Bounded by applyWait.
func (r *rtIO) waitApplied() {
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

// The handshake requests (pnp_rt.h, the handshakes): fields of the output
// frame, applied by the cycle function in the run that applies the commit
// they go out with.

// endJob ends the job the frame accepted as seq: busy falls with whatever else
// this commit carries -- the outcome and the model.
func (r *rtIO) endJob(seq uint32) { r.out.job_done = C.uint32_t(seq) }

// endPark ends the park accepted as seq, as endJob.
func (r *rtIO) endPark(seq uint32) { r.out.park_done = C.uint32_t(seq) }

// setTeach, setPickerOffset and setTaskTimeout hand the cycle function what it
// needs of the params: LinearUnits and each picker's offset for the teach pins,
// the stall check's limit (seconds; 0 off).
func (r *rtIO) setTeach(linearUnits float64) { r.out.linear_units = C.double(linearUnits) }

func (r *rtIO) setPickerOffset(n int, x, y float64) {
	r.out.picker[n].x_offset = C.double(x)
	r.out.picker[n].y_offset = C.double(y)
}

func (r *rtIO) setTaskTimeout(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	r.out.task_timeout = C.uint64_t(seconds * float64(time.Second))
}

// setWake sets the servo time the cycle function rings at (pnp_rt.h, the
// change counter).
func (r *rtIO) setWake(t time.Duration) { r.out.wake_at = C.uint64_t(t) }

// actedSince reports whether the loop has changed an output since base, an
// output frame as committed earlier (control.noteIdleWork). The frame's
// bookkeeping does not count, and neither does what publish copies from the
// params: they are not in the input frame, the idle wake is what carries them.
func (r *rtIO) actedSince(base *outFrame) bool {
	a, b := r.out, *base
	b.seq, b.sync, b.taken, b.wake_at = a.seq, a.sync, a.taken, a.wake_at
	b.task_timeout, b.linear_units = a.task_timeout, a.linear_units
	for i := range a.picker {
		b.picker[i].x_offset, b.picker[i].y_offset = a.picker[i].x_offset, a.picker[i].y_offset
	}
	return a != b
}

// requestScene sets the scene lock request (pnp_rt.h, scene lock); the lock is
// the function's to grant, sceneGranted says when the frame read last shows it.
func (r *rtIO) requestScene(want bool) {
	if want {
		r.out.scene_req = 1
	} else {
		r.out.scene_req = 0
	}
}

func (r *rtIO) sceneGranted() bool { return r.in.scene_granted != 0 }

// armWatch arms a lift watch (pnp_rt.h, lift watch): past segment id, the
// stations in release (bit per process station index) have their release
// held low. setWatchID moves the segment once it is known; disarmWatch ends
// the watch, fired or not; watchFired says whether the frame read last shows
// the armed one fired.
func (r *rtIO) armWatch(id int32, release uint32) {
	r.out.watch_seq++
	r.out.watch_id = C.int32_t(id)
	r.out.watch_release = C.uint32_t(release)
}

func (r *rtIO) setWatchID(id int32) { r.out.watch_id = C.int32_t(id) }

func (r *rtIO) disarmWatch() {
	r.out.watch_id = C.int32_t(math.MaxInt32)
	r.out.watch_release = 0
}

func (r *rtIO) watchFired() bool { return r.in.watch_fired == r.out.watch_seq }

// raise asks the cycle function to latch a fault. Whether it latches is the
// function's to say: the first fault wins there (§6.5), and the loop's own
// view of the latch can be a cycle old -- an error-reset may have cleared it
// since -- so a fault is sent whatever the last frame shows. It reports false,
// and sends nothing, only while a raise sent earlier is not yet taken: a
// second one in the same frame would overwrite the first one's fields.
func (r *rtIO) raise(id, station uint32, missing uint8) bool {
	if r.raisePending() {
		return false
	}
	r.out.raise_seq++
	r.out.raise_id = C.uint32_t(id)
	r.out.raise_station = C.uint32_t(station)
	r.out.raise_missing = C.uint8_t(missing)
	return true
}

// raisePending reports whether a raise has been sent that the frame read
// last does not show taken yet.
func (r *rtIO) raisePending() bool { return r.out.raise_seq != r.in.raise_seen }

// latched reports whether an error is latched, or about to be: the frame
// shows the latch, or a raise is on its way to it.
func (r *rtIO) latched() bool { return r.in.latch.set != 0 || r.raisePending() }

// ---------------------------------------------------------------------------
// The leg feed (pnp_rt.h, §13)
// ---------------------------------------------------------------------------

// errFeedFull: a record found the ring full. dispatchLeg checks a leg's room
// before it writes anything, so this is a backstop.
var errFeedFull = errors.New("the leg feed is full")

// feedRoom is how many records the ring takes now: the cycle function has
// fed at least what the last frame says, so this errs on the safe side.
func (r *rtIO) feedRoom() int {
	return feedCap - int(uint32(r.out.feed_end)-uint32(r.in.fed))
}

// feedPut writes one record at feed_end and counts it in. The function feeds
// it once a commit has carried the new feed_end out; the triple buffer's
// publish orders the record before that frame.
func (r *rtIO) feedPut(rec *C.pnp_rt_feed_rec_t) error {
	if r.feedRoom() <= 0 {
		return errFeedFull
	}
	ring := (*[feedCap]C.pnp_rt_feed_rec_t)(r.feed)
	ring[uint32(r.out.feed_end)%feedCap] = *rec
	r.out.feed_end++
	r.cmds++
	return nil
}

// feedTermCond, feedLine and feedCircle write the records of what motctl's
// SetTermCond, SetLine and SetCircle carry, field for field.
func (r *rtIO) feedTermCond(cond int32, tolerance float64) error {
	rec := C.pnp_rt_feed_rec_t{kind: C.PNP_FEED_TERM_COND, term_cond: C.int32_t(cond), tolerance: C.double(tolerance)}
	return r.feedPut(&rec)
}

func (r *rtIO) feedLine(pos motctl.Pose, vel, iniMaxvel, acc float64,
	motionType, id int32, feedMmPerMin float64, indexerJnum int32) error {
	rec := C.pnp_rt_feed_rec_t{
		kind: C.PNP_FEED_LINE, id: C.int32_t(id), motion_type: C.int32_t(motionType),
		turn: C.int32_t(indexerJnum), vel: C.double(vel), ini_maxvel: C.double(iniMaxvel),
		acc: C.double(acc), feed_mm_per_min: C.double(feedMmPerMin), pos: feedPose(pos),
	}
	return r.feedPut(&rec)
}

func (r *rtIO) feedCircle(pos motctl.Pose, center, normal motctl.Cartesian, turn int32,
	vel, iniMaxvel, acc float64, motionType, id int32, feedMmPerMin float64) error {
	rec := C.pnp_rt_feed_rec_t{
		kind: C.PNP_FEED_CIRCLE, id: C.int32_t(id), motion_type: C.int32_t(motionType),
		turn: C.int32_t(turn), vel: C.double(vel), ini_maxvel: C.double(iniMaxvel),
		acc: C.double(acc), feed_mm_per_min: C.double(feedMmPerMin), pos: feedPose(pos),
		center: feedCart(center), normal: feedCart(normal),
	}
	return r.feedPut(&rec)
}

func feedPose(p motctl.Pose) C.mot_pose_t {
	return C.mot_pose_t{x: C.double(p.X), y: C.double(p.Y), z: C.double(p.Z),
		a: C.double(p.A), b: C.double(p.B), c: C.double(p.C),
		u: C.double(p.U), v: C.double(p.V), w: C.double(p.W)}
}

func feedCart(c motctl.Cartesian) C.mot_cartesian_t {
	return C.mot_cartesian_t{x: C.double(c.X), y: C.double(c.Y), z: C.double(c.Z)}
}

// cancelFeed voids every record written and not yet fed: the function skips to
// feed_begin in the run that applies the commit carrying it.
func (r *rtIO) cancelFeed() { r.out.feed_begin = r.out.feed_end }

// feedThrough reports whether the frame read last shows every record written
// fed, and seen by its motion snapshot.
func (r *rtIO) feedThrough() bool { return r.in.fed == r.out.feed_end }

// feedErr is the frame's count of refused records, with the last one's
// segment id and motmod's status.
func (r *rtIO) feedErr() (seq uint32, id, status int32) {
	return uint32(r.in.feed_err_seq), int32(r.in.feed_err_id), int32(r.in.feed_err_status)
}

// takeFeedErr reports a refusal the frame shows and the loop has not acted
// on yet, and takes it.
func (r *rtIO) takeFeedErr() (id, status int32, ok bool) {
	seq, id, status := r.feedErr()
	if seq == r.feedErrTaken {
		return 0, 0, false
	}
	r.feedErrTaken = seq
	return id, status, true
}

// feedErrPending reports a refusal the loop has not taken yet.
func (r *rtIO) feedErrPending() bool { return uint32(r.in.feed_err_seq) != r.feedErrTaken }

// read takes the latest input frame into r.in, and makes it the frame the next
// commit says it was computed from (sync).
func (r *rtIO) read() {
	r.toGo.Read(&r.in)
	r.out.sync = r.in.cycle
	// Acting on this frame takes its rings (the stall check, §7.2).
	r.out.taken = r.in.change
	if r.dispatchPending && int32(uint32(r.in.cycle)-r.dispatchCycle) > 0 {
		r.dispatchPending = false
	}
}

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
func (r *rtIO) invoke() {
	C.pnp_rt_invoke(r.blk, C.long(time.Millisecond))
	if r.afterInvoke != nil {
		r.afterInvoke()
	}
}

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
	hal.RTFree(r.feed)
	r.feed = nil
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

// committedMotion commits the output frame before every motctl command, with
// the leg feed cancelled.
//
// motctl commands reach motmod at once; the outputs reach the pins at the next
// commit. Without this, an output written before a command could still read
// its old value while motion already acted on it. Committed first, and waited
// for (applyBeforeMotion), it is on the pins before motmod takes the command.
// A leg's segments do not come through here: they are leg feed records, which
// go out with the commit that carries their outputs (PNPTASK_RT_BOUNDARY.md
// §13.4).
//
// What is left on motctl -- enable, disable, the mode switches, homing, jog,
// abort -- is never meant to run behind a leg still being fed: the feed is
// cancelled in the same commit, and that commit is applied before the command
// goes out, so the function feeds nothing behind an abort or a mode switch.
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
	m.rt.dispatchPending = true
	m.rt.cmds++
	return err
}

// before cancels the leg feed and puts everything on the pins ahead of a
// motctl command.
func (m committedMotion) before() {
	m.rt.cancelFeed()
	m.rt.applyBeforeMotion()
}

func (m committedMotion) Abort() error {
	m.before()
	return m.note(m.motionControl.Abort())
}
func (m committedMotion) Enable() error {
	m.before()
	return m.note(m.motionControl.Enable())
}
func (m committedMotion) Disable() error {
	m.before()
	return m.note(m.motionControl.Disable())
}
func (m committedMotion) SetFree() error {
	m.before()
	return m.note(m.motionControl.SetFree())
}
func (m committedMotion) SetTeleop() error {
	m.before()
	return m.note(m.motionControl.SetTeleop())
}
func (m committedMotion) SetCoord() error {
	m.before()
	return m.note(m.motionControl.SetCoord())
}
func (m committedMotion) JointHome(joint int32) error {
	m.before()
	return m.note(m.motionControl.JointHome(joint))
}
func (m committedMotion) JogCont(num int32, vel float64, isTeleop int32) error {
	m.before()
	return m.note(m.motionControl.JogCont(num, vel, isTeleop))
}
func (m committedMotion) JogAbort(num int32, isTeleop int32) error {
	m.before()
	return m.note(m.motionControl.JogAbort(num, isTeleop))
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
	if s.rt.dispatchPending {
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

// takeAll takes every counter of an edges block as seen (primeEdges).
func (s *edgesSeen) takeAll(e *C.pnp_rt_edges_t) {
	s.machineOn = uint32(e.machine_on)
	s.home = uint32(e.home)
	s.errorReset = uint32(e.error_reset)
	for i := range s.pickerOpen {
		s.pickerOpen[i] = uint32(e.picker[i].open)
		s.pickerClose[i] = uint32(e.picker[i].close)
	}
	for i := range s.trayReset {
		s.trayReset[i] = uint32(e.tray[i].count)
		s.trayConflicts[i] = uint32(e.tray[i].conflicts)
	}
	for i := range s.procReset {
		s.procReset[i] = uint32(e.proc[i].count)
		s.procConflicts[i] = uint32(e.proc[i].conflicts)
	}
}

// latchIn puts a latched fault into an input frame, as the cycle function
// would publish it (tests).
func latchIn(f *inFrame, id errorID) {
	f.latch.set = 1
	f.latch.id = C.uint32_t(id)
}

// edgeBlock is the edges block of the input frame (tests read it).
type edgeBlock = C.pnp_rt_edges_t
