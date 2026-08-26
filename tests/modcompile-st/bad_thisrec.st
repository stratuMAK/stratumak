(* Until a method could reach a sibling, methods could not recurse at all:
   reaching one needed THIS, and reaching another block's method needed
   containment, which is refused.  Both routes exist now, so the cycle check
   has to see them -- the real-time stack is fixed and there is no useful way
   to bound the depth. *)

FUNCTION_BLOCK Loop
  VAR_OUTPUT
    o : DINT;
  END_VAR

  o := a();

METHOD a : DINT
  a := b();
END_METHOD

METHOD b : DINT
  b := THIS^.a();
END_METHOD

END_FUNCTION_BLOCK

PROGRAM bad_thisrec
  VAR_OUTPUT
    q : DINT;
  END_VAR
  VAR
    l : Loop;
  END_VAR

  l();
  q := l.o;
END_PROGRAM
