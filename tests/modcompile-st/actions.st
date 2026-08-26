{attribute 'summary' := 'Named pieces of a body, shared with it'}
{attribute 'license' := 'GPL'}

(* An ACTION is a named piece of a POU's body.  It shares every variable of the
   POU and has none of its own, which is what makes it different from a
   FUNCTION_BLOCK: nothing has to be passed in, and nothing has its own state.

   The alternative for splitting a long cyclic body is a function block, which
   changes the state layout and forces every touched variable through an
   interface, or a FUNCTION, which cannot see the POU's variables at all. *)

FUNCTION_BLOCK Counter
  VAR_INPUT
    step  : DINT;
    reset : BOOL;
  END_VAR
  VAR_OUTPUT
    total : DINT;
  END_VAR
  VAR
    overflows : DINT;
  END_VAR

  IF reset THEN
    clear();
  ELSE
    accumulate();
  END_IF;

(* An action of a function block reaches the instance, so its signature is the
   block body's exactly. *)
ACTION clear:
  total := 0;
  overflows := 0;
END_ACTION

ACTION accumulate:
  total := total + step;
  IF total > 1000 THEN
    overflows := overflows + 1;
    clear();
  END_IF;
END_ACTION

END_FUNCTION_BLOCK

{attribute 'doc' := 'Runs the counter and reports on it.'}
PROGRAM actions
  VAR_INPUT
    enable : BOOL;
    amount : DINT := 1;
  END_VAR
  VAR_OUTPUT
    count   : DINT;
    running : BOOL;
    idle    : BOOL;
  END_VAR
  VAR
    c        : Counter;
    selected : DINT;
  END_VAR

  (* An action of an instance, called from outside the block that declares
     it: `motor.stop()`.  It shares that instance's variables and takes no
     arguments -- there is no interface to pass anything through. *)
  c.clear();

  IF enable THEN
    (* An action of a PROGRAM sees the pin snapshot, so it may read and write
       pins exactly as the body does. *)
    advance(selected := amount);
  ELSE
    stop();
  END_IF;

  report();

ACTION advance:
  c(step := selected, reset := FALSE);
  count := c.total;
  running := TRUE;
END_ACTION

ACTION stop:
  c(step := 0, reset := TRUE);
  count := 0;
  running := FALSE;
END_ACTION

(* This one touches no pin at all, only instance state. *)
ACTION report:
  idle := NOT running;
END_ACTION

END_PROGRAM
