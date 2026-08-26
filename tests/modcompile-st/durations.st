{attribute 'summary' := 'The TIME type: literals, arithmetic and conversions'}
{attribute 'license' := 'GPL'}

(* A duration is held as int64 nanoseconds -- what get_time() and a task's
   period already are.  It is not a number: it adds to and compares with
   another duration, scales by a count, and divides by another duration to give
   their ratio, and anything else is refused rather than guessed at.

   See docs/dev/MODCOMPILE_ST_STDLIB.md §5. *)

TYPE Config : STRUCT
  (* The shape the production sources actually use: PT is read from a struct
     member far more often than it is written as a literal. *)
  errDelay  : TIME;
  pumpOff   : TIME;
END_STRUCT
END_TYPE

FUNCTION_BLOCK Elapsed
  VAR_INPUT
    run : BOOL;
  END_VAR
  VAR_OUTPUT
    et : TIME;
  END_VAR

  IF run THEN
    (* fperiod is LREAL seconds, so the bridge is named for seconds. *)
    et := et + SECONDS_TO_TIME(fperiod);
  ELSE
    et := T#0s;
  END_IF;
END_FUNCTION_BLOCK

PROGRAM durations
  VAR_INPUT
    run : BOOL;
    (* HAL has no duration type, so a settable one arrives as seconds. *)
    delay_s : LREAL;
  END_VAR
  VAR_OUTPUT
    expired    : BOOL;
    elapsed_ms : DINT;
    remaining  : LREAL;
    fraction   : LREAL;
  END_VAR
  VAR
    cfg : Config;
    e   : Elapsed;

    pt      : TIME := T#500ms;
    warmup  : TIME := T#1m30s;
    fine    : TIME := T#1s500ms250us;
    half    : TIME;
  END_VAR

  cfg.errDelay := SECONDS_TO_TIME(delay_s);
  cfg.pumpOff  := T#2s;

  e(run := run);

  expired    := e.et >= pt;
  elapsed_ms := TIME_TO_DINT(e.et);
  remaining  := TIME_TO_SECONDS(warmup - e.et);
  fraction   := e.et / pt;

  (* Scaling: by a whole count and by a fraction. *)
  half := pt / 2;
  pt   := half * 2;
  warmup := warmup * 1.5;

  IF cfg.errDelay > cfg.pumpOff THEN
    pt := fine;
  END_IF;
END_PROGRAM
