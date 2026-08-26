(* HAL stores a name in STMAK_HAL_NAME_LEN characters, and truncates anything
   longer.  Two pins differing only past that become one pin on the machine and
   nothing says so, which is why this is refused here instead.

   An array pin is measured at its widest element: what has to fit is
   bad_pinlen.<name>.99, not bad_pinlen.<name>. *)

PROGRAM bad_pinlen
  VAR_OUTPUT
    zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz : ARRAY [0..99] OF DINT;
  END_VAR

  zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz[0] := 1;
END_PROGRAM
