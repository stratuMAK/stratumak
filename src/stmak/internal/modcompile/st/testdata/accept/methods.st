{attribute 'summary' := 'Methods on a function block instance'}
{attribute 'license' := 'GPL'}

(* A METHOD is an operation on an instance: it has its own interface and its
   own locals, and it can reach the instance's variables.  That is what makes
   it different from an ACTION, which shares the POU's variables and has no
   interface at all -- and why both can share the `inst.name()` call syntax
   without either having to know about the other.

   See docs/dev/MODCOMPILE_ST_STDLIB.md §7a. *)

FUNCTION_BLOCK Accumulator
  VAR_OUTPUT
    total : DINT;
  END_VAR
  VAR
    calls : DINT;
  END_VAR

  calls := calls + 1;

{attribute 'doc' := 'Add a weighted value and report the new total.'}
METHOD add : DINT
  VAR_INPUT
    n      : DINT;
    weight : DINT;
  END_VAR
  VAR
    (* A method's own local, which does not persist between calls -- that is
       what the block's VAR is for. *)
    scaled : DINT;
  END_VAR

  scaled := n * weight;
  total  := total + scaled;
  add    := total;
END_METHOD

{attribute 'doc' := 'Start again.'}
METHOD clear
  (* No result type at all: a method that only acts on the instance returns
     nothing, and writing `: VOID` for that would need a type ST does not
     have. *)
  total := 0;
END_METHOD

(* An action beside the methods, sharing the block's variables and taking no
   interface of its own. *)
ACTION note:
  calls := calls + 1;
END_ACTION

END_FUNCTION_BLOCK

PROGRAM methods
  VAR_INPUT
    reset : BOOL;
  END_VAR
  VAR_OUTPUT
    running : DINT;
  END_VAR
  VAR
    acc : Accumulator;
  END_VAR

  acc();

  (* As an expression, for the result... *)
  running := acc.add(n := 5, weight := 2);

  (* ...and as a statement, where there is none to use. *)
  IF reset THEN
    acc.clear();
  END_IF;

  (* The same spelling reaches an action, which is the point of §7a. *)
  acc.note();
END_PROGRAM
