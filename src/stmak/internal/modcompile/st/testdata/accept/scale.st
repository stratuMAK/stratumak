{attribute 'summary'     := 'Scale and offset a value'}
{attribute 'description' := 'Computes out := in * gain + offset, with the result clamped to [min, max] when limiting is enabled.'}
{attribute 'license'     := 'GPL'}
{attribute 'author'      := 'stratuMAK'}
{attribute 'see_also'    := '\fBmult2\fR(9), \fBsum2\fR(9).'}

(* Exercises the full metadata vocabulary docgen needs, plus LREAL
   arithmetic and an underscore name that becomes a hyphenated pin:
   max_limit here is mycomp.max-limit in HAL. *)

PROGRAM scale
  VAR_INPUT
    in        : LREAL;
    gain      : LREAL := 1.0;
    offset    : LREAL := 0.0;
    limiting  : BOOL;
    min_limit : LREAL := -1.0E6;
    max_limit : LREAL := 1.0E6;
  END_VAR
  VAR_OUTPUT
    out       : LREAL;
    clamped   : BOOL;
  END_VAR
  VAR
    raw : LREAL;
  END_VAR

  raw := in * gain + offset;
  clamped := FALSE;

  IF limiting THEN
    IF raw > max_limit THEN
      raw := max_limit;
      clamped := TRUE;
    ELSIF raw < min_limit THEN
      raw := min_limit;
      clamped := TRUE;
    END_IF;
  END_IF;

  out := raw;
END_PROGRAM
