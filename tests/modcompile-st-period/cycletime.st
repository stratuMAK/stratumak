{attribute 'summary' := 'Report the cycle time each body is running at'}
{attribute 'license' := 'GPL'}

(* fperiod is the period of the thread this body is running in.  A
   FUNCTION_BLOCK body is its own C function and was never handed the HAL
   function's period argument, so it asks the RT task -- and this component
   exists to prove that the answer really is the calling thread's period, by
   running the same block from two threads of different rates.

   See docs/dev/MODCOMPILE_ST_STDLIB.md §4. *)

FUNCTION_BLOCK Tick
  VAR
    dt AT %Q : LREAL;
  END_VAR

  dt := fperiod;
END_FUNCTION_BLOCK

PROGRAM quick
  VAR
    q : Tick;
  END_VAR

  q();
END_PROGRAM

PROGRAM slow
  VAR
    s : Tick;
  END_VAR

  s();
END_PROGRAM
