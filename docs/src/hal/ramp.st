{attribute 'summary'     := 'Limit how fast a value may change'}
{attribute 'description' := 'Follows \fBtarget\fR at no more than \fBmax-rate\fR units per second, so a step on the input becomes a ramp on the output. Setting \fBenable\fR FALSE passes the target straight through.'}
{attribute 'license'     := 'GPL'}
{attribute 'see_also'    := '\fBlimit1\fR(9), \fBlimit2\fR(9).'}

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

{attribute 'doc' := 'Advances the output one cycle towards the target.'}
PROGRAM ramp
  VAR_INPUT
    {attribute 'doc' := 'The value to follow.'}
    target   : LREAL;
    {attribute 'doc' := 'Largest change per second.'}
    max_rate : LREAL := 10.0;
    {attribute 'doc' := 'FALSE passes \fBtarget\fR through unchanged.'}
    enable   : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The rate-limited value.'}
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
