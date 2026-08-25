{attribute 'summary' := 'Bounded iteration'}
{attribute 'license' := 'GPL'}

(* Every loop in a HAL function body carries a bound the compiler can see.
   FOR is bounded by construction.  WHILE and REPEAT are not, so each needs
   an explicit max_iterations cap -- without one, sema rejects the statement.
   See docs/dev/MODCOMPILE_ST_FRONTEND.md §4. *)

PROGRAM loops
  VAR_INPUT
    target    : LREAL;
    tolerance : LREAL := 0.001;
  END_VAR
  VAR_OUTPUT
    estimate   : LREAL;
    iterations : DINT;
  END_VAR
  VAR
    guess : LREAL;
    i     : DINT;
  END_VAR

  guess := target / 2.0;
  iterations := 0;

  (* Newton-Raphson for a square root: iterative settling is a real pattern,
     and a bounded WHILE says so more honestly than a FOR with a break. *)
  {attribute 'max_iterations' := '50'}
  WHILE guess * guess - target > tolerance DO
    guess := (guess + target / guess) / 2.0;
    iterations := iterations + 1;
  END_WHILE;

  {attribute 'max_iterations' := '10'}
  REPEAT
    iterations := iterations + 1;
  UNTIL iterations > 5
  END_REPEAT;

  FOR i := 0 TO 9 DO
    IF i > 4 THEN
      EXIT;
    END_IF;
    iterations := iterations + 1;
  END_FOR;

  estimate := guess;
END_PROGRAM
