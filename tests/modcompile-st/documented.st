{attribute 'summary'     := 'Every documentation field, exercised'}
{attribute 'description' := 'A component that exists to prove its man page is as complete as one generated from a .comp: description, function docs, per-pin docs, notes, examples, author, licence and see-also.'}
{attribute 'license'     := 'GPL'}
{attribute 'author'      := 'stratuMAK'}
{attribute 'see_also'    := '\fBscale\fR(9), \fBand2\fR(9).'}
{attribute 'notes'       := 'The two functions may run in different threads; only \fBupdate\fR needs a floating-point one.'}
{attribute 'examples'    := 'loadrt documented$Naddf documented.sample servo-thread$Naddf documented.update servo-thread'}

(* Metadata before the first declaration describes the component.  A pragma
   heading a PROGRAM documents that program's HAL function, and one inside a
   VAR section documents the pin it precedes. *)

VAR_GLOBAL
  latest : DINT;
END_VAR

{attribute 'doc' := 'Latches the raw input when strobed. Uses no floating point.'}
PROGRAM sample
  VAR_INPUT
    {attribute 'doc' := 'Value to latch.'}
    raw : DINT;
    {attribute 'doc' := 'Rising level latches \fBraw\fR.'}
    strobe : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'TRUE once a value has been latched.'}
    valid : BOOL;
  END_VAR

  IF strobe THEN
    latest := raw;
    valid := TRUE;
  END_IF;
END_PROGRAM

{attribute 'doc' := 'Scales the latched value. Requires a floating-point thread.'}
PROGRAM update
  VAR_INPUT
    {attribute 'doc' := 'Multiplier applied to the latched value.'}
    gain : LREAL := 1.0;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The latched value times \fBgain\fR.'}
    scaled : LREAL;
  END_VAR

  scaled := latest * gain;
END_PROGRAM
