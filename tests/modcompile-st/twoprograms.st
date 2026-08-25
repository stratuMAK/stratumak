{attribute 'summary' := 'Two thread functions sharing state'}
{attribute 'license' := 'GPL'}

(* One PROGRAM per exported HAL function.  These become twoprograms.read and
   twoprograms.write, addf-able to different threads, with VAR_GLOBAL the
   state they share.  Each PROGRAM carries its own pins.

   `read` touches no floating point, so it should export with fp = 0. *)

VAR_GLOBAL
  latched : DINT;
  ready   : BOOL;
END_VAR

PROGRAM read
  VAR_INPUT
    raw    : DINT;
    strobe : BOOL;
  END_VAR
  VAR_OUTPUT
    accepted : BOOL;
  END_VAR

  IF strobe THEN
    latched := raw;
    ready := TRUE;
  END_IF;
  accepted := ready;
END_PROGRAM

PROGRAM write
  VAR_INPUT
    gain : LREAL := 1.0;
  END_VAR
  VAR_OUTPUT
    scaled : LREAL;
    valid  : BOOL;
  END_VAR

  IF ready THEN
    scaled := latched * gain;
    valid  := TRUE;
  ELSE
    scaled := 0.0;
    valid  := FALSE;
  END_IF;
END_PROGRAM
