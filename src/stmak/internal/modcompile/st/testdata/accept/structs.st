{attribute 'summary' := 'Nested structures and indexed field assignment'}
{attribute 'license' := 'GPL'}

(* `pts[i].x := ...` is the lvalue shape the standalone prototype could not
   express at all: its assignment target was a name plus optional indices,
   not an expression. *)

TYPE
  Point : STRUCT
    x : LREAL;
    y : LREAL;
  END_STRUCT;

  Rect : STRUCT
    topLeft     : Point;
    bottomRight : Point;
  END_STRUCT;
END_TYPE

PROGRAM structs
  VAR_INPUT
    px    : LREAL;
    py    : LREAL;
    index : DINT := 0;
  END_VAR
  VAR_OUTPUT
    width  : LREAL;
    height : LREAL;
  END_VAR
  VAR
    cursor : Point;
    bounds : Rect;
    pts    : ARRAY[0..3] OF Point;
    i      : DINT;
  END_VAR

  cursor.x := px;
  cursor.y := py;

  pts[index].x := cursor.x;
  pts[index].y := cursor.y;

  bounds.topLeft.x := pts[0].x;
  bounds.topLeft.y := pts[0].y;
  bounds.bottomRight.x := pts[0].x;
  bounds.bottomRight.y := pts[0].y;

  FOR i := 1 TO 3 DO
    IF pts[i].x < bounds.topLeft.x THEN
      bounds.topLeft.x := pts[i].x;
    END_IF;
    IF pts[i].x > bounds.bottomRight.x THEN
      bounds.bottomRight.x := pts[i].x;
    END_IF;
  END_FOR;

  width  := bounds.bottomRight.x - bounds.topLeft.x;
  height := bounds.bottomRight.y - bounds.topLeft.y;
END_PROGRAM
