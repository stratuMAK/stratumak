(* Every variable, pin, parameter and struct field reaches the generated C as
   a bare identifier, so one named for a C keyword would produce code the C
   compiler rejects -- an error against generated source nobody keeps, which is
   the whole thing this frontend exists to prevent. *)

PROGRAM bad_cname
  VAR_OUTPUT
    q : DINT;
  END_VAR
  VAR
    int : DINT;
  END_VAR

  int := 1;
  q := int;
END_PROGRAM
