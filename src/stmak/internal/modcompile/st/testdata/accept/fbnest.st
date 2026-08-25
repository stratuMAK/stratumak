{attribute 'summary' := 'Nested and arrayed function block instances'}
{attribute 'license' := 'GPL'}

(* A function block instance nests as a struct inside inst_t, so an FB may
   contain another, and an array of instances is unremarkable -- neither
   would be true if instances carried HAL identities of their own. *)

FUNCTION_BLOCK Accumulator
  VAR_INPUT
    add   : LREAL;
    clear : BOOL;
  END_VAR
  VAR_OUTPUT
    total : LREAL;
  END_VAR

  IF clear THEN
    total := 0.0;
  ELSE
    total := total + add;
  END_IF;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Averager
  VAR_INPUT
    sample : LREAL;
    reset  : BOOL;
  END_VAR
  VAR_OUTPUT
    mean : LREAL;
  END_VAR
  VAR
    acc   : Accumulator;
    count : DINT;
  END_VAR

  IF reset THEN
    count := 0;
  ELSE
    count := count + 1;
  END_IF;

  acc(add := sample, clear := reset);

  IF count > 0 THEN
    mean := acc.total / count;
  ELSE
    mean := 0.0;
  END_IF;
END_FUNCTION_BLOCK

PROGRAM fbnest
  VAR_INPUT
    ch    : ARRAY[0..3] OF LREAL;
    reset : BOOL;
  END_VAR
  VAR_OUTPUT
    mean0 : LREAL;
    mean1 : LREAL;
  END_VAR
  VAR
    avg : ARRAY[0..3] OF Averager;
    i   : DINT;
  END_VAR

  FOR i := 0 TO 3 DO
    avg[i](sample := ch[i], reset := reset);
  END_FOR;

  mean0 := avg[0].mean;
  mean1 := avg[1].mean;
END_PROGRAM
