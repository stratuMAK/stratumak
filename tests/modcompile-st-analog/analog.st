{attribute 'summary' := 'Drives the analog and semaphore blocks from HAL'}
{attribute 'license' := 'GPL'}

USING standard;

PROGRAM analog
  VAR_INPUT
    x   : LREAL;
    run : BOOL;
    rst : BOOL;
  END_VAR
  VAR_OUTPUT
    integ : LREAL;
    deriv : LREAL;
    over  : BOOL;
    under : BOOL;
    inlim : BOOL;
    busy  : BOOL;
    hyst  : BOOL;
    scaled: LREAL;
    ramped: LREAL;
    pidy  : LREAL;
  END_VAR
  VAR
    ig : INTEGRAL;
    dv : DERIVATIVE;
    la : LIMITALARM;
    sm : SEMA;
    hy : HYSTERESIS;
    lt : LIN_TRAFO;
    rp : RAMP;
    pd : PID;

    (* A fixed CYCLE, not fperiod: the numbers this test asserts have to be
       arithmetic rather than a measurement of how the thread happened to run.
       One second a call makes the integral a running sum and the derivative a
       plain difference per second, both checkable by hand. *)
    dt : TIME := T#1s;
  END_VAR

  ig(RUN := run, R1 := rst, XIN := x, X0 := 0.0, CYCLE := dt);
  integ := ig.XOUT;

  dv(RUN := run, XIN := x, CYCLE := dt);
  deriv := dv.XOUT;

  la(IN := x, HIGH := 3.0, LOW := 1.0);
  over  := la.O;
  under := la.U;
  inlim := la.IL;

  (* Claimed on the first cycle and never released, so BUSY is false once and
     true forever after -- which is the whole contract. *)
  sm(CLAIM := run, RELEASE := rst);
  busy := sm.BUSY;

  (* A Schmitt trigger latches between its thresholds, so what is captured is
     the history of x and not just its present value. *)
  hy(IN := x, HIGH := 10.0, LOW := 2.0);
  hyst := hy.OUT;

  lt(IN := x, IN_MIN := 0.0, IN_MAX := 10.0, OUT_MIN := 0.0, OUT_MAX := 100.0);
  scaled := lt.OUT;

  (* One second a call and a rise limit of 3.0 per second, so the output can
     move by exactly 3.0 each call however far ahead the input runs. *)
  rp(IN := x, ASCEND := 3.0, DESCEND := 3.0, ENABLE := run, CYCLE := dt);
  ramped := rp.OUT;

  (* Proportional only -- TN and TV zero -- so Y is KP times the error,
     clamped.  That is the part of a PID that can be checked by hand. *)
  pd(ACTUAL := x, SET_POINT := 6.0, KP := 2.0, TN := 0.0, TV := 0.0,
     Y_MANUAL := 0.0, Y_OFFSET := 0.0, Y_MIN := -8.0, Y_MAX := 8.0,
     MANUAL := FALSE, RESET := rst, CYCLE := dt);
  pidy := pd.Y;
END_PROGRAM
