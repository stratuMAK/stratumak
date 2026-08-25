{attribute 'summary' := 'A loop with no bound the compiler can see'}

(* This must be refused: a WHILE in a HAL function body has to carry an
   explicit max_iterations cap, because it runs in the real-time thread.
   See docs/dev/MODCOMPILE_ST_FRONTEND.md §4. *)

PROGRAM bad_unbounded
  VAR_INPUT
    limit : DINT;
  END_VAR
  VAR_OUTPUT
    done : BOOL;
  END_VAR
  VAR
    n : DINT;
  END_VAR

  WHILE n < limit DO
    n := n + 1;
  END_WHILE;

  done := n >= limit;
END_PROGRAM
