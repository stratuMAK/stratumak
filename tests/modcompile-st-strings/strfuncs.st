{attribute 'summary' := 'Exercises IEC string functions from HAL'}
{attribute 'license' := 'GPL'}

PROGRAM strfuncs
  VAR_INPUT
    (* A runtime count, so the clamping paths run with a value the compiler
       cannot see rather than only with written-out ones. *)
    n : DINT;
  END_VAR
  VAR_OUTPUT
    (* One bit per check.  Each is a comparison against the answer written
       out, so a wrong result is a zero in a named column rather than a
       length that happens to match. *)
    ok : ARRAY [0..15] OF BOOL;
    (* How much of the string a runtime count takes, which is what the
       clamp decides. *)
    cut : DINT;
  END_VAR
  VAR
    s : STRING[16] := 'abcdefg';
  END_VAR

  ok[0]  := LEN(s) = 7;
  ok[1]  := LEFT(s, 3) = 'abc';
  ok[2]  := RIGHT(s, 3) = 'efg';
  ok[3]  := MID(s, 3, 2) = 'bcd';
  ok[4]  := CONCAT('ab', 'cd') = 'abcd';
  (* Variadic: four arguments fold into three binary calls, each with a
     buffer of its own -- which is the case a shared one would break. *)
  ok[5]  := CONCAT('a', 'b', 'c', 'd') = 'abcd';
  ok[6]  := INSERT('abc', 'XY', 1) = 'aXYbc';
  ok[7]  := INSERT('abc', 'XY', 0) = 'XYabc';
  ok[8]  := DELETE('abcde', 2, 2) = 'ade';
  ok[9]  := REPLACE('abcde', 'XY', 2, 2) = 'aXYde';
  ok[10] := FIND('abcabc', 'bc') = 2;
  ok[11] := FIND('abc', 'zz') = 0;
  (* Asking for more than there is gives what there is, and is not a fault:
     LEFT(s, 99) is how people write "up to 99". *)
  ok[12] := LEFT(s, 99) = s;
  ok[13] := MID(s, 3, 99) = '';
  ok[14] := RIGHT(s, 0) = '';
  (* Nested, which is the shape these appear in and the reason each call
     site gets its own buffer. *)
  ok[15] := CONCAT(CONCAT('drive=', LEFT(s, 2)), '!') = 'drive=ab!';

  cut := LEN(LEFT(s, n));
END_PROGRAM
