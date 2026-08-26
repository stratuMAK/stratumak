(* An action has no interface, so a call to one from outside takes no
   arguments.  Inside the declaring POU `Act(v := x)` is shorthand for setting
   one of the POU's own variables first; from outside the same spelling would
   reach past the instance's interface into its state.

   It is also the spelling an IEC ed.3 METHOD call uses, and a syntax whose
   meaning depended on which of the two the name turned out to be would be a
   poor thing to have built. *)

FUNCTION_BLOCK Motor
  VAR_OUTPUT
    speed : DINT;
  END_VAR

  speed := speed;

ACTION stop:
  speed := 0;
END_ACTION

END_FUNCTION_BLOCK

PROGRAM bad_extaction
  VAR_OUTPUT
    q : DINT;
  END_VAR
  VAR
    m : Motor;
  END_VAR

  m.stop(speed := 5);
  q := m.speed;
END_PROGRAM
