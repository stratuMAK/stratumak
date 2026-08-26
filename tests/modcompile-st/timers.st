{attribute 'library' := 'timers'}
{attribute 'summary' := 'IEC 61131-3 delay timers'}
{attribute 'license' := 'GPL'}

(* A library: TYPE, FUNCTION and FUNCTION_BLOCK declarations and no PROGRAM,
   marked by the 'library' attribute.  What it declares is generated into the
   consumer's own module -- see docs/dev/MODCOMPILE_ST_STDLIB.md §2 -- so a
   call to one of these blocks is a static C function in the same translation
   unit, checked against this declaration and mapped back here by #line. *)

FUNCTION_BLOCK TON
  VAR_INPUT
    IN : BOOL;
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    Q  : BOOL;
    ET : TIME;
  END_VAR

  IF IN THEN
    IF ET < PT THEN
      ET := ET + SECONDS_TO_TIME(fperiod);
    END_IF;
    Q := ET >= PT;
  ELSE
    ET := T#0s;
    Q := FALSE;
  END_IF;
END_FUNCTION_BLOCK

(* Nothing in the tree uses this one.  It must not reach the generated C: a
   block body is a static function and cmods build with -Werror, and a library
   exists precisely to declare more than any one consumer wants. *)
FUNCTION_BLOCK TOF
  VAR_INPUT
    IN : BOOL;
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    Q : BOOL;
  END_VAR

  Q := IN OR PT > T#0s;
END_FUNCTION_BLOCK
