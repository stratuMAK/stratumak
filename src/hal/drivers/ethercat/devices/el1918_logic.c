/**
 * @file el1918_logic.c
 * @brief Driver implementation for the Beckhoff TwinSAFE logic terminals.
 *
 * Manages dynamic FSoE slave associations, configurable standard I/O bits,
 * and the internal state/cycle-counter PDOs.
 *
 * This implementation serves both the EL1918 logic part and the EL6910, which
 * share the whole second-generation TwinSAFE logic layout: state and cycle
 * counter in 0xf100:01/:02, standard I/O packed into single bytes at 0xf788:00
 * and 0xf688:00, and one six byte FSoE message per connection laid out as
 * [cmd][data][crc][connid]. They differ in one constant only: the EL1918
 * carries eight local safe inputs on 0x6000/0x7000 and therefore places its
 * logic connections at 0x6080/0x7080, while the EL6910 has no local safe I/O
 * and starts at 0x6000/0x7000. See @c LCEC_EL1918_LOGIC_FSOE_OFS and
 * @c LCEC_EL6910_FSOE_OFS.
 *
 * State of testing. The EL6910 path is exercised on hardware. The EL1918 path
 * is not: it has never run on a logic-licensed EL1918, only the shared code
 * below has, and via the EL6910. What is known:
 *
 *   - The frame layout, the standard I/O bytes and the status PDO are the same
 *     on both devices; only the connection base differs.
 *   - On an EL1918-2200, the input-only variant, connection 001 lives at
 *     0x6080/0x7080. That unit has no logic, so this is its slave-role
 *     connection to an external master, and it says nothing about how a
 *     logic-licensed unit numbers its own master-role connections. Do not read
 *     it as proof that 0x?080 is the first external connection of the logic.
 *   - It is not known whether the local input modules of a logic unit consume
 *     a connection slot. Safe data consumed by the terminal's own logic never
 *     leaves the device and so needs no frame on the wire, which suggests they
 *     do not, and that connection 001 is the first external slave - the
 *     assumption this driver makes. If that is wrong, every connection is off
 *     by one and the first fsoeSlaveIdx has to name the terminal itself.
 *
 * The cheap way to settle it is the same one used for the EL6910: bring the
 * terminal up declared as a generic slave and read back its uploaded PDO map.
 * If the map holds one more connection than there are external slaves, the
 * local modules take slot 0.
 *
 * @copyright Copyright (C) 2021-2026 Sascha Ittner <sascha.ittner@modusoft.de>
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
#include "el1918_logic.h"
#include "el6910.h"

/**
 * @brief CRC PDO data for one FSoE data channel within a slave connection.
 */
typedef struct {
  stmak_hal_u32_t *fsoe_master_crc; /**< HAL output: FSoE master CRC for this data channel. */
  stmak_hal_u32_t *fsoe_slave_crc;  /**< HAL output: FSoE slave CRC for this data channel. */
  unsigned int fsoe_master_crc_os; /**< PDO offset: FSoE master CRC. */
  unsigned int fsoe_slave_crc_os;  /**< PDO offset: FSoE slave CRC. */
} lcec_el1918_logic_fsoe_crc_t;

/**
 * @brief HAL and PDO data for one FSoE slave connection managed by the EL1918.
 */
typedef struct {
  struct lcec_slave *fsoe_slave; /**< Pointer to the connected FSoE slave. */

  stmak_hal_u32_t *fsoe_master_cmd;    /**< HAL output: FSoE master command word. */
  stmak_hal_u32_t *fsoe_master_connid; /**< HAL output: FSoE master connection ID. */

  stmak_hal_u32_t *fsoe_slave_cmd;    /**< HAL output: FSoE slave command word. */
  stmak_hal_u32_t *fsoe_slave_connid; /**< HAL output: FSoE slave connection ID. */

  unsigned int fsoe_master_cmd_os;    /**< PDO offset: FSoE master command. */
  unsigned int fsoe_master_connid_os; /**< PDO offset: FSoE master connection ID. */

  unsigned int fsoe_slave_cmd_os;    /**< PDO offset: FSoE slave command. */
  unsigned int fsoe_slave_connid_os; /**< PDO offset: FSoE slave connection ID. */

  lcec_el1918_logic_fsoe_crc_t *fsoe_crc; /**< Array of per-channel CRC data (heap-allocated). */
} lcec_el1918_logic_fsoe_t;

/**
 * @brief Complete HAL data structure for the EL1918 logic terminal.
 *
 * The @c fsoe[] flexible array must be the last member; it is sized at
 * runtime to accommodate the number of connected FSoE slaves.
 */
typedef struct {
  int fsoe_count; /**< Number of FSoE slaves connected to this EL1918. */

  stmak_hal_u32_t *state;         /**< HAL output: EL1918 internal state register. */
  stmak_hal_u32_t *cycle_counter; /**< HAL output: EL1918 cycle counter. */

  stmak_hal_bit_t *std_in_pins[LCEC_EL1918_LOGIC_DIO_MAX_COUNT]; /**< Standard input HAL pins (STMAK_HAL_IN). */
  int std_in_count;    /**< Number of configured standard input pins. */
  unsigned int std_in_os; /**< PDO offset for the packed standard-input byte. */

  stmak_hal_bit_t *std_out_pins[LCEC_EL1918_LOGIC_DIO_MAX_COUNT]; /**< Standard output HAL pins (STMAK_HAL_OUT). */
  int std_out_count;    /**< Number of configured standard output pins. */
  unsigned int std_out_os; /**< PDO offset for the packed standard-output byte. */

  unsigned int state_os;         /**< PDO offset: EL1918 state register. */
  unsigned int cycle_counter_os; /**< PDO offset: EL1918 cycle counter. */

  // must be last entry (dynamic size)
  lcec_el1918_logic_fsoe_t fsoe[]; /**< Flexible array of FSoE slave connection data. */
} lcec_el1918_logic_data_t;

static const lcec_pindesc_t slave_pins[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_data_t, state), "%s.%s.%s.state" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_data_t, cycle_counter), "%s.%s.%s.cycle-counter" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

static const lcec_pindesc_t fsoe_pins[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_fsoe_t, fsoe_master_cmd), "%s.%s.%s.fsoe-%d-master-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_fsoe_t, fsoe_master_connid), "%s.%s.%s.fsoe-%d-master-connid" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_fsoe_t, fsoe_slave_cmd), "%s.%s.%s.fsoe-%d-slave-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_fsoe_t, fsoe_slave_connid), "%s.%s.%s.fsoe-%d-slave-connid" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

static const lcec_pindesc_t fsoe_crc_pins[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_fsoe_crc_t, fsoe_master_crc), "%s.%s.%s.fsoe-%d-master-crc%d" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_el1918_logic_fsoe_crc_t, fsoe_slave_crc), "%s.%s.%s.fsoe-%d-slave-crc%d" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

void lcec_el1918_logic_read(struct lcec_slave *slave, long period) STMAK_NONBLOCKING;
void lcec_el1918_logic_write(struct lcec_slave *slave, long period) STMAK_NONBLOCKING;

/**
 * @brief Export HAL pins for the standard I/O bits matching @p pid.
 *
 * Only creates pins; the bits share a single packed PDO entry (0xf788/0xf688)
 * that the caller registers once, so nothing is mapped here.
 *
 * @param slave EtherCAT slave structure.
 * @param pid   Parameter ID to match (STDIN_NAME or STDOUT_NAME).
 * @param pin   Pin pointer array to fill, in declaration order (bit 0 first).
 * @param dir   HAL pin direction.
 * @return Number of pins exported, or a negative error code.
 */
static int export_std_pins(struct lcec_slave *slave, int pid, stmak_hal_bit_t **pin, int dir) {
  lcec_master_t *master = slave->master;
  const cmod_env_t *env = master->env;
  lcec_slave_modparam_t *p;
  int count, err;

  for (p = slave->modparams, count = 0; p != NULL && p->id >= 0; p++) {
    // skip not matching params
    if (p->id != pid) {
      continue;
    }

    // export pin
    if ((err = lcec_pin_newf(env, master->comp_id, STMAK_HAL_BIT, dir, (void *) pin, "%s.%s.%s.%s", master->instance_name, master->name, slave->name, p->value.str)) != 0) {
      return err;
    }

    // next item
    pin++;
    count++;
  }

  return count;
}

/**
 * @brief Shared pre-initialisation for the TwinSAFE logic terminals.
 *
 * Counts the configured FSoE slaves and standard I/O bits to derive the PDO
 * entry count. Independent of the FSoE object base, so both devices use this
 * one exported function directly in their typelist rows — unlike init, there
 * is no per-device constant to bind, so a second entry point would only be a
 * body to keep in sync.
 *
 * @param slave Pointer to the lcec slave structure.
 * @return 0 on success, negative errno on failure.
 */
int lcec_el1918_logic_preinit(struct lcec_slave *slave) {
  lcec_master_t *master = slave->master;
  lcec_slave_modparam_t *p;
  int index, stdin_count, stdout_count;
  struct lcec_slave *fsoe_slave;
  const LCEC_CONF_FSOE_T *fsoeConf;

  slave->pdo_entry_count = LCEC_EL1918_LOGIC_PDOS;

  stdin_count = 0;
  stdout_count = 0;
  for (p = slave->modparams; p != NULL && p->id >= 0; p++) {
    switch(p->id) {
      case LCEC_EL1918_LOGIC_PARAM_SLAVEID:
        // find slave
        index = p->value.u32;
        fsoe_slave = lcec_slave_by_index(master, index);
        if (fsoe_slave == NULL) {
          LCEC_ERR(master, "%s.%s: slave index %d not found", master->name, slave->name, index);
          return -EINVAL;
        }

        fsoeConf = fsoe_slave->fsoeConf;
        if (fsoeConf == NULL) {
          LCEC_ERR(master, "%s.%s: slave index %d is not a fsoe slave", master->name, slave->name, index);
          return -EINVAL;
        }

        slave->pdo_entry_count += LCEC_EL1918_LOGIC_PARAM_SLAVE_PDOS + LCEC_EL1918_LOGIC_PARAM_SLAVE_CH_PDOS * fsoeConf->data_channels;
        break;

      case LCEC_EL1918_LOGIC_PARAM_STDIN_NAME:
        stdin_count++;
        if (stdin_count > LCEC_EL1918_LOGIC_DIO_MAX_COUNT) {
          LCEC_ERR(master, "%s.%s: maximum stdin count exceeded.", master->name, slave->name);
          return -EINVAL;
        }

        break;

      case LCEC_EL1918_LOGIC_PARAM_STDOUT_NAME:
        stdout_count++;
        if (stdout_count > LCEC_EL1918_LOGIC_DIO_MAX_COUNT) {
          LCEC_ERR(master, "%s.%s: maximum stdout count exceeded.", master->name, slave->name);
          return -EINVAL;
        }

        break;
    }
  }

  if (stdin_count > 0) {
    slave->pdo_entry_count += LCEC_EL1918_LOGIC_STDIN_PDOS;
  }
  if (stdout_count > 0) {
    slave->pdo_entry_count += LCEC_EL1918_LOGIC_STDOUT_PDOS;
  }

  return 0;
}

/**
 * @brief Shared initialisation for the TwinSAFE logic terminals.
 *
 * @param comp_id        HAL component ID.
 * @param slave          Pointer to the lcec slave structure.
 * @param pdo_entry_regs PDO entry registration array, advanced in place.
 * @param fsoe_ofs       Offset of the first FSoE message object within the
 *                       0x6000/0x7000 ranges (@c LCEC_EL1918_LOGIC_FSOE_OFS
 *                       or @c LCEC_EL6910_FSOE_OFS).
 * @return 0 on success, negative errno on failure.
 */
static int fslogic_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs, unsigned int fsoe_ofs) {
  lcec_master_t *master = slave->master;
  const cmod_env_t *env = master->env;
  lcec_el1918_logic_data_t *hal_data;
  lcec_el1918_logic_fsoe_t *fsoe_data;
  lcec_slave_modparam_t *p;
  int fsoe_idx, index, err;
  lcec_el1918_logic_fsoe_crc_t *crc;
  struct lcec_slave *fsoe_slave;
  const LCEC_CONF_FSOE_T *fsoeConf;

  // initialize callbacks
  slave->proc_read = lcec_el1918_logic_read;
  slave->proc_write = lcec_el1918_logic_write;

  // count fsoe slaves
  for (fsoe_idx = 0, p = slave->modparams; p != NULL && p->id >= 0; p++) {
    if (p->id == LCEC_EL1918_LOGIC_PARAM_SLAVEID) {
      fsoe_idx++;
    }
  }

  // alloc hal memory
  if ((hal_data = env->hal->malloc(env->hal->ctx, sizeof(lcec_el1918_logic_data_t) + fsoe_idx * sizeof(lcec_el1918_logic_fsoe_t))) == NULL) {
    LCEC_ERR(master, "hal_malloc() for slave %s.%s failed", master->name, slave->name);
    return -EIO;
  }
  memset(hal_data, 0, sizeof(lcec_el1918_logic_data_t) + fsoe_idx * sizeof(lcec_el1918_logic_fsoe_t));
  hal_data->fsoe_count = fsoe_idx;
  slave->hal_data = hal_data;

  // initialize POD entries
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xf100, 0x01, &hal_data->state_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xf100, 0x02, &hal_data->cycle_counter_os, NULL);

  // export pins
  if ((err = lcec_pin_newf_list(env, comp_id, hal_data, slave_pins, master->instance_name, master->name, slave->name)) != 0) {
    return err;
  }

  // map and export stdios
  hal_data->std_in_count = export_std_pins(slave, LCEC_EL1918_LOGIC_PARAM_STDIN_NAME, hal_data->std_in_pins, STMAK_HAL_IN);
  if (hal_data->std_in_count < 0) {
    return hal_data->std_in_count;
  }
  if (hal_data->std_in_count > 0) {
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xf788, 0x00, &hal_data->std_in_os, NULL);
  }

  hal_data->std_out_count = export_std_pins(slave, LCEC_EL1918_LOGIC_PARAM_STDOUT_NAME, hal_data->std_out_pins, STMAK_HAL_OUT);
  if (hal_data->std_out_count < 0) {
    return hal_data->std_out_count;
  }
  if (hal_data->std_out_count > 0) {
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xf688, 0x00, &hal_data->std_out_os, NULL);
  }

  // map and export fsoe slave data
  for (fsoe_idx = 0, fsoe_data = hal_data->fsoe, p = slave->modparams; p != NULL && p->id >= 0; p++) {
    if (p->id == LCEC_EL1918_LOGIC_PARAM_SLAVEID) {
      // find slave
      index = p->value.u32;
      fsoe_slave = lcec_slave_by_index(master, index);
      if (fsoe_slave == NULL) {
        LCEC_ERR(master, "%s.%s: slave index %d not found", master->name, slave->name, index);
        return -EINVAL;
      }
      fsoe_data->fsoe_slave = fsoe_slave;
      fsoe_slave->fsoe_slave_offset = &fsoe_data->fsoe_slave_cmd_os;
      fsoe_slave->fsoe_master_offset = &fsoe_data->fsoe_master_cmd_os;
      fsoeConf = fsoe_slave->fsoeConf;

      // alloc crc hal memory
      if ((fsoe_data->fsoe_crc = env->hal->malloc(env->hal->ctx, fsoeConf->data_channels * sizeof(lcec_el1918_logic_fsoe_crc_t))) == NULL) {
        LCEC_ERR(master, "hal_malloc() for fsoe_slave %s.%s crc data failed", master->name, fsoe_slave->name);
        return -EIO;
      }
      memset(fsoe_data->fsoe_crc, 0, fsoeConf->data_channels * sizeof(lcec_el1918_logic_fsoe_crc_t));

      // initialize POD entries
      LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7000 + fsoe_ofs + (fsoe_idx << 4), 0x01, &fsoe_data->fsoe_slave_cmd_os, NULL);
      LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7000 + fsoe_ofs + (fsoe_idx << 4), 0x02, &fsoe_data->fsoe_slave_connid_os, NULL);
      LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6000 + fsoe_ofs + (fsoe_idx << 4), 0x01, &fsoe_data->fsoe_master_cmd_os, NULL);
      LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6000 + fsoe_ofs + (fsoe_idx << 4), 0x02, &fsoe_data->fsoe_master_connid_os, NULL);

      // export pins
      if ((err = lcec_pin_newf_list(env, comp_id, fsoe_data, fsoe_pins, master->instance_name, master->name, slave->name, fsoe_idx)) != 0) {
        return err;
      }

      // map CRC PDOS
      for (index = 0, crc = fsoe_data->fsoe_crc; index < fsoeConf->data_channels; index++, crc++) {
        LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x7000 + fsoe_ofs + (fsoe_idx << 4), 0x03 + index, &crc->fsoe_slave_crc_os, NULL);
        LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0x6000 + fsoe_ofs + (fsoe_idx << 4), 0x03 + index, &crc->fsoe_master_crc_os, NULL);
        if ((err = lcec_pin_newf_list(env, comp_id, crc, fsoe_crc_pins, master->instance_name, master->name, slave->name, fsoe_idx, index)) != 0) {
          return err;
        }
      }

      fsoe_idx++;
      fsoe_data++;
    }
  }

  return 0;
}

int lcec_el1918_logic_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs) {
  return fslogic_init(comp_id, slave, pdo_entry_regs, LCEC_EL1918_LOGIC_FSOE_OFS);
}

int lcec_el6910_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs) {
  return fslogic_init(comp_id, slave, pdo_entry_regs, LCEC_EL6910_FSOE_OFS);
}

void lcec_el1918_logic_read(struct lcec_slave *slave, long period) {
  lcec_master_t *master = slave->master;
  lcec_el1918_logic_data_t *hal_data = (lcec_el1918_logic_data_t *) slave->hal_data;
  uint8_t *pd = master->process_data;
  lcec_el1918_logic_fsoe_t *fsoe_data;
  int i, crc_idx;
  uint8_t std_out;
  lcec_el1918_logic_fsoe_crc_t *crc;
  struct lcec_slave *fsoe_slave;
  const LCEC_CONF_FSOE_T *fsoeConf;

  *(hal_data->state) = EC_READ_U8(&pd[hal_data->state_os]);
  *(hal_data->cycle_counter) = EC_READ_U8(&pd[hal_data->cycle_counter_os]);

  if (hal_data->std_out_count > 0) {
    std_out = EC_READ_U8(&pd[hal_data->std_out_os]);
    for (i = 0; i < hal_data->std_out_count; i++) {
      *(hal_data->std_out_pins[i]) = !!(std_out & (1 << i));
    }
  }

  for (i = 0, fsoe_data = hal_data->fsoe; i < hal_data->fsoe_count; i++, fsoe_data++) {
    fsoe_slave = fsoe_data->fsoe_slave;
    fsoeConf = fsoe_slave->fsoeConf;
    *(fsoe_data->fsoe_master_cmd) = EC_READ_U8(&pd[fsoe_data->fsoe_master_cmd_os]);
    *(fsoe_data->fsoe_master_connid) = EC_READ_U16(&pd[fsoe_data->fsoe_master_connid_os]);
    *(fsoe_data->fsoe_slave_cmd) = EC_READ_U8(&pd[fsoe_data->fsoe_slave_cmd_os]);
    *(fsoe_data->fsoe_slave_connid) = EC_READ_U16(&pd[fsoe_data->fsoe_slave_connid_os]);
    for (crc_idx = 0, crc = fsoe_data->fsoe_crc; crc_idx < fsoeConf->data_channels; crc_idx++, crc++) {
      *(crc->fsoe_master_crc) = EC_READ_U16(&pd[crc->fsoe_master_crc_os]);
      *(crc->fsoe_slave_crc) = EC_READ_U16(&pd[crc->fsoe_slave_crc_os]);
    }
  }
}

void lcec_el1918_logic_write(struct lcec_slave *slave, long period) {
  lcec_master_t *master = slave->master;
  lcec_el1918_logic_data_t *hal_data = (lcec_el1918_logic_data_t *) slave->hal_data;
  uint8_t *pd = master->process_data;
  uint8_t std_in;
  int i;

  if (hal_data->std_in_count > 0) {
    std_in = 0;
    for (i = 0; i < hal_data->std_in_count; i++) {
      if (*(hal_data->std_in_pins[i])) std_in |= (1 << i);
    }
    EC_WRITE_U8(&pd[hal_data->std_in_os], std_in);
  }
}
