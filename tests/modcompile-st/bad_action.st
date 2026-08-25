(* An action calling itself is recursion by another name: the stack depth
   cannot be bounded, so the real-time rules refuse it exactly as they refuse a
   recursive FUNCTION. *)

PROGRAM bad_action
  VAR_OUTPUT
    out : DINT;
  END_VAR

  spin();

ACTION spin:
  out := out + 1;
  spin();
END_ACTION

END_PROGRAM
