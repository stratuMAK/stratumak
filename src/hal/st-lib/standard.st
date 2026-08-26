{attribute 'library' := 'standard'}
{attribute 'summary' := 'The IEC 61131-3 standard function blocks'}
{attribute 'author'  := 'Sascha Ittner'}
{attribute 'license' := 'LGPL-2.1-or-later WITH stratuMAK-module-exception'}

(* The standard function blocks of IEC 61131-3, written in Structured Text and
   compiled by the same frontend as any component.  Nothing here is a compiler
   builtin.

   What a component uses is generated into its own module and what it does not
   is pruned, so this file costs a consumer nothing per block it ignores.  That
   is what lets one library hold everything rather than making the reader
   choose a subset by hand.

   The bodies are the standard's own, with one deliberate difference: IEC
   declares a counter's clock as `CU : BOOL R_EDGE`, and stratuMAK refuses the
   R_EDGE qualifier.  A qualifier that silently turns a plain input into a
   one-cycle pulse is exactly the kind of thing that is invisible when reading
   a body, so the counters hold an R_TRIG instead and the edge is written out.

   Licensing.  This file is LGPL rather than GPL so that a proprietary module
   may use it.  That needs saying explicitly, because the usual LGPL reasoning
   is about linking against a separate library and nothing here is separate:
   what a component uses is compiled into its own module, which is closer to
   including a header than to linking.  The exception below says so, so that
   the intent does not rest on how anyone reads section 6 afterwards.

   See docs/dev/MODCOMPILE_ST_STDLIB.md, and COPYING in this directory. *)

(* ------------------------------------------------------------------------ *)
(* Bistables                                                                 *)
(* ------------------------------------------------------------------------ *)

{attribute 'doc' := 'Set-dominant latch: with both inputs true, Q1 is true.'}
FUNCTION_BLOCK SR
  VAR_INPUT
    {attribute 'doc' := 'Set. Wins when both are true.'}
    S1 : BOOL;
    {attribute 'doc' := 'Reset.'}
    R  : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The latched state.'}
    Q1 : BOOL;
  END_VAR

  Q1 := S1 OR (NOT R AND Q1);
END_FUNCTION_BLOCK

{attribute 'doc' := 'Reset-dominant latch: with both inputs true, Q1 is false.'}
FUNCTION_BLOCK RS
  VAR_INPUT
    {attribute 'doc' := 'Set.'}
    S  : BOOL;
    {attribute 'doc' := 'Reset. Wins when both are true.'}
    R1 : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The latched state.'}
    Q1 : BOOL;
  END_VAR

  Q1 := NOT R1 AND (S OR Q1);
END_FUNCTION_BLOCK

(* ------------------------------------------------------------------------ *)
(* Edge detection                                                            *)
(* ------------------------------------------------------------------------ *)

{attribute 'doc' := 'Rising edge: Q is true for the one cycle CLK goes true.'}
FUNCTION_BLOCK R_TRIG
  VAR_INPUT
    {attribute 'doc' := 'The signal to watch.'}
    CLK : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True for one cycle on the rising edge of CLK.'}
    Q : BOOL;
  END_VAR
  VAR
    (* IEC names this M.  It holds what CLK was last cycle. *)
    M : BOOL;
  END_VAR

  Q := CLK AND NOT M;
  M := CLK;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Falling edge: Q is true for the one cycle CLK goes false.'}
FUNCTION_BLOCK F_TRIG
  VAR_INPUT
    {attribute 'doc' := 'The signal to watch.'}
    CLK : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True for one cycle on the falling edge of CLK.'}
    Q : BOOL;
  END_VAR
  VAR
    (* Holds NOT CLK, and starts true.  Both matter: an instance loads with
       its members zeroed, so with M false and CLK low the very first cycle
       would compute NOT CLK AND NOT M = TRUE and report a falling edge that
       never happened.  IEC initialises it for exactly that reason. *)
    M : BOOL := TRUE;
  END_VAR

  Q := NOT CLK AND NOT M;
  M := NOT CLK;
END_FUNCTION_BLOCK

(* ------------------------------------------------------------------------ *)
(* Counters                                                                  *)
(* ------------------------------------------------------------------------ *)

(* IEC counts in INT, so the limits are INT's.  Counting past them would wrap
   to the far end of the range, which reads as the counter running backwards. *)
VAR_GLOBAL CONSTANT
  CV_MAX : INT := 32767;
  CV_MIN : INT := -32768;
END_VAR

{attribute 'doc' := 'Count up on each rising edge of CU, until reset.'}
FUNCTION_BLOCK CTU
  VAR_INPUT
    {attribute 'doc' := 'Counts one on each rising edge.'}
    CU : BOOL;
    {attribute 'doc' := 'Clears the count while true.'}
    R  : BOOL;
    {attribute 'doc' := 'The count at which Q becomes true.'}
    PV : INT;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True once the count reaches PV.'}
    Q  : BOOL;
    {attribute 'doc' := 'The count.'}
    CV : INT;
  END_VAR
  VAR
    edge : R_TRIG;
  END_VAR

  edge(CLK := CU);
  IF R THEN
    CV := 0;
  ELSIF edge.Q AND CV < CV_MAX THEN
    CV := CV + 1;
  END_IF;
  Q := CV >= PV;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Count down from a loaded value on each rising edge of CD.'}
FUNCTION_BLOCK CTD
  VAR_INPUT
    {attribute 'doc' := 'Counts one down on each rising edge.'}
    CD : BOOL;
    {attribute 'doc' := 'Loads PV into the count while true.'}
    LD : BOOL;
    {attribute 'doc' := 'The value loaded by LD.'}
    PV : INT;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True once the count reaches zero.'}
    Q  : BOOL;
    {attribute 'doc' := 'The count.'}
    CV : INT;
  END_VAR
  VAR
    edge : R_TRIG;
  END_VAR

  edge(CLK := CD);
  IF LD THEN
    CV := PV;
  ELSIF edge.Q AND CV > CV_MIN THEN
    CV := CV - 1;
  END_IF;
  Q := CV <= 0;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Count both ways, with separate up and down outputs.'}
FUNCTION_BLOCK CTUD
  VAR_INPUT
    {attribute 'doc' := 'Counts one up on each rising edge.'}
    CU : BOOL;
    {attribute 'doc' := 'Counts one down on each rising edge.'}
    CD : BOOL;
    {attribute 'doc' := 'Clears the count while true. Wins over LD.'}
    R  : BOOL;
    {attribute 'doc' := 'Loads PV into the count while true.'}
    LD : BOOL;
    {attribute 'doc' := 'The count at which QU becomes true, and the load value.'}
    PV : INT;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True once the count reaches PV.'}
    QU : BOOL;
    {attribute 'doc' := 'True once the count reaches zero.'}
    QD : BOOL;
    {attribute 'doc' := 'The count.'}
    CV : INT;
  END_VAR
  VAR
    up   : R_TRIG;
    down : R_TRIG;
  END_VAR

  up(CLK := CU);
  down(CLK := CD);

  IF R THEN
    CV := 0;
  ELSIF LD THEN
    CV := PV;
  ELSIF up.Q AND NOT down.Q THEN
    (* Edges on both inputs in one cycle cancel, which is IEC's rule and the
       only answer that does not depend on which was checked first. *)
    IF CV < CV_MAX THEN
      CV := CV + 1;
    END_IF;
  ELSIF down.Q AND NOT up.Q THEN
    IF CV > CV_MIN THEN
      CV := CV - 1;
    END_IF;
  END_IF;

  QU := CV >= PV;
  QD := CV <= 0;
END_FUNCTION_BLOCK

(* ------------------------------------------------------------------------ *)
(* Timers                                                                    *)
(* ------------------------------------------------------------------------ *)

(* These are built on MONOTONIC, the RT clock, and not on fperiod.

   IEC's TON(IN, PT) exposes no cycle input, because its semantics are elapsed
   real time rather than a count of cycles.  A timer that added fperiod each
   cycle would measure cycles times the nominal period, so it would run slow by
   whatever the thread lost to jitter or an overrun, and it would disagree with
   itself if two threads ever drove the same instance.  Reading the clock costs
   one call and has neither problem.

   Each body reads the clock once, into `now`, so that a reading taken and used
   in the same cycle cannot differ from itself by the handful of nanoseconds
   between two calls.  There is no VAR_TEMP here, so `now` is a member; eight
   bytes per instance is the price of the reading being consistent. *)

{attribute 'doc' := 'Delay on: Q follows IN, but only after IN has been true for PT.'}
FUNCTION_BLOCK TON
  VAR_INPUT
    {attribute 'doc' := 'The signal to delay.'}
    IN : BOOL;
    {attribute 'doc' := 'How long IN must stay true before Q follows.'}
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True once IN has been true for PT.'}
    Q  : BOOL;
    {attribute 'doc' := 'How long IN has been true, stopping at PT.'}
    ET : TIME;
  END_VAR
  VAR
    running : BOOL;
    start   : TIME;
    now     : TIME;
  END_VAR

  now := MONOTONIC;

  IF NOT IN THEN
    running := FALSE;
    Q  := FALSE;
    ET := T#0s;
  ELSE
    IF NOT running THEN
      running := TRUE;
      start := now;
    END_IF;
    ET := now - start;
    IF ET >= PT THEN
      (* IEC holds ET at PT rather than letting it run on, so a reader can
         compare it with PT instead of having to remember which is larger. *)
      ET := PT;
      Q := TRUE;
    END_IF;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Delay off: Q follows IN up, and drops PT after IN drops.'}
FUNCTION_BLOCK TOF
  VAR_INPUT
    {attribute 'doc' := 'The signal to hold.'}
    IN : BOOL;
    {attribute 'doc' := 'How long Q stays true after IN goes false.'}
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True while IN is true, and for PT after it goes false.'}
    Q  : BOOL;
    {attribute 'doc' := 'How long IN has been false, stopping at PT.'}
    ET : TIME;
  END_VAR
  VAR
    running : BOOL;
    start   : TIME;
    now     : TIME;
  END_VAR

  now := MONOTONIC;

  IF IN THEN
    running := FALSE;
    Q  := TRUE;
    ET := T#0s;
  ELSIF Q THEN
    (* Only while Q is still true: once it has dropped there is nothing left
       to time, and an instance that has never seen IN starts with Q false and
       so never starts timing at all. *)
    IF NOT running THEN
      running := TRUE;
      start := now;
    END_IF;
    ET := now - start;
    IF ET >= PT THEN
      ET := PT;
      Q := FALSE;
    END_IF;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Pulse: a rising edge of IN makes Q true for exactly PT.'}
FUNCTION_BLOCK TP
  VAR_INPUT
    {attribute 'doc' := 'A rising edge starts the pulse.'}
    IN : BOOL;
    {attribute 'doc' := 'How long the pulse lasts.'}
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True for PT after a rising edge of IN.'}
    Q  : BOOL;
    {attribute 'doc' := 'How far into the pulse, stopping at PT.'}
    ET : TIME;
  END_VAR
  VAR
    edge    : R_TRIG;
    running : BOOL;
    start   : TIME;
    now     : TIME;
  END_VAR

  now := MONOTONIC;
  edge(CLK := IN);

  IF edge.Q AND NOT running THEN
    (* An edge during a pulse is ignored, which is what makes the pulse a
       fixed length rather than a retriggerable one. *)
    running := TRUE;
    start := now;
    Q := TRUE;
  END_IF;

  IF running THEN
    ET := now - start;
    IF ET >= PT THEN
      ET := PT;
      Q := FALSE;
      IF NOT IN THEN
        (* Re-armed only once IN has gone away: holding IN high after a pulse
           must not start another. *)
        running := FALSE;
        ET := T#0s;
      END_IF;
    END_IF;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Retentive delay on: accumulates the time IN is true, until reset.'}
FUNCTION_BLOCK TONR
  VAR_INPUT
    {attribute 'doc' := 'Accumulates elapsed time while true.'}
    IN : BOOL;
    {attribute 'doc' := 'Clears the accumulated time.'}
    R  : BOOL;
    {attribute 'doc' := 'The total at which Q becomes true.'}
    PT : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True once the accumulated time reaches PT.'}
    Q  : BOOL;
    {attribute 'doc' := 'The accumulated time, stopping at PT.'}
    ET : TIME;
  END_VAR
  VAR
    running : BOOL;
    start   : TIME;
    acc     : TIME;
    now     : TIME;
  END_VAR

  now := MONOTONIC;

  IF R THEN
    acc     := T#0s;
    running := FALSE;
    Q       := FALSE;
    ET      := T#0s;
  ELSE
    IF IN THEN
      IF NOT running THEN
        running := TRUE;
        start := now;
      END_IF;
      ET := acc + (now - start);
    ELSE
      IF running THEN
        (* Fold the run that just ended into the total, so the time IN was
           true survives IN going false.  That is what retentive means. *)
        acc := acc + (now - start);
        running := FALSE;
      END_IF;
      ET := acc;
    END_IF;
    IF ET >= PT THEN
      ET := PT;
      Q  := TRUE;
    END_IF;
  END_IF;
END_FUNCTION_BLOCK

(* ------------------------------------------------------------------------ *)
(* Mutual exclusion                                                          *)
(* ------------------------------------------------------------------------ *)

{attribute 'doc' := 'A semaphore: BUSY tells a claimant whether it already belonged to someone.'}
FUNCTION_BLOCK SEMA
  VAR_INPUT
    {attribute 'doc' := 'Ask for the semaphore.'}
    CLAIM   : BOOL;
    {attribute 'doc' := 'Give it back.'}
    RELEASE : BOOL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'True if it was already claimed when CLAIM was made.'}
    BUSY : BOOL;
  END_VAR
  VAR
    X : BOOL;
  END_VAR

  (* BUSY reports the state as it was *before* this claim, which is what makes
     a claimant able to tell whether it won.  Reading it after the claim would
     always say true and tell nobody anything. *)
  BUSY := X;
  IF CLAIM THEN
    X := TRUE;
  ELSIF RELEASE THEN
    BUSY := FALSE;
    X := FALSE;
  END_IF;
END_FUNCTION_BLOCK

(* ------------------------------------------------------------------------ *)
(* Analog                                                                    *)
(*                                                                           *)
(* These take CYCLE as an input rather than reading fperiod, which is IEC's   *)
(* own arrangement and the right one: the caller decides what interval the    *)
(* block is integrating or differentiating over, and passing fperiod is only  *)
(* the common case.  The timers above are different because elapsed real time *)
(* is the whole of what they measure; here the cycle is a coefficient.        *)
(* ------------------------------------------------------------------------ *)

{attribute 'doc' := 'Integrate XIN over time. R1 loads X0; RUN gates accumulation.'}
FUNCTION_BLOCK INTEGRAL
  VAR_INPUT
    {attribute 'doc' := 'Accumulate while true.'}
    RUN   : BOOL;
    {attribute 'doc' := 'Reset to X0 while true. Wins over RUN.'}
    R1    : BOOL;
    {attribute 'doc' := 'The value being integrated, per second.'}
    XIN   : LREAL;
    {attribute 'doc' := 'The value R1 resets to.'}
    X0    : LREAL;
    {attribute 'doc' := 'The interval one call represents. Pass fperiod, converted.'}
    CYCLE : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'False while R1 holds the output at X0.'}
    Q    : BOOL;
    {attribute 'doc' := 'The integral.'}
    XOUT : LREAL;
  END_VAR

  IF R1 THEN
    XOUT := X0;
  ELSIF RUN THEN
    XOUT := XOUT + XIN * TIME_TO_SECONDS(CYCLE);
  END_IF;
  Q := NOT R1;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Differentiate XIN, smoothed over four samples.'}
FUNCTION_BLOCK DERIVATIVE
  VAR_INPUT
    {attribute 'doc' := 'Differentiate while true; false clears the history.'}
    RUN   : BOOL;
    {attribute 'doc' := 'The value being differentiated.'}
    XIN   : LREAL;
    {attribute 'doc' := 'The interval between calls. Pass fperiod, converted.'}
    CYCLE : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The rate of change of XIN, per second.'}
    XOUT : LREAL;
  END_VAR
  VAR
    X1 : LREAL;
    X2 : LREAL;
    X3 : LREAL;
  END_VAR

  (* IEC's four-point form rather than a plain difference of the last two
     samples.  A backward difference amplifies noise by 1/CYCLE, which at a
     1 kHz servo rate means a thousandfold, and the output of this block is
     usually on its way into something that acts on it.

     The coefficients are what make it exact on a ramp: for XIN rising by d
     each call the numerator is 3*3d + d = 10d, over 10*CYCLE, which is d per
     cycle -- the slope, with no scaling left over. *)
  IF NOT RUN THEN
    XOUT := 0.0;
    X1 := XIN;
    X2 := XIN;
    X3 := XIN;
  ELSIF CYCLE <= T#0s THEN
    (* A CYCLE nobody set is a configuration mistake, and dividing by it would
       put an infinity into whatever this feeds.  Zero is not the right answer
       either, but it is the one that does not propagate. *)
    XOUT := 0.0;
  ELSE
    XOUT := (3.0 * (XIN - X3) + X1 - X2) / (10.0 * TIME_TO_SECONDS(CYCLE));
    X3 := X2;
    X2 := X1;
    X1 := XIN;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Report whether IN is above HIGH, below LOW, or between them.'}
FUNCTION_BLOCK LIMITALARM
  VAR_INPUT
    {attribute 'doc' := 'The value to check.'}
    IN   : LREAL;
    {attribute 'doc' := 'The upper limit.'}
    HIGH : LREAL;
    {attribute 'doc' := 'The lower limit.'}
    LOW  : LREAL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'IN is above HIGH.'}
    O  : BOOL;
    {attribute 'doc' := 'IN is below LOW.'}
    U  : BOOL;
    {attribute 'doc' := 'IN is within the limits.'}
    IL : BOOL;
  END_VAR

  O  := IN > HIGH;
  U  := IN < LOW;
  IL := NOT O AND NOT U;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Square wave: OUT is high for TIMEHIGH, low for TIMELOW, while ENABLE.'}
FUNCTION_BLOCK BLINK
  VAR_INPUT
    {attribute 'doc' := 'Run while true; false holds OUT low.'}
    ENABLE   : BOOL;
    {attribute 'doc' := 'How long OUT stays low each period.'}
    TIMELOW  : TIME;
    {attribute 'doc' := 'How long OUT stays high each period.'}
    TIMEHIGH : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The square wave.'}
    OUT : BOOL;
  END_VAR
  VAR
    armed : BOOL;
    start : TIME;
    now   : TIME;
  END_VAR

  now := MONOTONIC;

  IF NOT ENABLE THEN
    OUT   := FALSE;
    armed := FALSE;
  ELSE
    IF NOT armed THEN
      (* Starts high, so that enabling it shows something immediately rather
         than after TIMELOW of nothing. *)
      armed := TRUE;
      start := now;
      OUT   := TRUE;
    ELSIF OUT AND now - start >= TIMEHIGH THEN
      OUT   := FALSE;
      start := now;
    ELSIF NOT OUT AND now - start >= TIMELOW THEN
      OUT   := TRUE;
      start := now;
    END_IF;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Schmitt trigger: on at or above HIGH, off at or below LOW, latched between.'}
FUNCTION_BLOCK HYSTERESIS
  VAR_INPUT
    {attribute 'doc' := 'The value to watch.'}
    IN   : LREAL;
    {attribute 'doc' := 'At or above this, OUT turns on.'}
    HIGH : LREAL;
    {attribute 'doc' := 'At or below this, OUT turns off.'}
    LOW  : LREAL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'Holds its state while IN is between the thresholds.'}
    OUT : BOOL;
  END_VAR

  (* Two explicit thresholds rather than CODESYS's centre-and-width
     (IN1, IN2, HYSTERESE).  The signature differs, so a ported source fails
     to compile rather than running with the polarity inverted -- which is the
     way round to be wrong. *)
  IF IN >= HIGH THEN
    OUT := TRUE;
  ELSIF IN <= LOW THEN
    OUT := FALSE;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Scale IN linearly from one range onto another.'}
FUNCTION_BLOCK LIN_TRAFO
  VAR_INPUT
    {attribute 'doc' := 'The value to scale.'}
    IN      : LREAL;
    {attribute 'doc' := 'Low end of the input range.'}
    IN_MIN  : LREAL;
    {attribute 'doc' := 'High end of the input range.'}
    IN_MAX  : LREAL;
    {attribute 'doc' := 'What IN_MIN maps to.'}
    OUT_MIN : LREAL;
    {attribute 'doc' := 'What IN_MAX maps to.'}
    OUT_MAX : LREAL;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The scaled value.'}
    OUT   : LREAL;
    {attribute 'doc' := 'True when the input range is empty, so nothing can be scaled.'}
    ERROR : BOOL;
  END_VAR

  IF IN_MAX = IN_MIN THEN
    (* An empty input range has no slope.  Saying so and holding the output at
       OUT_MIN beats dividing by zero and feeding an infinity onward. *)
    ERROR := TRUE;
    OUT   := OUT_MIN;
  ELSE
    ERROR := FALSE;
    OUT   := (IN - IN_MIN) * (OUT_MAX - OUT_MIN) / (IN_MAX - IN_MIN) + OUT_MIN;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'Slope limiter: OUT follows IN, but no faster than ASCEND up or DESCEND down.'}
FUNCTION_BLOCK RAMP
  VAR_INPUT
    {attribute 'doc' := 'The value to follow.'}
    IN      : LREAL;
    {attribute 'doc' := 'Fastest rise, per second.'}
    ASCEND  : LREAL;
    {attribute 'doc' := 'Fastest fall, per second, as a positive number.'}
    DESCEND : LREAL;
    {attribute 'doc' := 'False snaps OUT to IN and stops limiting.'}
    ENABLE  : BOOL;
    {attribute 'doc' := 'The interval one call represents. Pass fperiod, converted.'}
    CYCLE   : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The rate-limited value.'}
    OUT  : LREAL;
    {attribute 'doc' := 'True while OUT has not caught up with IN.'}
    BUSY : BOOL;
  END_VAR
  VAR
    step : LREAL;
    dt   : LREAL;
  END_VAR

  IF NOT ENABLE THEN
    OUT  := IN;
    BUSY := FALSE;
  ELSE
    dt := TIME_TO_SECONDS(CYCLE);
    IF IN > OUT THEN
      step := ASCEND * dt;
      IF OUT + step >= IN THEN
        OUT := IN;
      ELSE
        OUT := OUT + step;
      END_IF;
    ELSIF IN < OUT THEN
      step := DESCEND * dt;
      IF OUT - step <= IN THEN
        OUT := IN;
      ELSE
        OUT := OUT - step;
      END_IF;
    END_IF;
    BUSY := OUT <> IN;
  END_IF;
END_FUNCTION_BLOCK

{attribute 'doc' := 'PID controller with reset time, rate time and clamping anti-windup.'}
FUNCTION_BLOCK PID
  VAR_INPUT
    {attribute 'doc' := 'The measurement.'}
    ACTUAL    : LREAL;
    {attribute 'doc' := 'What the measurement should be.'}
    SET_POINT : LREAL;
    {attribute 'doc' := 'Proportional gain.'}
    KP        : LREAL;
    {attribute 'doc' := 'Reset time in seconds. Zero disables the integral term.'}
    TN        : LREAL;
    {attribute 'doc' := 'Rate time in seconds. Zero disables the derivative term.'}
    TV        : LREAL;
    {attribute 'doc' := 'Output used while MANUAL.'}
    Y_MANUAL  : LREAL;
    {attribute 'doc' := 'Added to the output; the value it sits at with no error.'}
    Y_OFFSET  : LREAL;
    {attribute 'doc' := 'Lower output limit.'}
    Y_MIN     : LREAL;
    {attribute 'doc' := 'Upper output limit.'}
    Y_MAX     : LREAL;
    {attribute 'doc' := 'Drive the output from Y_MANUAL instead of the error.'}
    MANUAL    : BOOL;
    {attribute 'doc' := 'Clear the integral and return the output to Y_OFFSET.'}
    RESET     : BOOL;
    {attribute 'doc' := 'The interval one call represents. Pass fperiod, converted.'}
    CYCLE     : TIME;
  END_VAR
  VAR_OUTPUT
    {attribute 'doc' := 'The control output, between Y_MIN and Y_MAX.'}
    Y             : LREAL;
    {attribute 'doc' := 'True while the output is being held at a limit.'}
    LIMITS_ACTIVE : BOOL;
  END_VAR
  VAR
    acc        : LREAL;
    prevActual : LREAL;
    err        : LREAL;
    raw        : LREAL;
    dt         : LREAL;
  END_VAR

  (* The classic industrial parameterisation -- gain, reset time, rate time --
     rather than three independent gains, because that is what a tuning
     procedure and every PLC handbook are written in terms of.  Two choices in
     here are worth knowing about, and neither is universal:

     The derivative acts on the measurement, not on the error.  Differentiating
     the error means a step in SET_POINT arrives at the output as a spike of
     TV/CYCLE times its size, which at a 1 kHz rate is enormous and is felt by
     whatever the output drives.  Following the measurement gives the same
     damping with no kick.

     Anti-windup is by clamping: while the output sits at a limit the integral
     stops accumulating.  Without it a long excursion buries the integral so
     deep that the controller stays saturated well after the error reverses.

     This is not a bit-exact clone of any vendor's PID.  Retune rather than
     assume a set of numbers carries over. *)
  IF RESET THEN
    acc           := 0.0;
    Y             := Y_OFFSET;
    prevActual    := ACTUAL;
    LIMITS_ACTIVE := FALSE;
  ELSIF MANUAL THEN
    Y             := Y_MANUAL;
    prevActual    := ACTUAL;
    LIMITS_ACTIVE := FALSE;
  ELSE
    err := SET_POINT - ACTUAL;
    dt  := TIME_TO_SECONDS(CYCLE);

    IF dt <= 0.0 THEN
      (* A CYCLE nobody set would make both the integral and the derivative
         meaningless; hold instead of computing with it. *)
      LIMITS_ACTIVE := FALSE;
    ELSE
      IF TN > 0.0 THEN
        IF NOT LIMITS_ACTIVE THEN
          acc := acc + err * dt;
        END_IF;
        raw := err + acc / TN;
      ELSE
        acc := 0.0;
        raw := err;
      END_IF;

      IF TV > 0.0 THEN
        raw := raw - TV * (ACTUAL - prevActual) / dt;
      END_IF;
      prevActual := ACTUAL;

      raw := KP * raw + Y_OFFSET;

      IF raw > Y_MAX THEN
        Y             := Y_MAX;
        LIMITS_ACTIVE := TRUE;
      ELSIF raw < Y_MIN THEN
        Y             := Y_MIN;
        LIMITS_ACTIVE := TRUE;
      ELSE
        Y             := raw;
        LIMITS_ACTIVE := FALSE;
      END_IF;
    END_IF;
  END_IF;
END_FUNCTION_BLOCK
