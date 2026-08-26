{attribute 'summary' := 'Drives the RTC block from HAL'}
{attribute 'license' := 'GPL'}

USING standard;

PROGRAM rtcblock
  VAR_INPUT
    en : BOOL;
  END_VAR
  VAR_OUTPUT
    running : BOOL;
    (* Seconds since the preset, not since the epoch: an absolute epoch second
       is 1.7e9 and would lose the milliseconds that matter here to float
       formatting.  The difference is what the block is being asked about. *)
    since : LREAL;
    (* And the absolute value once, as a whole number of seconds, to show the
       preset really is where it started. *)
    at_s : LREAL;
  END_VAR
  VAR
    clock : RTC;
  END_VAR

  clock(EN := en, PDT := DT#2026-08-26-08:30:00);
  running := clock.Q;
  since   := TIME_TO_SECONDS(clock.CDT - DT#2026-08-26-08:30:00);
  at_s    := DT_TO_SECONDS(clock.CDT) - 1787000000.0;
END_PROGRAM
