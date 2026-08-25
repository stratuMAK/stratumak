{attribute 'summary' := 'Named constants as bounds, limits and labels'}
{attribute 'license' := 'GPL'}

(* Real ST names its bounds far more often than it writes them out, and a
   constant is only useful where the compiler can see its value: sizing an
   array, bounding a FOR, labelling a CASE arm.  A constant written as an
   expression counts too -- (24 * 60) is a value, not a computation. *)

TYPE
  Mode  : (OFF, MANUAL, AUTO);
  State : (OFF, RUNNING, FAULT);
END_TYPE

FUNCTION_BLOCK History
  VAR CONSTANT
    SLOTS       : DINT := 8;
    LAST_SLOT   : DINT := SLOTS - 1;
    WINDOW      : DINT := (2 * 4);
  END_VAR
  VAR_INPUT
    sample : LREAL;
  END_VAR
  VAR_OUTPUT
    mean : LREAL;
  END_VAR
  VAR
    (* An array of a block's own constant: the bound has to resolve while the
       rest of the block's declarations are still being read. *)
    slot  : ARRAY[0..LAST_SLOT] OF LREAL;
    total : LREAL;
    i     : DINT;
  END_VAR

  FOR i := 0 TO LAST_SLOT - 1 DO
    slot[i] := slot[i + 1];
  END_FOR;
  slot[LAST_SLOT] := sample;

  total := 0.0;
  FOR i := 0 TO LAST_SLOT DO
    total := total + slot[i];
  END_FOR;
  mean := total / WINDOW;
END_FUNCTION_BLOCK

PROGRAM constants
  VAR CONSTANT
    CHANNELS       : DINT := 4;
    MINUTES_PER_DAY : DINT := (24 * 60);
    SCALE          : LREAL := 1.0 / 5.0;
  END_VAR
  VAR_INPUT
    raw     : ARRAY[0..3] OF LREAL;
    request : DINT := 0;
  END_VAR
  VAR_OUTPUT
    average : LREAL;
    minutes : DINT;
    active  : BOOL;
  END_VAR
  VAR
    hist : History;
    mode : Mode  := Mode.OFF;
    step : State := State.OFF;
    sum  : LREAL;
    i    : DINT;
  END_VAR

  sum := 0.0;
  FOR i := 0 TO CHANNELS - 1 DO
    sum := sum + raw[i];
  END_FOR;
  average := sum * SCALE;
  minutes := MINUTES_PER_DAY;

  hist(sample := average);

  (* Both enumerations declare OFF, so an unqualified OFF would be ambiguous;
     the qualified form says which is meant. *)
  CASE request OF
    0: mode := Mode.OFF;
    1: mode := Mode.MANUAL;
    2: mode := Mode.AUTO;
  ELSE
    mode := Mode.OFF;
  END_CASE;

  IF mode = Mode.OFF THEN
    step := State.OFF;
  ELSE
    step := State.RUNNING;
  END_IF;

  active := step = State.RUNNING;
END_PROGRAM
