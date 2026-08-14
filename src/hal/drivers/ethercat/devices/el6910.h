/**
 * @file el6910.h
 * @brief Driver for the Beckhoff EL6910 TwinSAFE logic terminal.
 *
 * The EL6910 is the second-generation TwinSAFE logic unit (successor to the
 * EL6900). It bridges multiple FSoE slaves to a safety project stored in the
 * terminal and exposes FSoE command/CRC/connection-ID fields per connection
 * plus standard I/O bits configurable through module parameters.
 *
 * It shares its whole process-data layout with the EL1918 logic part, so the
 * implementation lives in el1918_logic.c; only the FSoE object base differs
 * (@c LCEC_EL6910_FSOE_OFS). Unlike the EL6900 it has no control word, its
 * state is a full byte enum, and the standard variables are packed bytes in
 * 0xf788/0xf688 rather than bit arrays in 0xf201/0xf101.
 *
 * Vendor ID: LCEC_BECKHOFF_VID (0x00000002)
 * Product ID: 0x1afe3052
 *
 * Module parameters are the same set the EL1918 logic part accepts:
 *   - LCEC_EL6910_PARAM_SLAVEID     (1): EtherCAT index of a connected FSoE slave.
 *   - LCEC_EL6910_PARAM_STDIN_NAME  (2): HAL pin name for a standard input bit.
 *   - LCEC_EL6910_PARAM_STDOUT_NAME (3): HAL pin name for a standard output bit.
 *
 * @copyright Copyright (C) 2023-2026 Sascha Ittner <sascha.ittner@modusoft.de>
 *
 * This program is free software; you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation; either version 2 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, write to the Free Software
 * Foundation, Inc., 51 Franklin St, Fifth Floor, Boston, MA  02110-1301 USA
 */

#ifndef _LCEC_EL6910_H_
#define _LCEC_EL6910_H_

#include "../lcec.h"
#include "el1918_logic.h"

/** @brief EtherCAT vendor ID for EL6910 (Beckhoff). */
#define LCEC_EL6910_VID LCEC_BECKHOFF_VID
/** @brief EtherCAT product ID for the EL6910 TwinSAFE logic terminal. */
#define LCEC_EL6910_PID 0x1afe3052

/**
 * @brief Offset of the first FSoE message object inside 0x6000/0x7000.
 *
 * The EL6910 has no local safe I/O, so its connections start at the bottom of
 * the range. Compare @c LCEC_EL1918_LOGIC_FSOE_OFS.
 */
#define LCEC_EL6910_FSOE_OFS 0x0000

/*
 * The EL6910 takes the same module parameters as the EL1918 logic part, and
 * the shared implementation dispatches on these IDs, so alias them rather than
 * introducing a second set that could drift apart.
 */
/** @brief Module parameter ID: EtherCAT slave index of a connected FSoE slave. */
#define LCEC_EL6910_PARAM_SLAVEID     LCEC_EL1918_LOGIC_PARAM_SLAVEID
/** @brief Module parameter ID: HAL pin name for a standard input bit. */
#define LCEC_EL6910_PARAM_STDIN_NAME  LCEC_EL1918_LOGIC_PARAM_STDIN_NAME
/** @brief Module parameter ID: HAL pin name for a standard output bit. */
#define LCEC_EL6910_PARAM_STDOUT_NAME LCEC_EL1918_LOGIC_PARAM_STDOUT_NAME

/*
 * There is no lcec_el6910_preinit: pre-initialisation is independent of the
 * FSoE object base, so the typelist row uses lcec_el1918_logic_preinit
 * directly — the same do-not-duplicate reasoning as the parameter aliases
 * above.
 */

/**
 * @brief Initialize the EL6910 TwinSAFE logic terminal slave.
 *
 * @param comp_id        HAL component ID.
 * @param slave          Pointer to the lcec slave structure.
 * @param pdo_entry_regs Pointer to the PDO entry registration array (advanced in place).
 * @return 0 on success, negative errno on failure.
 */
int lcec_el6910_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs);

#endif
