{attribute 'summary' := 'Select and average from an array pin'}
{attribute 'license' := 'GPL'}

(* Array pins map one dimension only, since a HAL pin name carries one
   index: channel.0 .. channel.7.  Multi-dimensional arrays are fine as
   internal state.  The non-zero lower bound on `window` is deliberate --
   every index site has to subtract it. *)

PROGRAM arrays
  VAR_INPUT
    channel  : ARRAY[0..7] OF LREAL;
    selected : DINT := 0;
  END_VAR
  VAR_OUTPUT
    value   : LREAL;
    average : LREAL;
  END_VAR
  VAR
    window : ARRAY[1..4] OF LREAL;
    grid   : ARRAY[0..1, 0..2] OF LREAL;
    total  : LREAL;
    i, j   : DINT;
  END_VAR

  value := channel[selected];

  total := 0.0;
  FOR i := 0 TO 7 DO
    total := total + channel[i];
  END_FOR;
  average := total / 8.0;

  FOR i := 1 TO 4 DO
    window[i] := average;
  END_FOR;

  FOR i := 0 TO 1 DO
    FOR j := 0 TO 2 DO
      grid[i, j] := average;
    END_FOR;
  END_FOR;
END_PROGRAM
