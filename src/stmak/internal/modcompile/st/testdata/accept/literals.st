{attribute 'summary' := 'Literal forms and operator precedence'}
{attribute 'license' := 'GPL'}

(* Spelled out because these are where a hand-written parser goes wrong
   quietly: based literals, digit separators, typed literals, and the
   precedence chain from ** down to OR. *)

PROGRAM literals
  VAR_INPUT
    enable : BOOL;
  END_VAR
  VAR_OUTPUT
    mask    : UDINT;
    result  : LREAL;
    decided : BOOL;
  END_VAR
  VAR
    a : DINT := 16#FF;
    b : DINT := 2#1010_1010;
    c : DINT := 8#777;
    d : DINT := 1_000_000;
    e : LREAL := 1.5;
    f : LREAL := 1.0E-3;
    g : LREAL := 2.5E3;
  END_VAR

  mask := UDINT#16#DEAD_BEEF;

  (* ** is right-associative and binds tighter than *; unary minus binds
     looser than ** on its left operand. *)
  result := e + f * g - 2.0 ** 3.0 ** 2.0;
  result := -result;
  result := (e + f) * g;

  (* MOD sits with * and /; comparison is looser than arithmetic; then
     equality, AND, XOR, OR. *)
  decided := a + 1 < b AND c MOD 8 = 7 OR NOT enable;
  decided := decided XOR (a > b);
  decided := enable & decided;
END_PROGRAM
