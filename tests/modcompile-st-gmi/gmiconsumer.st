{attribute 'summary' := 'Consumes the home interface from Structured Text'}
{attribute 'license' := 'GPL'}

(* A consumed GMI interface reads as an instance with methods, because that is
   what it is: a table of functions and a context.  `home.do_homing()` is the
   same spelling as a call on any function block, and it resolves through the
   same path -- the difference is that the callee lives in another module. *)

{attribute 'gmi_consume' := 'home from gmiprovider'}

USING standard;

PROGRAM gmiconsumer
  VAR_INPUT
    start  : BOOL;
    step   : BOOL;
    cancel : BOOL;
  END_VAR
  VAR_OUTPUT
    homing : DINT;
    homed  : DINT;
    idle   : DINT;
  END_VAR
  VAR
    rise_start  : R_TRIG;
    rise_step   : R_TRIG;
    rise_cancel : R_TRIG;
  END_VAR

  (* Edges, so one press is one call across the interface. *)
  rise_start(CLK := start);
  rise_step(CLK := step);
  rise_cancel(CLK := cancel);

  IF rise_start.Q THEN
    home.do_home();
  END_IF;
  IF rise_step.Q THEN
    home.do_homing();
  END_IF;
  IF rise_cancel.Q THEN
    home.do_cancel();
  END_IF;

  (* Read back every cycle, which also exercises the call that returns a
     value being used as one. *)
  home.read_in_pins();
  homing := home.get_homing();
  homed  := home.get_homed();
  idle   := home.get_is_idle();
END_PROGRAM
