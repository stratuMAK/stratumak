(* A stateless FUNCTION that depended on the rate of whatever thread called it
   would not be stateless, so fperiod is not available in one.  IEC's own
   analog blocks take CYCLE as an explicit input for the same reason. *)

FUNCTION scale : LREAL
  VAR_INPUT
    v : LREAL;
  END_VAR

  scale := v * fperiod;
END_FUNCTION

PROGRAM bad_period
  VAR_OUTPUT
    out : LREAL;
  END_VAR

  out := scale(1.0);
END_PROGRAM
