{attribute 'summary' := 'Three-state sequencer with a fault path'}
{attribute 'license' := 'GPL'}

(* An enumeration used as a state variable, with CASE arms labelled by
   constant name rather than by number.  Resolving those names is exactly
   what the standalone prototype could not do. *)

TYPE
  State : (IDLE, RUNNING, FAULTED);
END_TYPE

PROGRAM statemachine
  VAR_INPUT
    start   : BOOL;
    stop    : BOOL;
    fault   : BOOL;
    reset   : BOOL;
  END_VAR
  VAR_OUTPUT
    is_running : BOOL;
    alarm   : BOOL;
  END_VAR
  VAR
    state : State := IDLE;
  END_VAR

  IF fault THEN
    state := FAULTED;
  END_IF;

  CASE state OF
    IDLE:
      IF start AND NOT stop THEN
        state := RUNNING;
      END_IF;
    RUNNING:
      IF stop THEN
        state := IDLE;
      END_IF;
    FAULTED:
      IF reset AND NOT fault THEN
        state := IDLE;
      END_IF;
  ELSE
    state := FAULTED;
  END_CASE;

  is_running := state = RUNNING;
  alarm   := state = FAULTED;
END_PROGRAM
