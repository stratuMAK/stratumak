{attribute 'summary' := 'String functions in every kind of body'}
{attribute 'license' := 'GPL'}

FUNCTION_BLOCK Tagger
  VAR_INPUT
    go : BOOL;
  END_VAR
  VAR_OUTPUT
    tag : STRING[32];
  END_VAR
  VAR
    (* A STRING member with an initialiser: a char array, so the block's
       initialiser has to copy it rather than assign it. *)
    base : STRING[8] := 'ax';
  END_VAR

  IF go THEN
    build();
  END_IF

  (* An action and a method each become their own C function, so each needs
     its own buffers -- and a helper either of them calls has to be defined
     in the file whether or not the block's own body calls one. *)
  ACTION build:
    tag := CONCAT(base, '-run');
  END_ACTION

  METHOD suffix : INT
    VAR_INPUT
      extra : STRING[8];
    END_VAR
    tag := CONCAT(base, extra);
    suffix := LEN(tag);
  END_METHOD
END_FUNCTION_BLOCK

FUNCTION shorten : INT
  VAR_INPUT
    s : STRING[32];
  END_VAR
  VAR
    (* A STRING local of a function: declared with its length, and
       initialised at the declaration, which is the one place C takes a
       literal for an array. *)
    cut : STRING[16] := 'none';
  END_VAR
  cut := LEFT(s, 4);
  shorten := LEN(cut);
END_FUNCTION

PROGRAM strings
  VAR_OUTPUT
    n : INT;
  END_VAR
  VAR
    t : Tagger;
    (* A STRING has no HAL representation, so it stays inside the
       component -- which is where a message being assembled belongs. *)
    label : STRING[64];
  END_VAR

  t(go := TRUE);
  n := t.suffix(extra := '!') + shorten(t.tag);
  (* Nested, which is the shape these appear in: each call site gets a
     buffer of its own, so the inner one is not overwritten while the outer
     one is still reading it. *)
  label := CONCAT(CONCAT('tag=', t.tag), '!');
END_PROGRAM
