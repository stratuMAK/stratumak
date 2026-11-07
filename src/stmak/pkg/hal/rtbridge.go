// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package hal

// The RT bridge: what a Go module with a cyclic C function needs to exchange
// data with it at runtime (docs/dev/PNPTASK_RT_BOUNDARY.md §3).
//
// GOMOD_RT_DESIGN.md covers a function that walks a structure nothing writes
// once the threads run. A function that does a module's pin I/O for it needs
// more: frames that change every cycle in one direction and every Go step in
// the other, a table of the pins it copies, and a way to tell Go that
// something changed. Three pieces, each a C header in pkg/cmodule with its Go
// handle here:
//
//   - TriBuf  (stmak_tribuf.h)  — whole frames, one writer, one reader,
//     wait-free on both sides, latest complete frame wins.
//   - PinMap  (stmak_pinmap.h)  — pins copied into a frame and back at one
//     point of the servo thread.
//   - Doorbell (stmak_doorbell.h) — change and cycle counters the function
//     bumps and Go polls.
//
// All three live in RTCalloc memory, are created in the module factory (before
// the function can run), handed to the function through its arg block by
// their RTPtr, and freed in Destroy, past the launcher's RT barrier — the
// lifecycle of ExportFunct, see there.

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// ErrNotPlainData indicates a TriBuf frame type that contains a pointer, a
// slice, a string, a map, an interface, a channel or a func. A frame is copied
// into C memory, where the collector cannot see it: a Go pointer there is
// exactly what the cgo pointer rules forbid.
var ErrNotPlainData = &Error{
	Code:    -22, // -EINVAL
	Message: "frame type must be plain data: no pointers, slices, strings, maps, interfaces, channels or funcs",
}

// ErrRTStalled indicates that a realtime function's cycle counter stood still
// for longer than the caller allowed: it is not on a thread, or its thread is
// not running.
var ErrRTStalled = &Error{
	Code:    -110, // -ETIMEDOUT
	Message: "realtime function is not running",
}

// ErrPinMap indicates a binding a PinMap refuses; the detail says which and
// why.
var ErrPinMap = &Error{
	Code:    -22, // -EINVAL
	Message: "pin map binding refused",
}

// ---------------------------------------------------------------------------
// TriBuf
// ---------------------------------------------------------------------------

// TriBuf is the Go handle on a stmak_tribuf_t carrying frames of type T.
//
// T is the frame: a struct declared in the C header the cyclic function
// includes, used here through its cgo type, so the layout is defined once. It
// must be plain data (checked at construction): the frame is copied in and out
// of C memory as bytes.
//
// One TriBuf has one writer and one reader. Which of the two is Go is the
// owner's choice: Publish on a buffer Go writes, Read on a buffer Go reads.
// Concurrent Go callers on the same side are serialized here, so the buffer
// sees one logical writer and one logical reader whatever the goroutines do.
type TriBuf[T any] struct {
	blk  unsafe.Pointer
	size uintptr
	wmu  sync.Mutex
	rmu  sync.Mutex
}

// NewTriBuf allocates and initializes a triple buffer for frames of type T.
func NewTriBuf[T any]() (*TriBuf[T], error) {
	var zero T
	size := unsafe.Sizeof(zero)
	if size == 0 {
		return nil, newError("NewTriBuf", "frame type has size 0", -22)
	}
	if size > 1<<24 {
		return nil, newError("NewTriBuf", "frame type larger than 16 MiB", -22)
	}
	if err := checkPlainData(reflect.TypeFor[T]()); err != nil {
		e := newError("NewTriBuf", ErrNotPlainData.Message, ErrNotPlainData.Code)
		e.Detail = err.Error()
		return nil, e
	}
	blk := RTCalloc(tribufSize(size))
	if blk == nil {
		return nil, newError("NewTriBuf", ErrNoMemory.Message, ErrNoMemory.Code)
	}
	tribufInit(blk, size)
	return &TriBuf[T]{blk: blk, size: size}, nil
}

// RTPtr is the stmak_tribuf_t * to hand the cyclic function.
func (b *TriBuf[T]) RTPtr() unsafe.Pointer { return b.blk }

// Publish copies *v into the write slot and hands it to the reader.
func (b *TriBuf[T]) Publish(v *T) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if b.blk == nil {
		return
	}
	tribufPublishCopy(b.blk, unsafe.Pointer(v))
}

// Read copies the latest published frame into *v and reports whether it is new
// since the previous Read. Before the writer's first publish it is the zero
// frame, not fresh.
func (b *TriBuf[T]) Read(v *T) bool {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	if b.blk == nil {
		return false
	}
	return tribufReadCopy(b.blk, unsafe.Pointer(v))
}

// Free releases the buffer. Only past the launcher's RT barrier (Destroy).
func (b *TriBuf[T]) Free() {
	b.wmu.Lock()
	b.rmu.Lock()
	defer b.wmu.Unlock()
	defer b.rmu.Unlock()
	RTFree(b.blk)
	b.blk = nil
}

// checkPlainData refuses any type that holds, or is, something the collector
// tracks.
func checkPlainData(t reflect.Type) error {
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr, reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128:
		return nil
	case reflect.Array:
		if err := checkPlainData(t.Elem()); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		return nil
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if err := checkPlainData(f.Type); err != nil {
				return fmt.Errorf("%s.%s: %w", t, f.Name, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s is a %s", t, t.Kind())
	}
}

// ---------------------------------------------------------------------------
// PinMap
// ---------------------------------------------------------------------------

// PinMap is the Go handle on a stmak_pinmap_t: the pins a cyclic function
// copies into a frame (an In map) or out of one (an Out map), every cycle.
//
// Build it in the factory, after the pins exist, with the Bind functions, then
// hand RTPtr to the function. Frame and pin types are paired by the Bind
// function used, so a bit pin can only land in a byte field, a float pin in a
// double, and so on.
//
// An In map takes pins of any direction: reading the value of an output pin is
// harmless. An Out map takes only Out pins: the map writes its pins every
// cycle, which on an IO pin would overwrite the other writer, and on an In pin
// is meaningless.
type PinMap struct {
	blk       unsafe.Pointer
	dir       Direction
	frameSize uintptr
	cap       int
	n         int
	fields    map[uintptr]string // In map: offset -> pin, one pin per field
	pins      map[unsafe.Pointer]string
}

// NewPinMap allocates a map of up to capacity pins for frames of frameSize
// bytes. dir is In (pins -> frame) or Out (frame -> pins).
func NewPinMap(dir Direction, frameSize uintptr, capacity int) (*PinMap, error) {
	if dir != In && dir != Out {
		return nil, newError("NewPinMap", "direction must be In or Out", -22)
	}
	if frameSize == 0 || capacity <= 0 || capacity > 1<<16 {
		return nil, newError("NewPinMap", "frame size and capacity must be positive", -22)
	}
	blk := RTCalloc(pinmapSize(capacity))
	if blk == nil {
		return nil, newError("NewPinMap", ErrNoMemory.Message, ErrNoMemory.Code)
	}
	pinmapInit(blk, capacity)
	return &PinMap{
		blk:       blk,
		dir:       dir,
		frameSize: frameSize,
		cap:       capacity,
		fields:    map[uintptr]string{},
		pins:      map[unsafe.Pointer]string{},
	}, nil
}

// RTPtr is the stmak_pinmap_t * to hand the cyclic function.
func (m *PinMap) RTPtr() unsafe.Pointer { return m.blk }

// Len is the number of pins bound.
func (m *PinMap) Len() int { return m.n }

// Free releases the map. Only past the launcher's RT barrier (Destroy).
func (m *PinMap) Free() {
	RTFree(m.blk)
	m.blk = nil
}

// BindBit binds a bit pin to a uint8_t frame field. frame points at any value
// of the frame type (only its address is used), field at the field in it.
func BindBit[F ~uint8](m *PinMap, p *Pin[bool], frame unsafe.Pointer, field *F) error {
	return m.bind(p.Name(), p.Direction(), p.RTDataPtr(), TypeBit, frame, unsafe.Pointer(field), 1)
}

// BindFloat binds a float pin to a double frame field.
func BindFloat[F ~float64](m *PinMap, p *Pin[float64], frame unsafe.Pointer, field *F) error {
	return m.bind(p.Name(), p.Direction(), p.RTDataPtr(), TypeFloat, frame, unsafe.Pointer(field), 8)
}

// BindS32 binds an s32 pin to an int32_t frame field.
func BindS32[F ~int32](m *PinMap, p *Pin[int32], frame unsafe.Pointer, field *F) error {
	return m.bind(p.Name(), p.Direction(), p.RTDataPtr(), TypeS32, frame, unsafe.Pointer(field), 4)
}

// BindU32 binds a u32 pin to a uint32_t frame field.
func BindU32[F ~uint32](m *PinMap, p *Pin[uint32], frame unsafe.Pointer, field *F) error {
	return m.bind(p.Name(), p.Direction(), p.RTDataPtr(), TypeU32, frame, unsafe.Pointer(field), 4)
}

func (m *PinMap) bind(name string, dir Direction, slot unsafe.Pointer, typ PinType,
	frame, field unsafe.Pointer, size uintptr) error {
	refuse := func(why string) error {
		e := newError("PinMap.Bind", ErrPinMap.Message, ErrPinMap.Code)
		e.Detail = name + ": " + why
		return e
	}
	switch {
	case m == nil || m.blk == nil:
		return refuse("map is nil or freed")
	case slot == nil:
		return refuse("pin has no data slot")
	case m.n >= m.cap:
		return refuse(fmt.Sprintf("map is full (%d pins)", m.cap))
	case m.dir == Out && dir != Out:
		return refuse(fmt.Sprintf("an Out map writes its pins every cycle, a %s pin cannot take that", dir))
	}
	if uintptr(field) < uintptr(frame) || uintptr(field)+size > uintptr(frame)+m.frameSize {
		return refuse("field lies outside the frame")
	}
	ofs := uintptr(field) - uintptr(frame)
	if ofs%size != 0 {
		return refuse(fmt.Sprintf("field at offset %d is not %d-byte aligned", ofs, size))
	}
	if m.dir == In {
		if other, ok := m.fields[ofs]; ok {
			return refuse("field already bound to " + other)
		}
	}
	if other, ok := m.pins[slot]; ok {
		return refuse("pin already bound (as " + other + ")")
	}
	if err := pinmapAdd(m.blk, slot, ofs, typ); err != nil {
		return refuse(err.Error())
	}
	m.fields[ofs] = name
	m.pins[slot] = name
	m.n++
	return nil
}

// ---------------------------------------------------------------------------
// Doorbell
// ---------------------------------------------------------------------------

// Doorbell is the Go handle on a stmak_doorbell_t: the change and cycle
// counters a cyclic function bumps, and the wait on them.
type Doorbell struct {
	blk unsafe.Pointer
}

// doorbell mirrors stmak_doorbell_t; checked against the C size in
// rtbridge_cgo.go.
type doorbell struct {
	change uint32
	cycle  uint32
}

// NewDoorbell allocates a doorbell, both counters 0.
func NewDoorbell() (*Doorbell, error) {
	blk := RTCalloc(unsafe.Sizeof(doorbell{}))
	if blk == nil {
		return nil, newError("NewDoorbell", ErrNoMemory.Message, ErrNoMemory.Code)
	}
	return &Doorbell{blk: blk}, nil
}

// RTPtr is the stmak_doorbell_t * to hand the cyclic function.
func (d *Doorbell) RTPtr() unsafe.Pointer { return d.blk }

func (d *Doorbell) words() *doorbell { return (*doorbell)(d.blk) }

// Change is the change counter now.
func (d *Doorbell) Change() uint32 { return atomic.LoadUint32(&d.words().change) }

// Cycle is the cycle counter now.
func (d *Doorbell) Cycle() uint32 { return atomic.LoadUint32(&d.words().cycle) }

// Free releases the doorbell. Only past the launcher's RT barrier (Destroy),
// and not while a Wait runs.
func (d *Doorbell) Free() {
	RTFree(d.blk)
	d.blk = nil
}

// DefaultPoll is the interval Wait looks at the counters with when the caller
// passes 0.
const DefaultPoll = time.Millisecond

// Wait returns the change counter as soon as it differs from seen: at once if
// it already does, else after polling it every poll (0: DefaultPoll).
//
// It gives up with ctx's error when ctx ends, and with ErrRTStalled when stall
// is positive and the cycle counter has not moved for that long — the function
// is not running, and nothing it would announce is coming.
func (d *Doorbell) Wait(ctx context.Context, seen uint32, poll, stall time.Duration) (uint32, error) {
	if c := d.Change(); c != seen {
		return c, nil
	}
	if poll <= 0 {
		poll = DefaultPoll
	}
	timer := time.NewTimer(poll)
	defer timer.Stop()

	lastCycle := d.Cycle()
	lastMove := time.Now()
	for {
		select {
		case <-ctx.Done():
			return seen, ctx.Err()
		case <-timer.C:
		}
		if c := d.Change(); c != seen {
			return c, nil
		}
		if cy := d.Cycle(); cy != lastCycle {
			lastCycle = cy
			lastMove = time.Now()
		} else if stall > 0 && time.Since(lastMove) >= stall {
			return seen, newError("Doorbell.Wait", ErrRTStalled.Message, ErrRTStalled.Code)
		}
		timer.Reset(poll)
	}
}
