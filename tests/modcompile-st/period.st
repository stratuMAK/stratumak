{attribute 'summary' := 'fperiod, in each kind of body that may use it'}
{attribute 'license' := 'GPL'}

(* fperiod is the period of the thread this body is running in.  The backend
   reaches it two ways -- from the HAL function's own argument where that is in
   scope, and from the RT task otherwise -- and they are the same number,
   because a HAL function is invoked by its own thread.  See
   docs/dev/MODCOMPILE_ST_STDLIB.md §4. *)

FUNCTION_BLOCK Integrator
  VAR_INPUT
    rate : LREAL;
  END_VAR
  VAR_OUTPUT
    total : LREAL;
  END_VAR

  (* A block body is its own C function and was never handed the argument. *)
  IF rate = 0.0 THEN
    reset();
  ELSE
    total := total + rate * fperiod;
  END_IF;

ACTION reset:
  total := 0.0;
END_ACTION

END_FUNCTION_BLOCK

PROGRAM period
  VAR_INPUT
    enable : BOOL;
    rate   : LREAL;
  END_VAR
  VAR_OUTPUT
    total   : LREAL;
    elapsed : LREAL;
  END_VAR
  VAR
    acc : Integrator;
  END_VAR

  IF enable THEN
    (* An exported body has the argument. *)
    elapsed := elapsed + fperiod;
    acc(rate := rate);
  ELSE
    stop();
  END_IF;
  total := acc.total;

(* An action is its own C function too, even inside a PROGRAM. *)
ACTION stop:
  elapsed := 0.0;
END_ACTION

END_PROGRAM
