/**
 * @file el1918.c
 * @brief Driver implementation for the Beckhoff EL1918 8-channel TwinSAFE digital input terminal.
 *
 * Exposes the FSoE command/CRC/connection-ID fields and, per channel, the safe
 * input, its module fault flag and the error acknowledgement bit, and copies
 * FSoE frame data between the slave and master PDO areas.
 *
 * All pins are read-only. The error acknowledgement bits live inside the
 * master-to-slave frame payload, which is produced by the safety logic and
 * relayed here verbatim by copy_fsoe_data(); writing them from HAL would alter
 * bytes the logic has already checksummed and break the connection. Acknowledge
 * through the safety project instead (on an EL6900/EL6910 that is a standard
 * input variable), exactly as the EL2904 exposes its safe outputs read-only.
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

#include "../lcec.h"
#include "el1918.h"

/**
 * @brief Per-channel HAL data for one EL1918 safe digital input.
 */
typedef struct {
  stmak_hal_bit_t *fsoe_in;     /**< HAL output: safe digital input state. */
  stmak_hal_bit_t *fsoe_in_not; /**< HAL output: inverted safe digital input state. */
  stmak_hal_bit_t *fsoe_fault;  /**< HAL output: module fault for this channel. */
  stmak_hal_bit_t *fsoe_errack; /**< HAL output: error acknowledgement bit sent by the safety logic. */

  unsigned int fsoe_in_os; /**< Byte offset of the safe input bit. */
  unsigned int fsoe_in_bp; /**< Bit position within fsoe_in_os. */

  unsigned int fsoe_fault_os; /**< Byte offset of the module fault bit. */
  unsigned int fsoe_fault_bp; /**< Bit position within fsoe_fault_os. */

  unsigned int fsoe_errack_os; /**< Byte offset of the error acknowledgement bit. */
  unsigned int fsoe_errack_bp; /**< Bit position within fsoe_errack_os. */
} lcec_el1918_data_in_t;

/**
 * @brief Complete HAL data structure for the EL1918.
 */
typedef struct {
  stmak_hal_u32_t *fsoe_master_cmd;    /**< HAL output: FSoE master command word. */
  stmak_hal_u32_t *fsoe_master_crc;    /**< HAL output: FSoE master CRC. */
  stmak_hal_u32_t *fsoe_master_connid; /**< HAL output: FSoE master connection ID. */

  stmak_hal_u32_t *fsoe_slave_cmd;    /**< HAL output: FSoE slave command word. */
  stmak_hal_u32_t *fsoe_slave_crc;    /**< HAL output: FSoE slave CRC. */
  stmak_hal_u32_t *fsoe_slave_connid; /**< HAL output: FSoE slave connection ID. */

  lcec_el1918_data_in_t inputs[LCEC_EL1918_INPUT_COUNT]; /**< Per-channel safe input data. */

  unsigned int fsoe_master_cmd_os;    /**< PDO offset: FSoE master command. */
  unsigned int fsoe_master_crc_os;    /**< PDO offset: FSoE master CRC. */
  unsigned int fsoe_master_connid_os; /**< PDO offset: FSoE master connection ID. */

  unsigned int fsoe_slave_cmd_os;    /**< PDO offset: FSoE slave command. */
  unsigned int fsoe_slave_crc_os;    /**< PDO offset: FSoE slave CRC. */
  unsigned int fsoe_slave_connid_os; /**< PDO offset: FSoE slave connection ID. */

} lcec_el1918_data_t;

static const lcec_pindesc_t slave_pins[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_data_t, fsoe_master_cmd), "%s.%s.%s.fsoe-master-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_data_t, fsoe_master_crc), "%s.%s.%s.fsoe-master-crc" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_data_t, fsoe_master_connid), "%s.%s.%s.fsoe-master-connid" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_data_t, fsoe_slave_cmd), "%s.%s.%s.fsoe-slave-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_data_t, fsoe_slave_crc), "%s.%s.%s.fsoe-slave-crc" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_data_t, fsoe_slave_connid), "%s.%s.%s.fsoe-slave-connid" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

static const lcec_pindesc_t slave_in_pins[] = {
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_el1918_data_in_t, fsoe_in), "%s.%s.%s.fsoe-in-%d" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_el1918_data_in_t, fsoe_in_not), "%s.%s.%s.fsoe-in-%d-not" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_el1918_data_in_t, fsoe_fault), "%s.%s.%s.fsoe-in-%d-fault" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_el1918_data_in_t, fsoe_errack), "%s.%s.%s.fsoe-in-%d-errack" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

/**
 * @brief FsoE frame dimensions.
 *
 * Two safe data bytes slave-to-master (eight inputs interleaved with eight
 * module fault bits), one master-to-slave (eight error acknowledgement bits).
 */
static const LCEC_CONF_FSOE_T fsoe_conf = {
  .slave_data_len = 2,
  .master_data_len = 1,
  .data_channels = 1
};

/**
 * @brief EtherCAT cyclic read callback — relays the FSoE frame and reads all pins.
 * @param slave  Pointer to the lcec slave structure.
 * @param period Servo period in nanoseconds (unused).
 */
void lcec_el1918_read(struct lcec_slave *slave, long period) STMAK_NONBLOCKING;

int lcec_el1918_preinit(struct lcec_slave *slave) {
  // set fsoe config
  slave->fsoeConf = &fsoe_conf;

  return 0;
}

int lcec_el1918_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs) {
  lcec_master_t *master = slave->master;
  const cmod_env_t *env = master->env;
  lcec_el1918_data_t *hal_data;
  int i, err;
  lcec_el1918_data_in_t *in;

  // initialize callbacks
  slave->proc_read = lcec_el1918_read;

  // alloc hal memory
  if ((hal_data = env->hal->malloc(env->hal->ctx, sizeof(lcec_el1918_data_t))) == NULL) {
    LCEC_ERR(master, "hal_malloc() for slave %s.%s failed", master->name, slave->name);
    return -EIO;
  }
  memset(hal_data, 0, sizeof(lcec_el1918_data_t));
  slave->hal_data = hal_data;

  // initialize POD entries, in frame order: [cmd][data][crc][connid]
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7080, 0x01, &hal_data->fsoe_master_cmd_os, NULL);
  for (i = 0, in = hal_data->inputs; i < LCEC_EL1918_INPUT_COUNT; i++, in++) {
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7002 + (i << 4), 0x01, &in->fsoe_errack_os, &in->fsoe_errack_bp);
  }
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7080, 0x03, &hal_data->fsoe_master_crc_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7080, 0x02, &hal_data->fsoe_master_connid_os, NULL);

  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6080, 0x01, &hal_data->fsoe_slave_cmd_os, NULL);
  for (i = 0, in = hal_data->inputs; i < LCEC_EL1918_INPUT_COUNT; i++, in++) {
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6001 + (i << 4), 0x01, &in->fsoe_in_os, &in->fsoe_in_bp);
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6002 + (i << 4), 0x01, &in->fsoe_fault_os, &in->fsoe_fault_bp);
  }
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6080, 0x03, &hal_data->fsoe_slave_crc_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6080, 0x02, &hal_data->fsoe_slave_connid_os, NULL);

  // export pins
  if ((err = lcec_pin_newf_list(env, comp_id, hal_data, slave_pins, master->instance_name, master->name, slave->name)) != 0) {
    return err;
  }
  for (i = 0, in = hal_data->inputs; i < LCEC_EL1918_INPUT_COUNT; i++, in++) {
    if ((err = lcec_pin_newf_list(env, comp_id, in, slave_in_pins, master->instance_name, master->name, slave->name, i)) != 0) {
      return err;
    }
  }

  return 0;
}

void lcec_el1918_read(struct lcec_slave *slave, long period) {
  lcec_master_t *master = slave->master;
  lcec_el1918_data_t *hal_data = (lcec_el1918_data_t *) slave->hal_data;
  uint8_t *pd = master->process_data;
  int i;
  lcec_el1918_data_in_t *in;

  copy_fsoe_data(slave, hal_data->fsoe_slave_cmd_os, hal_data->fsoe_master_cmd_os);

  *(hal_data->fsoe_slave_cmd) = EC_READ_U8(&pd[hal_data->fsoe_slave_cmd_os]);
  *(hal_data->fsoe_slave_crc) = EC_READ_U16(&pd[hal_data->fsoe_slave_crc_os]);
  *(hal_data->fsoe_slave_connid) = EC_READ_U16(&pd[hal_data->fsoe_slave_connid_os]);

  *(hal_data->fsoe_master_cmd) = EC_READ_U8(&pd[hal_data->fsoe_master_cmd_os]);
  *(hal_data->fsoe_master_crc) = EC_READ_U16(&pd[hal_data->fsoe_master_crc_os]);
  *(hal_data->fsoe_master_connid) = EC_READ_U16(&pd[hal_data->fsoe_master_connid_os]);

  for (i = 0, in = hal_data->inputs; i < LCEC_EL1918_INPUT_COUNT; i++, in++) {
    *(in->fsoe_in) = EC_READ_BIT(&pd[in->fsoe_in_os], in->fsoe_in_bp);
    *(in->fsoe_in_not) = ! *(in->fsoe_in);
    *(in->fsoe_fault) = EC_READ_BIT(&pd[in->fsoe_fault_os], in->fsoe_fault_bp);
    *(in->fsoe_errack) = EC_READ_BIT(&pd[in->fsoe_errack_os], in->fsoe_errack_bp);
  }
}
