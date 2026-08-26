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
