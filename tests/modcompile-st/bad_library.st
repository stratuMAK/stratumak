(* A library that is not there.  The message lists every directory searched,
   because that is the question being asked. *)

USING nosuchlibrary;

PROGRAM bad_library
  VAR_OUTPUT
    q : BOOL;
  END_VAR

  q := TRUE;
END_PROGRAM
