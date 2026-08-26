{attribute 'summary' := 'Provides the home interface from Structured Text'}
{attribute 'license' := 'GPL'}

(* A GMI provider written in ST.  The interface is IEC ed.3's IMPLEMENTS, and
   its functions are METHODs: every GMI callback already takes a void *ctx
   first and the registration sets that to the component instance, so a method
   on a block is the shape the callback has, minus the plumbing.

   See docs/dev/MODCOMPILE_ST_STDLIB.md §7a. *)

{attribute 'gmi_provide' := 'home'}

FUNCTION_BLOCK Homing IMPLEMENTS home
  VAR_OUTPUT
    (* Made visible on pins by the PROGRAM below, so the test can watch what
       the consumer's calls did. *)
    homing_flag : DINT;
    homed_flag  : DINT;
    calls       : DINT;
    last_flags  : DINT;
  END_VAR

  (* The block's own body runs once a cycle like any other; the interface is
     driven by whoever consumes it, not from here. *)
  homing_flag := homing_flag;

METHOD init : DINT
  VAR_INPUT
    comp_id      : DINT;
    servo_period : LREAL;
  END_VAR
  homing_flag := 0;
  homed_flag  := 0;
  init := 0;
END_METHOD

METHOD set_params : DINT
  VAR_INPUT
    offset          : LREAL;
    home            : LREAL;
    home_final_vel  : LREAL;
    home_search_vel : LREAL;
    home_latch_vel  : LREAL;
    home_flags      : DINT;
    home_sequence   : DINT;
    volatile_home   : DINT;
  END_VAR
  last_flags := home_flags;
  set_params := 0;
END_METHOD

METHOD update_params : DINT
  VAR_INPUT
    home_offset   : LREAL;
    home_home     : LREAL;
    home_sequence : DINT;
  END_VAR
  update_params := 0;
END_METHOD

METHOD read_in_pins : DINT
  calls := calls + 1;
  read_in_pins := 0;
END_METHOD

METHOD do_homing : DINT
  (* One call moves it on: not homing yet, then homing, then homed. *)
  IF homed_flag = 0 THEN
    IF homing_flag = 0 THEN
      homing_flag := 1;
    ELSE
      homing_flag := 0;
      homed_flag  := 1;
    END_IF;
  END_IF;
  do_homing := 0;
END_METHOD

METHOD write_out_pins : DINT
  write_out_pins := 0;
END_METHOD

METHOD do_home : DINT
  homed_flag  := 0;
  homing_flag := 1;
  do_home := 0;
END_METHOD

METHOD do_cancel : DINT
  homing_flag := 0;
  do_cancel := 0;
END_METHOD

METHOD set_unhomed : DINT
  VAR_INPUT
    (* MotionState is an enumeration in the interface, which is an integer
       with names on it; ST sees the integer and the generated thunk keeps the
       C spelling so the callback still matches. *)
    motstate : DINT;
  END_VAR
  homed_flag := 0;
  set_unhomed := 0;
END_METHOD

METHOD get_homing : DINT
  get_homing := homing_flag;
END_METHOD

METHOD get_homed : DINT
  get_homed := homed_flag;
END_METHOD

METHOD get_index_enable : DINT
  get_index_enable := 0;
END_METHOD

METHOD get_needs_unlock_first : DINT
  get_needs_unlock_first := 0;
END_METHOD

METHOD get_is_idle : DINT
  get_is_idle := 1;
END_METHOD

METHOD get_at_index_search_wait : DINT
  get_at_index_search_wait := 0;
END_METHOD

METHOD get_at_final_move_wait : DINT
  get_at_final_move_wait := 0;
END_METHOD

METHOD do_final_move : DINT
  do_final_move := 0;
END_METHOD

END_FUNCTION_BLOCK

PROGRAM gmiprovider
  VAR_OUTPUT
    is_homing : DINT;
    is_homed  : DINT;
    calls     : DINT;
    flags     : DINT;
  END_VAR
  VAR
    impl : Homing;
  END_VAR

  is_homing := impl.homing_flag;
  is_homed  := impl.homed_flag;
  calls     := impl.calls;
  flags     := impl.last_flags;
END_PROGRAM
