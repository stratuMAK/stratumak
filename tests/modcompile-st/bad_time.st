(* A duration is not a number.  Adding one to it would be adding one what?
   The combinations that have an unambiguous meaning are accepted and the rest
   are refused by name, rather than guessed at. *)

PROGRAM bad_time
  VAR_OUTPUT
    out : BOOL;
  END_VAR
  VAR
    pt : TIME := T#500ms;
  END_VAR

  pt := pt + 1;
  out := pt > T#0s;
END_PROGRAM
