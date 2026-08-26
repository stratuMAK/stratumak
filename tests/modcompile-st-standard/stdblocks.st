{attribute 'summary' := 'Drives the standard library blocks from HAL'}
{attribute 'license' := 'GPL'}

USING standard;

PROGRAM stdblocks
  VAR_INPUT
    clk : BOOL;
    aux : BOOL;
  END_VAR
  VAR_OUTPUT
    sr_q    : BOOL;
    rs_q    : BOOL;
    rise    : BOOL;
    fall    : BOOL;
    cu_cv   : DINT;
    cu_q    : BOOL;
    cd_cv   : DINT;
  END_VAR
  VAR
    latch_s : SR;
    latch_r : RS;
    up      : R_TRIG;
    down    : F_TRIG;
    counter : CTU;
    downer  : CTD;
  END_VAR

  (* Set-dominant and reset-dominant, driven from the same two signals so the
     one cycle where both are true tells them apart. *)
  latch_s(S1 := clk, R := aux);
  sr_q := latch_s.Q1;
  latch_r(S := clk, R1 := aux);
  rs_q := latch_r.Q1;

  up(CLK := clk);
  rise := up.Q;
  down(CLK := clk);
  fall := down.Q;

  counter(CU := clk, R := aux, PV := 2);
  cu_cv := counter.CV;
  cu_q  := counter.Q;

  downer(CD := clk, LD := aux, PV := 3);
  cd_cv := downer.CV;
END_PROGRAM
