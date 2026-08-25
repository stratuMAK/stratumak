{attribute 'summary' := 'Two-input AND gate'}
{attribute 'license' := 'GPL'}
{attribute 'author'  := 'stratuMAK'}

(* The smallest useful component: two inputs, one output, no state.
   The .comp equivalent is src/hal/components/and2.comp. *)

PROGRAM and2
  VAR_INPUT
    {attribute 'doc' := 'First input.'}
    in0 : BOOL;
    {attribute 'doc' := 'Second input.'}
    in1 : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'TRUE when both inputs are TRUE.'}
    out : BOOL;
  END_VAR

  out := in0 AND in1;
END_PROGRAM
