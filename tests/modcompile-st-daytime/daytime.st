{attribute 'summary' := 'Splits a point in time into its day and its time of day'}
{attribute 'license' := 'GPL'}

PROGRAM daytime
  VAR_INPUT
    (* Seconds since 1970, signed: the days before the epoch are the ones
       where a remainder takes the sign of its left operand, and getting them
       wrong is the whole reason the split has helpers of its own. *)
    epoch : DINT;
  END_VAR
  VAR_OUTPUT
    (* The midnight of that day, and the position within it.  Both as
       seconds, because a day before 1970 is a negative one and does not fit
       the unsigned form. *)
    day_s : LREAL;
    tod_s : LREAL;
    (* Whether that position falls inside a shift, which is what a time of
       day is usually asked. *)
    at_work : BOOL;
    (* And how far the day is from a fixed one, in days -- the subtraction
       that a day supports and a time of day does not. *)
    days : LREAL;
  END_VAR
  VAR
    stamp : DATE_AND_TIME;
    day   : DATE;
    clock : TIME_OF_DAY;
    wide  : LINT;
  END_VAR

  wide  := epoch;
  stamp := LINT_TO_DT(wide);
  day   := DT_TO_DATE(stamp);
  clock := DT_TO_TOD(stamp);

  day_s   := DATE_TO_SECONDS(day);
  tod_s   := TOD_TO_SECONDS(clock);
  at_work := clock >= TOD#08:00:00 AND clock < TOD#17:00:00;
  days    := TIME_TO_SECONDS(day - D#2026-01-01) / 86400.0;
END_PROGRAM
