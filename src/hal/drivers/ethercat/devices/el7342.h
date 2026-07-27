/**
 * @file el7342.h
 * @brief Driver header for the Beckhoff EL7342 2-channel DC motor terminal.
 *
 * The EL7342 provides two independent DC motor drive channels, each with
 * an integrated incremental encoder interface and a 16-bit signed velocity
 * setpoint PDO.  Each channel also supports two synchronous information
 * words that can be selected to report quantities such as motor voltage,
 * current, duty cycle, velocity, or temperature.
 *
 * @copyright Copyright (C) 2011-2026 Sascha Ittner <sascha.ittner@modusoft.de>
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

#ifndef _LCEC_EL7342_H_
#define _LCEC_EL7342_H_

#include "../lcec.h"

#define LCEC_EL7342_VID LCEC_BECKHOFF_VID  /**< Beckhoff vendor ID */
#define LCEC_EL7342_PID 0x1cae3052         /**< EL7342 product ID */

#define LCEC_EL7342_CHANS 2                                  /**< Number of independent motor channels */
#define LCEC_EL7342_PDOS  (33 * LCEC_EL7342_CHANS)          /**< Total PDO entry count (33 per channel) */

// Module parameters.  The low nibble selects the channel (chN-), the high
// nibbles select the function; both are OR'd together in the descriptor table
// (see EL5002 for the same scheme).  Each maps to a per-channel CoE object that
// is written at init via ecrt_slave_config_sdo* (applied by the master at
// activation — RAM only, no EEPROM).  ch1 objects are at the ch0 index + 0x10.
#define LCEC_EL7342_PARAM_CH_MASK   0x000f   /**< Channel selector mask (0 or 1) */
#define LCEC_EL7342_PARAM_FNK_MASK  0xfff0   /**< Function selector mask */

#define LCEC_EL7342_PARAM_CH_0      0x0000   /**< Channel 0 */
#define LCEC_EL7342_PARAM_CH_1      0x0001   /**< Channel 1 */

#define LCEC_EL7342_PARAM_OP_MODE   0x0010   /**< DCM Features operation mode (0x80n2:01, u8; 1 = velocityDirect) */
#define LCEC_EL7342_PARAM_INFO1     0x0020   /**< DCM Features info-data-1 select (0x80n2:11, u8; 7 = motor velocity -> enables srv-N-velo-fb) */
#define LCEC_EL7342_PARAM_MAX_CURR  0x0030   /**< DCM Settings max current (0x80n0:01, u16, mA) */
#define LCEC_EL7342_PARAM_NOM_CURR  0x0040   /**< DCM Settings nominal current (0x80n0:02, u16, mA) */
#define LCEC_EL7342_PARAM_NOM_VOLT  0x0050   /**< DCM Settings nominal voltage (0x80n0:03, u16, mV) */
#define LCEC_EL7342_PARAM_COIL_RES  0x0060   /**< DCM Settings coil resistance (0x80n0:04, u16) */
#define LCEC_EL7342_PARAM_ENC_INCR  0x0070   /**< DCM Settings encoder increments/rev (0x80n0:07, u16 — verify vs ESI) */
#define LCEC_EL7342_PARAM_NOM_RPM   0x0080   /**< DCM Settings nominal motor speed (0x80n0:08, u16, rpm — verify vs ESI) */

/**
 * @brief Initialise the EL7342 2-channel DC motor terminal.
 *
 * @param comp_id         HAL component ID.
 * @param slave           Pointer to the EtherCAT slave structure.
 * @param pdo_entry_regs  Pointer to the PDO entry registration array.
 * @return 0 on success, negative errno on failure.
 */
int lcec_el7342_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs);

#endif
