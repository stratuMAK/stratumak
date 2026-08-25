{attribute 'summary' := 'Explicit pin mapping with AT'}
{attribute 'license' := 'GPL'}

(* A function block that publishes one of its own values to HAL.  It is not a
   VAR_INPUT, so nothing at a call site writes it -- the direction in the AT
   clause is the whole story, and there is no second writer to conflict with. *)

FUNCTION_BLOCK Axis
  VAR_INPUT
    cmd : LREAL;
  END_VAR
  VAR
    pos AT %Q.position : LREAL;
    err AT %Q.error    : LREAL;
    home AT %I.at_home : BOOL;
  END_VAR

  IF home THEN
    pos := 0.0;
  ELSE
    pos := pos + (cmd - pos) * 0.1;
  END_IF;
  err := cmd - pos;
END_FUNCTION_BLOCK

PROGRAM atmap
  VAR
    (* A plain VAR mapped explicitly, and renamed on the way out. *)
    enable  AT %I         : BOOL;
    running AT %Q.active  : BOOL;
    feed    AT %IO        : LREAL;

    x    : Axis;
    axes : ARRAY[0..2] OF Axis;
    i    : DINT;
  END_VAR

  running := enable;
  x(cmd := feed);
  FOR i := 0 TO 2 DO
    axes[i](cmd := feed);
  END_FOR;
END_PROGRAM
