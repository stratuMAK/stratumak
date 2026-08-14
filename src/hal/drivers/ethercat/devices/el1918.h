/**
 * @file el1918.h
 * @brief Driver for the Beckhoff EL1918 8-channel TwinSAFE digital input terminal.
 *
 * The EL1918 is an FSoE slave carrying eight safe digital inputs. Each channel
 * is a separate "FSIN Module" in the object dictionary, at a 0x10 stride:
 * inputs in 0x6001+(n<<4), module status in 0x6002+(n<<4), and the error
 * acknowledgement bit in 0x7002+(n<<4). The FSoE frame itself sits in
 * 0x6080/0x7080, above the eight module ranges.
 *
 * The frame is asymmetric: the slave-to-master direction carries two safe data
 * bytes (eight inputs interleaved with eight module-fault bits), the
 * master-to-slave direction one (the eight error acknowledgement bits).
 *
 * Vendor ID: LCEC_BECKHOFF_VID (0x00000002)
 * Product ID: 0x077e3052
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

#ifndef _LCEC_EL1918_H_
#define _LCEC_EL1918_H_

#include "../lcec.h"

/** @brief EtherCAT vendor ID for EL1918 (Beckhoff). */
#define LCEC_EL1918_VID LCEC_BECKHOFF_VID
/** @brief EtherCAT product ID for the EL1918 safety input terminal. */
#define LCEC_EL1918_PID 0x077e3052

/** @brief Number of safe digital input channels. */
#define LCEC_EL1918_INPUT_COUNT 8

/**
 * @brief Number of PDO entries.
 *
 * Six for the FSoE frame (command, CRC and connection ID per direction) plus
 * three per channel: input, module fault, error acknowledgement.
 */
#define LCEC_EL1918_PDOS (6 + LCEC_EL1918_INPUT_COUNT * 3)

/**
 * @brief Pre-initialise the EL1918 slave (attaches the FsoE configuration).
 * @param slave Pointer to the lcec slave structure.
 * @return 0 on success.
 */
int lcec_el1918_preinit(struct lcec_slave *slave);

/**
 * @brief Initialize the EL1918 TwinSAFE digital input slave.
 *
 * @param comp_id        HAL component ID.
 * @param slave          Pointer to the lcec slave structure.
 * @param pdo_entry_regs Pointer to the PDO entry registration array (advanced in place).
 * @return 0 on success, negative errno on failure.
 */
int lcec_el1918_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs);

#endif
