(* An rw parameter is HAL's to set and the component's to read, which is the
   same division an input pin has -- so writing one is refused for the same
   reason.  A component that wants to clamp a tunable reads it and keeps the
   clamped value in a VAR. *)

PROGRAM bad_param
  VAR_OUTPUT
    out : LREAL;
  END_VAR
  VAR
    gain AT %MRW : LREAL := 1.0;
  END_VAR

  IF gain > 10.0 THEN
    gain := 10.0;
  END_IF;
  out := gain;
END_PROGRAM
