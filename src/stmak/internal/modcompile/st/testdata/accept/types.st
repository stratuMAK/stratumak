{attribute 'summary' := 'Every type that reaches a HAL pin'}
{attribute 'license' := 'GPL'}

(* The pin side of the type table in docs/dev/MODCOMPILE_ST_FRONTEND.md §3.1.
   BOOL, DINT, UDINT and LREAL map exactly; SINT/INT and USINT/UINT are
   widened.  LINT, ULINT, TIME, STRING, STRUCT and FUNCTION_BLOCK cannot
   appear on a pin and stay internal here.  REAL on a pin is accepted with a
   warning, not rejected -- see reject/ for what is refused outright. *)

TYPE
  Celsius : LREAL;
  Counter : UDINT;
END_TYPE

PROGRAM types
  VAR_INPUT
    flag     : BOOL;
    small    : SINT;
    medium   : INT;
    large    : DINT;
    usmall   : USINT;
    umedium  : UINT;
    ularge   : UDINT;
    precise  : LREAL;
    temp     : Celsius;
    ticks    : Counter;
  END_VAR
  VAR_OUTPUT
    ok       : BOOL;
    sum      : DINT;
    scaled   : LREAL;
    ticked   : UDINT;
    widened  : UDINT;
  END_VAR
  VAR
    (* No HAL representation: internal only. *)
    wide     : LINT;
    uwide    : ULINT;
    label    : STRING[32];
    shortstr : STRING;
    approx   : REAL;
  END_VAR

  ok := flag;
  sum := small + medium + large;
  scaled := precise + temp;
  ticked := ticks + 1;
  wide := large;
  uwide := ularge;
  approx := 1.5;
  widened := ularge + usmall + umedium;
END_PROGRAM
