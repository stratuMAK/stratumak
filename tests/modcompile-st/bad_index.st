{attribute 'summary' := 'A constant subscript outside the declared bounds'}

(* An index the compiler can see is a mistake, not something to clamp: this
   must be refused at compile time.  A subscript it cannot see is clamped at
   run time instead -- see arrays.st. *)

PROGRAM bad_index
  VAR_INPUT
    value : DINT;
  END_VAR
  VAR_OUTPUT
    out : DINT;
  END_VAR
  VAR
    slots : ARRAY[1..4] OF DINT;
  END_VAR

  slots[5] := value;
  out := slots[1];
END_PROGRAM
