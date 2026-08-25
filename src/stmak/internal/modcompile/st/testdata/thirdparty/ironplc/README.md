# IronPLC test corpus (subset)

Imported IEC 61131-3 Structured Text fixtures used to exercise the `.st`
scanner and parser.

| | |
| --- | --- |
| Upstream | https://github.com/ironplc/ironplc |
| Path | `compiler/resources/test/` |
| Commit | `d3b756aa6cc910a735a6f93f7e5135bb44c35e9f` (2026-08-25) |
| Licence | MIT — see [LICENSE](LICENSE), copied verbatim from upstream |
| Copyright | 2022 Garret Fick |

MIT permits redistribution inside this GPL-2-or-later tree provided the notice
is preserved, which is what `LICENSE` here is for. These files are test input
only: they are never compiled into, linked with, or shipped in any binary.

## These are parse-level fixtures, not compile-level

Most of these files reference identifiers that are never declared —
`expressions.st` uses `var3` and `FUNC_NAME`, `nested.st` uses `array_avg`,
`arr` and `idx`, `first_steps_program.st` instantiates function block types
defined in sibling files that were not imported. Upstream uses them to test
parsing, so they are valid syntax and invalid programs.

That makes them the right corpus for phase 1 (scanner and parser: must parse
without a syntax error) and the wrong one for phase 2 onward, where sema will
correctly reject most of them with "unknown identifier". Do not promote them to
an accept-and-compile tier without editing them, and if they are edited, say so
here.

## What was taken

Statement and expression coverage:

- `expressions.st` — operator precedence across arithmetic, comparison, `MOD`,
  `**`, `XOR`, and typed literals. The single most valuable file here.
- `textual.st` — the full statement set: `IF`/`ELSIF`/`ELSE`, `CASE` with and
  without `ELSE`, `FOR ... BY`, `WHILE`, `REPEAT`.
- `if.st`, `conditional.st`, `literal.st`

Declarations:

- `input_var_decl.st`, `inout_var_decl.st`, `var_temp.st`, `empty_var_block.st`
- `first_steps_data_type_decl.st` — enumeration with a default value
- `first_steps_func_avg_val.st` — `FUNCTION` with `VAR_INPUT`
- `first_steps_function_block_logger.st` — `FUNCTION_BLOCK`
- `first_steps_program.st` — a `PROGRAM` with `VAR_INPUT`/`VAR_OUTPUT` calling
  function block instances by named argument and reading their outputs. The
  closest upstream file to the shape described in
  `docs/dev/MODCOMPILE_ST_FRONTEND.md` §3.

Arrays, comments, scanning:

- `array.st`, `array_in_function_var.st`, `array_of_string.st`, `nested.st`
- `comment.st`, `c_style_block_comment.st`
- `strings.st` — a non-ASCII character inside a string literal
- `steel_thread.st` — minimal smoke test

## What was deliberately left out

Everything exercising a construct stratuMAK does not implement, so that a
failure in this directory always means a real defect rather than an unsupported
feature:

- `CONFIGURATION` / `RESOURCE` / `TASK` and located variables (`AT %IX1`),
  `VAR_ACCESS` — stratuMAK maps I/O through `VAR_INPUT`/`VAR_OUTPUT` onto HAL
  pins instead: `configuration.st`, `first_steps_configuration.st`, `main.st`,
  `program.st`
- Other IEC languages — SFC, LD, FBD: `sfc.st`,
  `first_steps_function_block_counter_{sfc,ld,fbd}.st`
- References and pointers: `reference_to.st`, `ref.st`, `pointer_to.st`,
  `adr.st`
- `WSTRING`: `wstring_ops.st`, `sized_string_contexts.st`, `type_decl.st`,
  `var_decl.st`
- Vendor extensions — `SIZEOF`, `WORD` bit access: `sizeof.st`,
  `bit_access.st`, `partial_access_bit.st`, `partial_access_multi.st`
- `first_steps.st` and its two error variants — 177 lines each, dominated by
  `CONFIGURATION` and SFC
- `oscat.st` — OSCAT-derived content, which would reopen a provenance question
  MIT alone does not settle
- `time_function_decl.st` — `TIME` is deferred; import it when `TIME` lands

## Updating

Re-import from the same upstream path, refresh the commit hash above, and
re-check the exclusion list — upstream adds files. Never edit a file in place
without recording it under "What was taken".
