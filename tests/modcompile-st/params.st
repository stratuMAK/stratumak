{attribute 'summary' := 'HAL parameters, at the top level and inside a block'}
{attribute 'license' := 'GPL'}

(* A HAL parameter is a value in shared memory that no signal connects to.
   `AT %MRW` is a tunable HAL sets and the component reads; `AT %MR` is a
   diagnostic the component writes and HAL only reads.  See §3.5. *)

FUNCTION_BLOCK Limiter
  VAR_INPUT
    raw : LREAL;
  END_VAR
  VAR_OUTPUT
    limited : LREAL;
  END_VAR
  VAR
    (* A parameter inside a block becomes one per instance, named by the
       path that reaches it -- exactly as a published pin is. *)
    ceiling AT %MRW        : LREAL := 100.0;
    hits    AT %MR.clamped : DINT;
  END_VAR

  limited := raw;
  IF limited > ceiling THEN
    limited := ceiling;
    hits := hits + 1;
  END_IF;
END_FUNCTION_BLOCK

PROGRAM params
  VAR_INPUT
    cmd : LREAL;
  END_VAR
  VAR_OUTPUT
    out : LREAL;
  END_VAR
  VAR
    {attribute 'doc' := 'Multiplies the command before limiting.'}
    gain    AT %MRW       : LREAL := 1.0;
    {attribute 'doc' := 'Set when the last cycle was scaled down.'}
    scaled  AT %MR.active : BOOL;
    invert  AT %MRW       : BOOL;

    lim : Limiter;
  END_VAR

  lim(raw := cmd * gain);
  out := lim.limited;
  IF invert THEN
    out := -out;
  END_IF;
  scaled := gain < 1.0;
END_PROGRAM
