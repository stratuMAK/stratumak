{attribute 'summary' := 'Uses the timers library'}
{attribute 'license' := 'GPL'}

USING timers;

PROGRAM uselib
  VAR_INPUT
    start : BOOL;
  END_VAR
  VAR_OUTPUT
    running : BOOL;
    elapsed : LREAL;
  END_VAR
  VAR
    delay : TON;
  END_VAR

  delay(IN := start, PT := T#500ms);
  running := delay.Q;
  elapsed := TIME_TO_SECONDS(delay.ET);
END_PROGRAM
