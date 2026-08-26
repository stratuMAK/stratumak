// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// A duration literal is `T#` followed by a run of magnitude-and-unit pairs:
// T#0s, T#500ms, T#1m30s, T#1d2h3m4s5ms.  stratuMAK holds a TIME as int64
// nanoseconds, which is what get_time() and a task's period already are, so
// the parser's whole job is to reach that number.
//
// See docs/dev/MODCOMPILE_ST_STDLIB.md §5.

// durationUnit is one unit suffix and what it is worth in nanoseconds.  The
// order matters twice over: it is the order the units must appear in, and it
// is the order they are matched in, so `ms` is found before `m`.
type durationUnit struct {
	suffix string
	ns     int64
}

var durationUnits = []durationUnit{
	{"d", 24 * 60 * 60 * 1e9},
	{"h", 60 * 60 * 1e9},
	{"ms", 1e6}, // before "m", or "1ms" reads as one minute and a stray s
	{"m", 60 * 1e9},
	{"s", 1e9},
	{"us", 1e3},
	{"ns", 1},
}

// unitOrder is the position of each unit in a well-formed literal.  Matching
// order and ordinal order differ -- ms is matched second and ordered fourth --
// so they are kept apart rather than inferred from the slice index.
var unitOrder = map[string]int{"d": 0, "h": 1, "m": 2, "s": 3, "ms": 4, "us": 5, "ns": 6}

// parseDuration converts the body of a T# literal to nanoseconds.
//
// IEC's rules, enforced rather than assumed: the units run from largest to
// smallest and none repeats; the first magnitude may overrun its unit, so
// T#25h is legal and means what it says; and a fraction is allowed only on the
// last magnitude, because `T#1.5m30s` has no meaning anyone agrees on.
func parseDuration(body string) (int64, error) {
	s := strings.ToLower(strings.ReplaceAll(body, "_", ""))
	if s == "" {
		return 0, fmt.Errorf("a duration needs a magnitude and a unit, as in T#500ms")
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("a duration needs a magnitude and a unit, as in T#500ms")
	}

	var total int64
	lastOrder := -1
	sawFraction := false
	first := true

	for s != "" {
		if sawFraction {
			return 0, fmt.Errorf("only the last magnitude of a duration may have a fraction")
		}

		// The magnitude.
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
			i++
		}
		frac := false
		if i < len(s) && s[i] == '.' {
			frac = true
			i++
			for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
				i++
			}
		}
		if i == 0 || (frac && i == 1) {
			return 0, fmt.Errorf("%q is not a magnitude; a duration is written T#1m30s", s)
		}
		magnitude := s[:i]
		s = s[i:]

		// The unit.
		unit := durationUnit{}
		for _, u := range durationUnits {
			if strings.HasPrefix(s, u.suffix) {
				unit = u
				break
			}
		}
		if unit.suffix == "" {
			if s == "" {
				return 0, fmt.Errorf("the magnitude %s has no unit; write d, h, m, s, ms, us or ns", magnitude)
			}
			return 0, fmt.Errorf("%q is not a duration unit; write d, h, m, s, ms, us or ns", s)
		}
		s = s[len(unit.suffix):]

		order := unitOrder[unit.suffix]
		if order <= lastOrder {
			return 0, fmt.Errorf("the units of a duration run from largest to smallest and none repeats, "+
				"so %s cannot follow what came before it", unit.suffix)
		}
		lastOrder = order

		if frac {
			sawFraction = true
			f, err := strconv.ParseFloat(magnitude, 64)
			if err != nil {
				return 0, fmt.Errorf("%s is not a magnitude", magnitude)
			}
			total += int64(f * float64(unit.ns))
			first = false
			continue
		}

		n, err := strconv.ParseInt(magnitude, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s does not fit in a duration", magnitude)
		}
		// Only the first magnitude may overrun its unit; T#1m90s is as
		// meaningless as writing the minutes twice.
		if !first && n >= overrunLimit(unit.suffix) {
			return 0, fmt.Errorf("%d%s overruns its unit; only the first magnitude of a duration may",
				n, unit.suffix)
		}
		first = false

		if n != 0 && (n > (1<<62)/unit.ns) {
			return 0, fmt.Errorf("the duration is too large to hold in nanoseconds")
		}
		total += n * unit.ns
	}

	if neg {
		total = -total
	}
	return total, nil
}

// overrunLimit is the value at which a magnitude has spilled into the unit
// above it: 60 seconds are a minute, 1000 milliseconds a second.
func overrunLimit(suffix string) int64 {
	switch suffix {
	case "h":
		return 24
	case "m", "s":
		return 60
	case "ms", "us", "ns":
		return 1000
	}
	return 1 << 62
}

// ---------------------------------------------------------------------------
// TIME in an expression
// ---------------------------------------------------------------------------

// scalesDuration reports whether an operator can pair a duration with a plain
// number, which is what makes that number a count rather than a duration.
func scalesDuration(op ast.BinOp) bool {
	return op == ast.BinMul || op == ast.BinDiv
}

// checkTimeBinary types an operator with a duration on at least one side.
//
// A duration is not a number, and letting it be one would accept `PT + 1` --
// one what? -- so the cases are written out.  What is allowed is what has an
// unambiguous meaning: durations add and subtract and compare with each other,
// scale by a count, and divide by each other to give the ratio that is the
// only sensible reading of "how many of these fit in that".
func (c *checker) checkTimeBinary(v *st.BinaryExpr, x, y ast.Expr, xt, yt ast.Type) ast.Expr {
	time := ast.Type{Kind: ast.TypeTime}
	both := xt.Kind == ast.TypeTime && yt.Kind == ast.TypeTime

	if v.Op.IsComparison() {
		if !both {
			c.errorf(v.Pos, "a duration can only be compared with another duration, not with %s",
				other(xt, yt))
			return nil
		}
		return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: ast.Type{Kind: ast.TypeBool}}
	}

	switch v.Op {
	case ast.BinAdd, ast.BinSub:
		if !both {
			c.errorf(v.Pos, "%s needs two durations; %s is not one. "+
				"To shift a duration by a number of seconds, write SECONDS_TO_TIME(n)",
				v.Op, other(xt, yt))
			return nil
		}
		return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: time}

	case ast.BinMul:
		// One side must be the count.  Two durations multiplied would be an
		// area, which is not a thing TIME can hold.
		if both {
			c.errorf(v.Pos, "two durations cannot be multiplied; scale one by a number instead")
			return nil
		}
		num, numT := x, xt
		if xt.Kind == ast.TypeTime {
			num, numT = y, yt
		}
		if !numT.IsNumeric() {
			c.errorf(v.Pos, "a duration can be scaled by a number, not by %s", numT)
			return nil
		}
		return c.scaleTime(v, x, y, num, numT, time)

	case ast.BinDiv:
		if both {
			// How many of the right fit in the left.  A ratio, so a number.
			// The operands are converted rather than coerced: coerce works
			// over the numeric conversion lattice, and a duration is not on
			// it.
			lreal := ast.Type{Kind: ast.TypeLReal}
			return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op,
				X:   &ast.ConvExpr{Pos: v.Pos, X: x, Typ: lreal},
				Y:   &ast.ConvExpr{Pos: v.Pos, X: y, Typ: lreal},
				Typ: lreal}
		}
		if xt.Kind != ast.TypeTime {
			c.errorf(v.Pos, "%s cannot be divided by a duration; "+
				"dividing a duration by a number shortens it, and dividing two durations "+
				"gives their ratio", xt)
			return nil
		}
		if !yt.IsNumeric() {
			c.errorf(v.Pos, "a duration can be divided by a number or by another duration, not by %s", yt)
			return nil
		}
		// A divisor written out as zero is a mistake, not something to guard:
		// the guard of §4 exists for the divisors that cannot be known here.
		// The same rule the numeric path applies, which a duration would
		// otherwise slip past.
		if n, ok := foldInt(y); ok && n == 0 {
			c.errorf(v.Y.Position(), "division by zero")
			return nil
		}
		if f, ok := foldFloat(y); ok && f == 0 && yt.IsFloat() {
			c.errorf(v.Y.Position(), "division by zero")
			return nil
		}
		return c.scaleTime(v, x, y, y, yt, time)
	}

	c.errorf(v.Pos, "%s cannot be applied to a duration", v.Op)
	return nil
}

// scaleTime builds a multiply or divide of a duration by a count.
//
// The arithmetic happens in the duration's own type so that nanoseconds are
// not lost: a fractional count is applied as a double and converted back,
// while an integer one stays exact.
func (c *checker) scaleTime(v *st.BinaryExpr, x, y, num ast.Expr, numT, time ast.Type) ast.Expr {
	if numT.IsFloat() {
		// The whole expression goes through a double and comes back, which is
		// the only way `PT * 1.5` means what it looks like.
		lreal := ast.Type{Kind: ast.TypeLReal}
		conv := func(e ast.Expr) ast.Expr {
			if e.ExprType().Kind == ast.TypeTime {
				return &ast.ConvExpr{Pos: v.Pos, X: e, Typ: lreal}
			}
			return c.coerce(e, lreal)
		}
		scaled := &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: conv(x), Y: conv(y), Typ: lreal}
		return &ast.ConvExpr{Pos: v.Pos, X: scaled, Typ: time}
	}

	// An integer count: widen it to the duration's own width and stay exact.
	lint := ast.Type{Kind: ast.TypeLInt}
	widen := func(e ast.Expr) ast.Expr {
		if e == num {
			return &ast.ConvExpr{Pos: v.Pos, X: e, Typ: lint}
		}
		return e
	}
	return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: widen(x), Y: widen(y), Typ: time}
}

// other names whichever of the two types is not the duration, for a message
// that points at the operand the reader has to change.
func other(xt, yt ast.Type) ast.Type {
	if xt.Kind == ast.TypeTime {
		return yt
	}
	return xt
}

// ---------------------------------------------------------------------------
// Conversions
// ---------------------------------------------------------------------------

// A duration is held in nanoseconds but CODESYS holds one in milliseconds, and
// every IEC-named conversion has to agree with CODESYS or a ported source
// silently computes a number a thousand times off.  So TIME_TO_* and *_TO_TIME
// are milliseconds, exactly as they are there.
//
// Seconds are what stratuMAK works in -- fperiod is seconds, and so is every
// float pin carrying a time -- so they get their own pair of names.
// TIME_TO_SECONDS is not an IEC name and appears in no CODESYS source, which
// is the point: there is no spelling that could be read as either.
const (
	nsPerMillisecond = 1e6
	nsPerSecond      = 1e9
)

// timeConv is one conversion between a duration and a number.
type timeConv struct {
	from ast.TypeKind // TypeTime for TIME_TO_x
	to   ast.TypeKind
	// unit is how many nanoseconds one of the number's units is worth.
	unit float64
}

var timeConversions = map[string]timeConv{
	"TIME_TO_DINT":    {ast.TypeTime, ast.TypeDInt, nsPerMillisecond},
	"TIME_TO_UDINT":   {ast.TypeTime, ast.TypeUDInt, nsPerMillisecond},
	"TIME_TO_LINT":    {ast.TypeTime, ast.TypeLInt, nsPerMillisecond},
	"TIME_TO_ULINT":   {ast.TypeTime, ast.TypeULInt, nsPerMillisecond},
	"TIME_TO_REAL":    {ast.TypeTime, ast.TypeReal, nsPerMillisecond},
	"TIME_TO_LREAL":   {ast.TypeTime, ast.TypeLReal, nsPerMillisecond},
	"DINT_TO_TIME":    {ast.TypeDInt, ast.TypeTime, nsPerMillisecond},
	"UDINT_TO_TIME":   {ast.TypeUDInt, ast.TypeTime, nsPerMillisecond},
	"LINT_TO_TIME":    {ast.TypeLInt, ast.TypeTime, nsPerMillisecond},
	"ULINT_TO_TIME":   {ast.TypeULInt, ast.TypeTime, nsPerMillisecond},
	"REAL_TO_TIME":    {ast.TypeReal, ast.TypeTime, nsPerMillisecond},
	"LREAL_TO_TIME":   {ast.TypeLReal, ast.TypeTime, nsPerMillisecond},
	"TIME_TO_SECONDS": {ast.TypeTime, ast.TypeLReal, 1e9},
	"SECONDS_TO_TIME": {ast.TypeLReal, ast.TypeTime, 1e9},

	// A point in time converts in seconds since 1970, which is what CODESYS
	// holds a DT in -- so DT_TO_UDINT gives the same number there and here.
	// There is no millisecond form to disagree about.
	"DT_TO_UDINT":   {ast.TypeDateTime, ast.TypeUDInt, nsPerSecond},
	"UDINT_TO_DT":   {ast.TypeUDInt, ast.TypeDateTime, nsPerSecond},
	"DT_TO_LINT":    {ast.TypeDateTime, ast.TypeLInt, nsPerSecond},
	"LINT_TO_DT":    {ast.TypeLInt, ast.TypeDateTime, nsPerSecond},
	"DT_TO_SECONDS": {ast.TypeDateTime, ast.TypeLReal, nsPerSecond},
	"SECONDS_TO_DT": {ast.TypeLReal, ast.TypeDateTime, nsPerSecond},
}

// checkTimeConversion resolves one of the calls above, or reports that this is
// not one.  It runs before the scope lookup in checkCall, so a file declaring
// its own FUNCTION of the same name is refused as a redeclaration rather than
// silently shadowing the conversion.
func (c *checker) checkTimeConversion(v *st.CallExpr, name string) (ast.Expr, bool) {
	conv, ok := timeConversions[strings.ToUpper(name)]
	if !ok {
		return nil, false
	}
	if len(v.Args) != 1 {
		c.errorf(v.Pos, "%s takes one argument, got %d", strings.ToUpper(name), len(v.Args))
		return nil, true
	}

	from := ast.Type{Kind: conv.from}
	to := ast.Type{Kind: conv.to}

	arg := c.checkExpr(v.Args[0].Value, from)
	if arg == nil {
		return nil, true
	}
	if at := arg.ExprType(); at.Kind != conv.from {
		// A number widens into the parameter as it would for any function; a
		// duration has to be one already.
		if conv.from == ast.TypeTime || conv.from == ast.TypeDateTime {
			c.errorf(v.Args[0].Pos, "%s takes %s, got %s",
				strings.ToUpper(name), ast.Type{Kind: conv.from}, at)
			return nil, true
		}
		arg = c.assign(v.Args[0].Pos, strings.ToUpper(name), from, arg)
		if arg == nil {
			return nil, true
		}
	}

	return &ast.TimeConvExpr{Pos: v.Pos, X: arg, Unit: conv.unit, Typ: to}, true
}

// ---------------------------------------------------------------------------
// DATE_AND_TIME
// ---------------------------------------------------------------------------

// A DT# literal is a point in time: DT#2026-08-26-08:30:00, with optional
// fractional seconds.  It is held as nanoseconds since 1970-01-01 UTC, the
// same unit a TIME is in, which is what lets one be added to the other without
// a scale factor appearing from nowhere.
//
// The epoch matters only to the conversions.  Everything else works on
// differences, where it cancels.

// dtEpochGuard bounds the literals that fit in int64 nanoseconds, which runs
// from 1677 to 2262.  A date outside it is a typo far more often than it is a
// requirement.
const (
	dtMinYear = 1678
	dtMaxYear = 2261
)

// parseDateTime converts the body of a DT# literal to nanoseconds since the
// Unix epoch.
func parseDateTime(body string) (int64, error) {
	s := strings.TrimSpace(body)

	// YYYY-MM-DD-hh:mm:ss -- the date and the time of day are separated by a
	// third '-', which is IEC's spelling and not ISO's 'T'.
	date, clock, ok := strings.Cut(s, "-")
	if !ok {
		return 0, fmt.Errorf("a date and time is written DT#2026-08-26-08:30:00")
	}
	year := date
	rest := clock
	month, rest2, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, fmt.Errorf("a date and time is written DT#2026-08-26-08:30:00")
	}
	day, timePart, ok := strings.Cut(rest2, "-")
	if !ok {
		return 0, fmt.Errorf("a date and time needs a time of day, as in DT#2026-08-26-08:30:00")
	}

	y, err := strconv.Atoi(year)
	if err != nil {
		return 0, fmt.Errorf("%q is not a year", year)
	}
	mo, err := strconv.Atoi(month)
	if err != nil || mo < 1 || mo > 12 {
		return 0, fmt.Errorf("%q is not a month", month)
	}
	d, err := strconv.Atoi(day)
	if err != nil || d < 1 || d > 31 {
		return 0, fmt.Errorf("%q is not a day", day)
	}
	if y < dtMinYear || y > dtMaxYear {
		return 0, fmt.Errorf("the year %d is outside %d to %d, which is what fits in "+
			"nanoseconds since 1970", y, dtMinYear, dtMaxYear)
	}

	hh, mm, ss, ns, err := parseClock(timePart)
	if err != nil {
		return 0, err
	}

	t := time.Date(y, time.Month(mo), d, hh, mm, ss, ns, time.UTC)
	// time.Date normalises out-of-range fields rather than refusing them, so
	// 2026-02-30 would silently become 2026-03-02.  Reading them back is what
	// catches that.
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return 0, fmt.Errorf("%s-%s-%s is not a date", year, month, day)
	}
	return t.UnixNano(), nil
}

// parseClock reads hh:mm:ss with optional fractional seconds.
func parseClock(s string) (hh, mm, ss, ns int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, 0, 0, 0, fmt.Errorf("%q is not a time of day; write hh:mm:ss", s)
	}
	if hh, err = strconv.Atoi(parts[0]); err != nil || hh < 0 || hh > 23 {
		return 0, 0, 0, 0, fmt.Errorf("%q is not an hour", parts[0])
	}
	if mm, err = strconv.Atoi(parts[1]); err != nil || mm < 0 || mm > 59 {
		return 0, 0, 0, 0, fmt.Errorf("%q is not a minute", parts[1])
	}

	sec, frac, hasFrac := strings.Cut(parts[2], ".")
	if ss, err = strconv.Atoi(sec); err != nil || ss < 0 || ss > 59 {
		return 0, 0, 0, 0, fmt.Errorf("%q is not a second", parts[2])
	}
	if hasFrac {
		f, ferr := strconv.ParseFloat("0."+frac, 64)
		if ferr != nil {
			return 0, 0, 0, 0, fmt.Errorf("%q is not a fraction of a second", frac)
		}
		ns = int(f * 1e9)
	}
	return hh, mm, ss, ns, nil
}

// checkDateTimeBinary types an operator with a point in time on at least one
// side.
//
// A point is not a duration, and the difference is the whole reason they are
// two types: two points subtract to the time between them, a point and a
// duration add to another point, and two points added together are nothing at
// all -- what would twice last Tuesday be?
func (c *checker) checkDateTimeBinary(v *st.BinaryExpr, x, y ast.Expr, xt, yt ast.Type) ast.Expr {
	dt := ast.Type{Kind: ast.TypeDateTime}
	time_ := ast.Type{Kind: ast.TypeTime}
	both := xt.Kind == ast.TypeDateTime && yt.Kind == ast.TypeDateTime

	if v.Op.IsComparison() {
		if !both {
			c.errorf(v.Pos, "a point in time can only be compared with another one, not with %s",
				other2(xt, yt))
			return nil
		}
		return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: ast.Type{Kind: ast.TypeBool}}
	}

	switch v.Op {
	case ast.BinSub:
		if both {
			// How long between them.
			return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: time_}
		}
		if xt.Kind == ast.TypeDateTime && yt.Kind == ast.TypeTime {
			// Earlier by that much.
			return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: dt}
		}
		c.errorf(v.Pos, "%s minus %s has no meaning; "+
			"subtract two points in time for the duration between them, "+
			"or a duration from a point for an earlier one", xt, yt)
		return nil

	case ast.BinAdd:
		if both {
			c.errorf(v.Pos, "two points in time cannot be added; "+
				"add a duration to one to move it")
			return nil
		}
		if xt.Kind == ast.TypeTime || yt.Kind == ast.TypeTime {
			return &ast.BinaryExpr{Pos: v.Pos, Op: v.Op, X: x, Y: y, Typ: dt}
		}
		c.errorf(v.Pos, "a point in time moves by a duration, not by %s", other2(xt, yt))
		return nil
	}

	c.errorf(v.Pos, "%s cannot be applied to a point in time", v.Op)
	return nil
}

// other2 names whichever of the two is not the point in time.
func other2(xt, yt ast.Type) ast.Type {
	if xt.Kind == ast.TypeDateTime {
		return yt
	}
	return xt
}
