{attribute 'summary' := 'Writes every kind of value out as text'}
{attribute 'license' := 'GPL'}

PROGRAM tostring
  VAR_INPUT
    (* A number that arrives at run time, so the writers are not handed a
       value the compiler could have folded. *)
    v : DINT;
  END_VAR
  VAR_OUTPUT
    (* The first check that did not hold, counting from one, or zero. One
       pin rather than one per check, so the list can grow without the HAL
       file growing with it. *)
    first : DINT;
    (* And how many failed altogether, which says whether one thing broke
       or the whole family did. *)
    nfail : DINT;
    (* The streamed number written out, measured by its length, so that at
       least one conversion is of something that varies. *)
    vlen : DINT;
  END_VAR
  VAR
    ok : ARRAY [0..31] OF BOOL;
    i  : INT;
    stamp : DATE_AND_TIME := DT#2026-08-26-08:30:00;
    msg : STRING[64];
  END_VAR

  (* A BOOL is written as the literal it is, upper case. *)
  ok[0]  := BOOL_TO_STRING(TRUE) = 'TRUE';
  ok[1]  := BOOL_TO_STRING(FALSE) = 'FALSE';

  (* The ends of each integer range, where a naive negation overflows. *)
  ok[2]  := SINT_TO_STRING(-128) = '-128';
  ok[3]  := INT_TO_STRING(-32768) = '-32768';
  ok[4]  := DINT_TO_STRING(-2147483648) = '-2147483648';
  (* The most negative LINT, which a naive negation turns into itself.
     Written as a subtraction because the magnitude on its own is one past
     what an int64 literal holds. *)
  ok[5]  := LINT_TO_STRING(-9223372036854775807 - 1) = '-9223372036854775808';
  ok[6]  := USINT_TO_STRING(255) = '255';
  ok[7]  := UDINT_TO_STRING(4294967295) = '4294967295';
  ok[8]  := ULINT_TO_STRING(9223372036854775807) = '9223372036854775807';
  ok[9]  := INT_TO_STRING(0) = '0';

  (* Six decimals, with the trailing zeros dropped -- so a whole number has
     no point after it and a half is a half. *)
  ok[10] := LREAL_TO_STRING(1.5) = '1.5';
  ok[11] := LREAL_TO_STRING(2.0) = '2';
  ok[12] := LREAL_TO_STRING(-0.5) = '-0.5';
  ok[13] := LREAL_TO_STRING(-12.25) = '-12.25';
  ok[14] := REAL_TO_STRING(0.0) = '0';

  (* A duration is written as the T# literal it is, largest unit first and
     the empty ones left out. *)
  ok[15] := TIME_TO_STRING(T#1m30s) = 'T#1m30s';
  ok[16] := TIME_TO_STRING(T#0s) = 'T#0s';
  ok[17] := TIME_TO_STRING(T#1s500ms) = 'T#1s500ms';
  ok[18] := TIME_TO_STRING(T#1d2h3m4s5ms) = 'T#1d2h3m4s5ms';

  (* And a point in time as its literal, which is the round trip that says
     the calendar arithmetic is right. *)
  ok[19] := DT_TO_STRING(stamp) = 'DT#2026-08-26-08:30:00';
  ok[20] := DATE_TO_STRING(DT_TO_DATE(stamp)) = 'D#2026-08-26';
  ok[21] := TOD_TO_STRING(DT_TO_TOD(stamp)) = 'TOD#08:30:00';
  ok[22] := DT_TO_STRING(DT#1970-01-01-00:00:00) = 'DT#1970-01-01-00:00:00';
  (* Before the epoch, where the day is a negative number and a remainder
     that keeps its sign would put the date a day out. *)
  ok[23] := DT_TO_STRING(DT#1969-07-20-20:17:00) = 'DT#1969-07-20-20:17:00';
  ok[24] := DT_TO_STRING(DT#1969-12-31-23:59:59) = 'DT#1969-12-31-23:59:59';
  (* The leap rules: a leap year, a century that is not one, and a century
     that is. *)
  ok[25] := DATE_TO_STRING(D#2024-02-29) = 'D#2024-02-29';
  ok[26] := DATE_TO_STRING(D#1900-03-01) = 'D#1900-03-01';
  ok[27] := DATE_TO_STRING(D#2000-02-29) = 'D#2000-02-29';
  (* Both ends of what an int64 of nanoseconds reaches. *)
  ok[28] := DATE_TO_STRING(D#1678-01-01) = 'D#1678-01-01';
  ok[29] := DATE_TO_STRING(D#2261-12-31) = 'D#2261-12-31';
  (* A fraction of a second is written only when there is one. *)
  ok[30] := TOD_TO_STRING(TOD#08:30:00.5) = 'TOD#08:30:00.500000000';

  (* Nested in a CONCAT, which is what these are for. *)
  ok[31] := CONCAT('at ', DT_TO_STRING(stamp)) = 'at DT#2026-08-26-08:30:00';

  first := 0;
  nfail := 0;
  FOR i := 0 TO 31 DO
    IF NOT ok[i] THEN
      nfail := nfail + 1;
      IF first = 0 THEN
        first := i + 1;
      END_IF
    END_IF
  END_FOR

  msg  := DINT_TO_STRING(v);
  vlen := LEN(msg);
END_PROGRAM
