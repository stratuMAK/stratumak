{attribute 'summary' := 'A function block that contains itself'}

(* This must be refused.  A block holding an instance of itself is infinitely
   large before it ever runs, and recursion has no place in a real-time body
   anyway: the stack depth cannot be bounded.
   See docs/dev/MODCOMPILE_ST_FRONTEND.md §4. *)

FUNCTION_BLOCK Nested
  VAR_INPUT
    trigger : BOOL;
  END_VAR
  VAR_OUTPUT
    depth : DINT;
  END_VAR
  VAR
    inner : Nested;
  END_VAR

  inner(trigger := trigger);
  depth := inner.depth + 1;
END_FUNCTION_BLOCK

PROGRAM bad_recursive
  VAR_INPUT
    go : BOOL;
  END_VAR
  VAR_OUTPUT
    level : DINT;
  END_VAR
  VAR
    n : Nested;
  END_VAR

  n(trigger := go);
  level := n.depth;
END_PROGRAM
