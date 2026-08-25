{attribute 'summary' := 'An IEC physical address in an AT clause'}

(* This must be refused.  %QX0.0 is a physical address -- memory area, size
   code, offset -- and stratuMAK has no address space to locate a variable in,
   because HAL signals are the addressing scheme.  Saying so beats an
   "unknown direction" that leaves the author guessing.
   See docs/dev/MODCOMPILE_ST_FRONTEND.md §3.1. *)

PROGRAM bad_address
  VAR
    lamp AT %QX0.0 : BOOL;
  END_VAR

  lamp := TRUE;
END_PROGRAM
