{attribute 'summary' := 'Structured Text twins of and2, or2, not and mux2'}
{attribute 'license' := 'GPL'}

(* Each PROGRAM here reimplements one shipped .comp component.  The test nets
   the same inputs into both and captures both outputs, so a difference between
   the two frontends shows up as a diff rather than as a surprise on a machine.

   The bodies are deliberately written the way the .comp ones are, so what is
   being compared is the two toolchains and not two algorithms. *)

{attribute 'doc' := 'Twin of and2: out is TRUE when both inputs are.'}
PROGRAM and2
  VAR_INPUT
    in0 : BOOL;
    in1 : BOOL;
  END_VAR
  VAR_OUTPUT
    out : BOOL;
  END_VAR

  out := in0 AND in1;
END_PROGRAM

{attribute 'doc' := 'Twin of or2: out is TRUE when either input is.'}
PROGRAM or2
  VAR_INPUT
    or_in0 : BOOL;
    or_in1 : BOOL;
  END_VAR
  VAR_OUTPUT
    or_out : BOOL;
  END_VAR

  or_out := or_in0 OR or_in1;
END_PROGRAM

{attribute 'doc' := 'Twin of not: out is the inverse of in.'}
PROGRAM inv
  VAR_INPUT
    inv_in : BOOL;
  END_VAR
  VAR_OUTPUT
    inv_out : BOOL;
  END_VAR

  inv_out := NOT inv_in;
END_PROGRAM

{attribute 'doc' := 'Twin of mux2: out follows in0 or in1 according to sel.'}
PROGRAM mux2
  VAR_INPUT
    sel : BOOL;
    mux_in0 : LREAL;
    mux_in1 : LREAL;
  END_VAR
  VAR_OUTPUT
    mux_out : LREAL;
  END_VAR

  IF sel THEN
    mux_out := mux_in1;
  ELSE
    mux_out := mux_in0;
  END_IF;
END_PROGRAM
