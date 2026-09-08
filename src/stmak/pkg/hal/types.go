// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package hal

// Direction represents the direction of data flow for a HAL pin.
// It corresponds to hal_pin_dir_t in the C HAL API.
type Direction int

const (
	// In indicates the pin is an input to the component (HAL_IN = 16).
	// The component reads values from this pin.
	In Direction = 16

	// Out indicates the pin is an output from the component (HAL_OUT = 32).
	// The component writes values to this pin.
	Out Direction = 32

	// IO indicates the pin is bidirectional (HAL_IO = HAL_IN | HAL_OUT).
	// The component can both read from and write to this pin.
	IO Direction = 48 // HAL_IN | HAL_OUT
)

// String returns the string representation of the direction.
func (d Direction) String() string {
	switch d {
	case In:
		return "IN"
	case Out:
		return "OUT"
	case IO:
		return "IO"
	default:
		return "UNKNOWN"
	}
}

// ParamDirection represents the access direction of a HAL parameter.
// It corresponds to hal_param_dir_t in the C HAL API.
//
// Parameters are not linked to signals, so their direction describes access
// from *outside* the component only: the owning component always writes its own
// parameters.
type ParamDirection int

const (
	// RO indicates the parameter is read-only from outside (HAL_RO = 64).
	// The component writes it to provide a view into its internal state;
	// halcmd can display it but "halcmd setp" is refused.
	RO ParamDirection = 64

	// RW indicates the parameter is writable from outside (HAL_RW = 192).
	// This is the tuning knob case: "halcmd setp" adjusts the value at
	// runtime, and the component both reads and (initially) writes it.
	//
	// HAL_RW is HAL_RO | HAL_WO — the one exception to hal_param_dir_t's
	// powers-of-two rule.
	RW ParamDirection = 192
)

// String returns the string representation of the parameter direction.
func (d ParamDirection) String() string {
	switch d {
	case RO:
		return "RO"
	case RW:
		return "RW"
	default:
		return "UNKNOWN"
	}
}

// PinType represents the data type of a HAL pin or signal.
// It corresponds to hal_type_t in the C HAL API.
type PinType int

const (
	// TypeBit represents a boolean value (HAL_BIT = 1).
	TypeBit PinType = 1

	// TypeFloat represents a 64-bit floating point value (HAL_FLOAT = 2).
	TypeFloat PinType = 2

	// TypeS32 represents a signed 32-bit integer (HAL_S32 = 3).
	TypeS32 PinType = 3

	// TypeU32 represents an unsigned 32-bit integer (HAL_U32 = 4).
	TypeU32 PinType = 4

	// TypePort represents a byte-stream port (HAL_PORT = 5).
	// Used as the underlying transport for string pins.
	TypePort PinType = 5
)

// String returns the string representation of the pin type.
func (t PinType) String() string {
	switch t {
	case TypeBit:
		return "BIT"
	case TypeFloat:
		return "FLOAT"
	case TypeS32:
		return "S32"
	case TypeU32:
		return "U32"
	case TypePort:
		return "PORT"
	default:
		return "UNKNOWN"
	}
}

// PinValue is a type constraint for values that can be stored in HAL pins.
// These correspond to the actual HAL data types supported by LinuxCNC.
type PinValue interface {
	bool | float64 | int32 | uint32 | string
}

// pinTypeOf maps a Go value type from the PinValue constraint to its HAL type.
// It is the single copy of that mapping, shared by NewPin/NewParam and the
// Type() accessors in both the cgo and non-cgo builds, so the sites cannot
// drift when a HAL type is added. ok is false only for a type outside the
// constraint, which the compiler prevents; callers still branch on it so an
// impossible mapping fails loudly instead of silently yielding type 0.
// ParamValue's type set is a subset of PinValue's, so parameter constructors
// instantiate it too (string maps to TypePort, which ParamValue rules out).
func pinTypeOf[T PinValue]() (typ PinType, ok bool) {
	var zero T
	switch any(zero).(type) {
	case bool:
		return TypeBit, true
	case float64:
		return TypeFloat, true
	case int32:
		return TypeS32, true
	case uint32:
		return TypeU32, true
	case string:
		return TypePort, true
	default:
		return 0, false
	}
}

// ParamValue is a type constraint for values that can be stored in HAL
// parameters. It is PinValue minus string: HAL has no HAL_PORT parameter
// (hal_param_new rejects any type other than HAL_BIT, HAL_FLOAT, HAL_S32 and
// HAL_U32), and a port is a buffer that only makes sense behind a signal.
type ParamValue interface {
	bool | float64 | int32 | uint32
}
