{attribute 'summary' := 'Stateless helper functions'}
{attribute 'license' := 'GPL'}

(* A FUNCTION keeps no state between calls, so it may be called from an
   expression -- unlike a FUNCTION_BLOCK, whose invocation is a statement
   because it mutates its instance. *)

FUNCTION Clamp : LREAL
  VAR_INPUT
    value : LREAL;
    low   : LREAL;
    high  : LREAL;
  END_VAR

  IF value < low THEN
    Clamp := low;
  ELSIF value > high THEN
    Clamp := high;
  ELSE
    Clamp := value;
  END_IF;
END_FUNCTION

FUNCTION Lerp : LREAL
  VAR_INPUT
    a, b : LREAL;
    t    : LREAL;
  END_VAR
  VAR
    span : LREAL;
  END_VAR

  span := b - a;
  Lerp := a + span * Clamp(t, 0.0, 1.0);
END_FUNCTION

PROGRAM funcs
  VAR_INPUT
    raw  : LREAL;
    blend : LREAL;
  END_VAR
  VAR_OUTPUT
    limited : LREAL;
    mixed   : LREAL;
  END_VAR

  limited := Clamp(raw, -10.0, 10.0);
  mixed := Lerp(0.0, limited, blend);
END_PROGRAM
