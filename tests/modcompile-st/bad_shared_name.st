{attribute 'summary' := 'Two programs claiming one name'}

(* This must be refused.  The PROGRAMs of one file share a single instance and
   a single HAL component, so two of them cannot both declare a pin called
   `ready` -- the second registration would collide at load time. *)

PROGRAM read
  VAR_INPUT
    raw : DINT;
  END_VAR
  VAR_OUTPUT
    ready : BOOL;
  END_VAR

  ready := raw > 0;
END_PROGRAM

PROGRAM write
  VAR_INPUT
    gain : DINT;
  END_VAR
  VAR_OUTPUT
    ready : BOOL;
  END_VAR

  ready := gain > 0;
END_PROGRAM
