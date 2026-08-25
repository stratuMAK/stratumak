{attribute 'summary' := 'Rate-limited position follower'}
{attribute 'license' := 'GPL'}
{attribute 'author'  := 'stratuMAK'}

(* The worked example of docs/dev/MODCOMPILE_ST_FRONTEND.md §3.  A function
   block is internal: Ramp has no HAL identity, only ramplimit's own
   VAR_INPUT/VAR_OUTPUT become pins.  Where a sub-component's input should be
   wirable, declare the pin at PROGRAM level and pass it in -- which is what
   `target := target` does here. *)

FUNCTION_BLOCK Ramp
  VAR_INPUT
    target : LREAL;
    rate   : LREAL;
    dt     : LREAL;
  END_VAR
  VAR_OUTPUT
    out    : LREAL;
  END_VAR
  VAR
    step   : LREAL;
  END_VAR

  step := rate * dt;
  IF target > out + step THEN
    out := out + step;
  ELSIF target < out - step THEN
    out := out - step;
  ELSE
    out := target;
  END_IF;
END_FUNCTION_BLOCK

PROGRAM ramplimit
  VAR_INPUT
    {attribute 'doc' := 'Commanded position.'}
    target   : LREAL;
    {attribute 'doc' := 'Maximum rate of change, units per second.'}
    max_rate : LREAL := 10.0;
    enable   : BOOL;
  END_VAR
  VAR_OUTPUT
    out      : LREAL;
  END_VAR
  VAR
    r : Ramp;
  END_VAR

  IF enable THEN
    r(target := target, rate := max_rate, dt := fperiod);
    out := r.out;
  ELSE
    out := target;
  END_IF;
END_PROGRAM
