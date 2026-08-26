{attribute 'summary' := 'Drives the standard library timers from HAL'}
{attribute 'license' := 'GPL'}

USING standard;

PROGRAM timers
  VAR_INPUT
    go : BOOL;
  END_VAR
  VAR_OUTPUT
    ton_q  : BOOL;
    tof_q  : BOOL;
    tp_q   : BOOL;
    tonr_q : BOOL;
    ton_et : LREAL;
  END_VAR
  VAR
    on    : TON;
    off   : TOF;
    pulse : TP;
    ret   : TONR;
  END_VAR

  on(IN := go, PT := T#5ms);
  ton_q  := on.Q;
  ton_et := TIME_TO_SECONDS(on.ET);

  off(IN := go, PT := T#5ms);
  tof_q := off.Q;

  pulse(IN := go, PT := T#5ms);
  tp_q := pulse.Q;

  (* Retentive, and never reset, so what it accumulates has to survive `go`
     going away again. *)
  ret(IN := go, R := FALSE, PT := T#10ms);
  tonr_q := ret.Q;
END_PROGRAM
