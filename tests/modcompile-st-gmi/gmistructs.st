{attribute 'summary' := 'Consumes an interface that takes strings and structures'}
{attribute 'license' := 'GPL'}

(* The scalar part of a GMI interface was reachable first; this is the rest.

   A structure's ST type is a view over the one the generated header already
   declares -- named as the interface names it, compiling to the header's C
   type -- so there is no second declaration to keep in step, and a field ST
   cannot represent is simply not nameable rather than being approximated.

   A string reaches the callback as `const char *`, and an ST STRING is a char
   array, so passing one costs nothing.  Receiving one is a different matter
   and is refused; see docs/dev/MODCOMPILE_ST_STDLIB.md §9. *)
{attribute 'gmi_consume' := 'hm2_serial from hostmot2'}

PROGRAM gmistructs
  VAR_OUTPUT
    rc    : DINT;
  END_VAR
  VAR
    chan : STRING[32] := 'uart0';
    rx   : PktUartConfig;
    tx   : PktUartConfig;
  END_VAR

  rx.baudrate := 115200;
  rx.flags    := 0;
  tx.baudrate := 115200;

  rc := hm2_serial.pktuart_config(name := chan, rxcfg := rx, txcfg := tx, queue := 0);
END_PROGRAM
