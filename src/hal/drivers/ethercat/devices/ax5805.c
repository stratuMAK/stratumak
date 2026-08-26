/**
 * @file ax5805.c
 * @brief Driver for the Beckhoff AX5805 TwinSAFE drive option card.
 *
 * The AX5805 is the FSoE safety card for AX5100/AX5200 drives.  It must sit
 * at EtherCAT index (drive_index + 1).  Init registers all FSoE PDO entries and
 * exports HAL pins for:
 *   - fsoe-master-cmd / fsoe-master-connid / fsoe-master-crc[-0/-1]
 *   - fsoe-slave-cmd  / fsoe-slave-connid  / fsoe-slave-crc[-0/-1]
 *   - fsoe-cmd-data-N / fsoe-cmd-sto-N / fsoe-cmd-err-ack-N   (commanded, per axis)
 *   - fsoe-sts-data-N / fsoe-sts-sto-N / fsoe-sts-err-N       (reported,  per axis)
 * The read callback copies FSoE data and updates all HAL pin values.
 *
 * Both directions of the safety payload are published deliberately, because they
 * are independent: the command is what the safety application asks of the card,
 * the status is the state the card is actually in, and the two can differ in
 * either direction.  A card can report a function active that was never
 * demanded - after an internal error, say - and reading only one side hides
 * exactly that case.
 *
 * The pins carry the payload as transmitted.  Nothing is inverted or otherwise
 * interpreted: what a given bit means, and in which sense, is the device's
 * business and is documented by the vendor.  A configuration that wants the
 * opposite sense should invert it explicitly in HAL.
 *
 * @par Configuration
 * The card is a modular device: object @c 0x2F10 selects the safety process
 * data module, and that choice decides everything else - the size of the FSoE
 * frame, whether axis 2 is present, and the contents of the @c 0x1600 /
 * @c 0x1A00 mappings.  The driver issues no configuration of its own; the whole
 * parameter set including @c 0x2F10 comes from the TwinCAT export referenced by
 * @c \<initCmds\>, which is therefore mandatory for this device.  What the
 * driver does is read the module ident back out of that command list and
 * declare the matching process image, so the master sizes the domain from the
 * layout the card will actually have rather than from whatever it happened to
 * be holding when the bus was scanned.
 *
 * Without the declaration the master snapshots the scanned mapping in
 * ecrt_master_slave_config() (main.c) long before @c 0x2F10 is applied at
 * PREOP->SAFEOP, so a card that changes mode during start-up comes up with
 * stale offsets, or refuses SAFEOP outright because the sync-manager length no
 * longer matches.
 *
 * @copyright Copyright (C) 2018-2026 Sascha Ittner <sascha.ittner@modusoft.de>
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
#include "ax5805.h"
#include "ax5100.h"
#include "ax5200.h"

/** @brief Object holding the safety process data module ident (see file header). */
#define LCEC_AX5805_IDX_MODULE_SEL 0x2F10

/** @brief Axis count encoded in the high word of an @c 0x2F10 module ident. */
#define LCEC_AX5805_MODULE_AXES(ident) ((ident) >> 16)

/** @brief FSoE configuration for a one-axis safety module: 2-byte data, one channel. */
static const LCEC_CONF_FSOE_T fsoe_conf_1ch = {
  .slave_data_len = 2,
  .master_data_len = 2,
  .data_channels = 1
};

/** @brief FSoE configuration for a two-axis safety module: 2-byte data, two channels. */
static const LCEC_CONF_FSOE_T fsoe_conf_2ch = {
  .slave_data_len = 2,
  .master_data_len = 2,
  .data_channels = 2
};

/**
 * @brief One entry of a safety PDO mapping, as declared by the AX5805 ESI.
 */
typedef struct {
  uint16_t index;     /**< Object index, or 0 for a padding bit. */
  uint8_t  subindex;  /**< Subindex. */
  uint8_t  bits;      /**< Bit length. */
} lcec_ax5805_pdo_entry_t;

/* The safety data field of each axis is a full 2 bytes: eight named function
   bits followed by eight unnamed pad bits.  The pad bits must be declared or
   everything after them lands at the wrong offset. */
#define LCEC_AX5805_PAD8 \
  {0x0000, 0x00, 1}, {0x0000, 0x00, 1}, {0x0000, 0x00, 1}, {0x0000, 0x00, 1}, \
  {0x0000, 0x00, 1}, {0x0000, 0x00, 1}, {0x0000, 0x00, 1}, {0x0000, 0x00, 1}

/** @brief RxPDO 0x1600 (outputs, master to card), axis 1 block plus its CRC. */
static const lcec_ax5805_pdo_entry_t rx_axis1[] = {
  {0x6640, 0x00, 1},  // Axis 1 STO
  {0x6650, 0x01, 1},  // Axis 1 SS1(1)
  {0x6670, 0x01, 1},  // Axis 1 SS2(1)
  {0x6668, 0x01, 1},  // Axis 1 SOS(1)
  {0x6680, 0x01, 1},  // Axis 1 SSR(1)
  {0x66d0, 0x00, 1},  // Axis 1 SDI_p
  {0x66d1, 0x00, 1},  // Axis 1 SDI_n
  {0x6632, 0x00, 1},  // Axis 1 Error_Ack
  LCEC_AX5805_PAD8,
  {0xe700, 0x03, 16}  // FSOE Master CRC_0
};

/** @brief RxPDO 0x1600, axis 2 block plus its CRC (two-axis module only). */
static const lcec_ax5805_pdo_entry_t rx_axis2[] = {
  {0x6e40, 0x00, 1},  // Axis 2 STO
  {0x6e50, 0x01, 1},  // Axis 2 SS1(1)
  {0x6e70, 0x01, 1},  // Axis 2 SS2(1)
  {0x6e68, 0x01, 1},  // Axis 2 SOS(1)
  {0x6e80, 0x01, 1},  // Axis 2 SSR(1)
  {0x6ed0, 0x00, 1},  // Axis 2 SDI_p
  {0x6ed1, 0x00, 1},  // Axis 2 SDI_n
  {0x6e32, 0x00, 1},  // Axis 2 Error_Ack
  LCEC_AX5805_PAD8,
  {0xe700, 0x04, 16}  // FSOE Master CRC_1
};

/** @brief TxPDO 0x1A00 (inputs, card to master), axis 1 block plus its CRC. */
static const lcec_ax5805_pdo_entry_t tx_axis1[] = {
  {0x6640, 0x00, 1},  // Axis 1 STO
  {0x66e0, 0x01, 1},  // Axis 1 SSM(1)
  {0x66e0, 0x02, 1},  // Axis 1 SSM(2)
  {0x6668, 0x01, 1},  // Axis 1 SOS(1)
  {0x6680, 0x01, 1},  // Axis 1 SSR(1)
  {0x66d0, 0x00, 1},  // Axis 1 SDI_p
  {0x66d1, 0x00, 1},  // Axis 1 SDI_n
  {0x6632, 0x00, 1},  // Axis 1 Error
  LCEC_AX5805_PAD8,
  {0xe600, 0x03, 16}  // FSOE Slave CRC_0
};

/** @brief TxPDO 0x1A00, axis 2 block plus its CRC (two-axis module only). */
static const lcec_ax5805_pdo_entry_t tx_axis2[] = {
  {0x6e40, 0x00, 1},  // Axis 2 STO
  {0x6ee0, 0x01, 1},  // Axis 2 SSM(1)
  {0x6ee0, 0x02, 1},  // Axis 2 SSM(2)
  {0x6e68, 0x01, 1},  // Axis 2 SOS(1)
  {0x6e80, 0x01, 1},  // Axis 2 SSR(1)
  {0x6ed0, 0x00, 1},  // Axis 2 SDI_p
  {0x6ed1, 0x00, 1},  // Axis 2 SDI_n
  {0x6e32, 0x00, 1},  // Axis 2 Error
  LCEC_AX5805_PAD8,
  {0xe600, 0x04, 16}  // FSOE Slave CRC_1
};

/**
 * @brief Safety data transparency for one axis of the AX5805.
 *
 * The card's safety payload is one byte per axis in each direction, laid out
 * the same way in both: bit 0 STO, then SS1/SS2/SOS/SSR and SDI, and bit 7 the
 * error.  Commanded and reported are both published because they are separate
 * quantities - the status is the card's own state, not an echo of the command,
 * and the two are observed to differ - so a bit is only meaningful next to its
 * counterpart.  The whole byte is published alongside the named bits so the
 * remaining functions never need a driver change to inspect.
 *
 * Values are passed through unaltered; see the note in the file header.
 */
typedef struct {
  stmak_hal_u32_t *cmd_data;   /**< HAL OUT: commanded safety byte (master to card). */
  stmak_hal_bit_t *cmd_sto;    /**< HAL OUT: commanded STO bit, as transmitted. */
  stmak_hal_bit_t *cmd_err_ack; /**< HAL OUT: commanded error acknowledge. */
  stmak_hal_u32_t *sts_data;   /**< HAL OUT: reported safety byte (card to master). */
  stmak_hal_bit_t *sts_sto;    /**< HAL OUT: reported STO bit, as received. */
  stmak_hal_bit_t *sts_err;    /**< HAL OUT: reported error state. */
} lcec_ax5805_axis_t;

/** @brief Bit position of STO within an AX5805 safety data byte. */
#define LCEC_AX5805_BIT_STO 0
/** @brief Bit position of the error / error-acknowledge flag. */
#define LCEC_AX5805_BIT_ERR 7

/**
 * @brief Internal HAL data for the AX5805 TwinSAFE card.
 */
typedef struct {
  lcec_syncs_t syncs;                    /**< Declared sync-manager / PDO layout. */
  lcec_ax5805_axis_t axes[2];            /**< Per-axis safety data transparency. */

  stmak_hal_u32_t *fsoe_master_cmd;      /**< HAL OUT: FSoE master command byte. */
  stmak_hal_u32_t *fsoe_master_crc0;     /**< HAL OUT: FSoE master CRC for channel 0. */
  stmak_hal_u32_t *fsoe_master_crc1;     /**< HAL OUT: FSoE master CRC for channel 1 (dual-axis only). */
  stmak_hal_u32_t *fsoe_master_connid;   /**< HAL OUT: FSoE master connection ID. */

  stmak_hal_u32_t *fsoe_slave_cmd;       /**< HAL OUT: FSoE slave command byte. */
  stmak_hal_u32_t *fsoe_slave_crc0;      /**< HAL OUT: FSoE slave CRC for channel 0. */
  stmak_hal_u32_t *fsoe_slave_crc1;      /**< HAL OUT: FSoE slave CRC for channel 1 (dual-axis only). */
  stmak_hal_u32_t *fsoe_slave_connid;    /**< HAL OUT: FSoE slave connection ID. */


  unsigned int fsoe_master_cmd_os;    /**< PDO byte offset: FSoE master command. */
  unsigned int fsoe_master_crc0_os;   /**< PDO byte offset: FSoE master CRC channel 0. */
  unsigned int fsoe_master_crc1_os;   /**< PDO byte offset: FSoE master CRC channel 1. */
  unsigned int fsoe_master_connid_os; /**< PDO byte offset: FSoE master connection ID. */

  unsigned int fsoe_slave_cmd_os;     /**< PDO byte offset: FSoE slave command. */
  unsigned int fsoe_slave_crc0_os;    /**< PDO byte offset: FSoE slave CRC channel 0. */
  unsigned int fsoe_slave_crc1_os;    /**< PDO byte offset: FSoE slave CRC channel 1. */
  unsigned int fsoe_slave_connid_os;  /**< PDO byte offset: FSoE slave connection ID. */


} lcec_ax5805_data_t;

static const lcec_pindesc_t slave_pins_1ch[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_cmd), "%s.%s.%s.fsoe-master-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_crc0), "%s.%s.%s.fsoe-master-crc" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_connid), "%s.%s.%s.fsoe-master-connid" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_cmd), "%s.%s.%s.fsoe-slave-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_crc0), "%s.%s.%s.fsoe-slave-crc" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_connid), "%s.%s.%s.fsoe-slave-connid" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

static const lcec_pindesc_t slave_pins_2ch[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_cmd), "%s.%s.%s.fsoe-master-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_crc0), "%s.%s.%s.fsoe-master-crc-0" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_crc1), "%s.%s.%s.fsoe-master-crc-1" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_master_connid), "%s.%s.%s.fsoe-master-connid" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_cmd), "%s.%s.%s.fsoe-slave-cmd" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_crc0), "%s.%s.%s.fsoe-slave-crc-0" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_crc1), "%s.%s.%s.fsoe-slave-crc-1" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_data_t, fsoe_slave_connid), "%s.%s.%s.fsoe-slave-connid" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

/** @brief Per-axis safety transparency pins; %d is the axis index. */
static const lcec_pindesc_t axis_pins[] = {
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_axis_t, cmd_data), "%s.%s.%s.fsoe-cmd-data-%d" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_ax5805_axis_t, cmd_sto), "%s.%s.%s.fsoe-cmd-sto-%d" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_ax5805_axis_t, cmd_err_ack), "%s.%s.%s.fsoe-cmd-err-ack-%d" },
  { STMAK_HAL_U32, STMAK_HAL_OUT, offsetof(lcec_ax5805_axis_t, sts_data), "%s.%s.%s.fsoe-sts-data-%d" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_ax5805_axis_t, sts_sto), "%s.%s.%s.fsoe-sts-sto-%d" },
  { STMAK_HAL_BIT, STMAK_HAL_OUT, offsetof(lcec_ax5805_axis_t, sts_err), "%s.%s.%s.fsoe-sts-err-%d" },
  { STMAK_HAL_TYPE_UNSPECIFIED, STMAK_HAL_DIR_UNSPECIFIED, -1, NULL }
};

void lcec_ax5805_read(struct lcec_slave *slave, long period) STMAK_NONBLOCKING;

static int init_syncs(struct lcec_slave *slave, lcec_ax5805_data_t *hal_data);

/**
 * @brief Find the safety module ident the init commands select.
 *
 * Walks the slave's SDO startup-configuration list for @c 0x2F10 and returns
 * its little-endian U32 value.  The list is built from @c \<initCmds\> (and any
 * @c \<sdoConfig\>) before proc_preinit runs, so the mode is known in time to
 * size the process image.
 *
 * @param slave  Slave to inspect.
 * @param ident  Receives the module ident on success.
 * @return 0 on success, -ENOENT if no usable 0x2F10 command is configured.
 */
static int find_module_ident(struct lcec_slave *slave, uint32_t *ident) {
  lcec_slave_sdoconf_t *sdo;

  for (sdo = slave->sdo_config; sdo != NULL && sdo->index != 0xffff;
       sdo = (lcec_slave_sdoconf_t *)&sdo->data[sdo->length]) {
    if (sdo->index != LCEC_AX5805_IDX_MODULE_SEL || sdo->length != 4) {
      continue;
    }
    *ident = ((uint32_t)sdo->data[0]) | ((uint32_t)sdo->data[1] << 8) |
             ((uint32_t)sdo->data[2] << 16) | ((uint32_t)sdo->data[3] << 24);
    return 0;
  }

  return -ENOENT;
}

int lcec_ax5805_preinit(struct lcec_slave *slave) {
  lcec_master_t *master = slave->master;
  struct lcec_slave *ax5n_slave;
  uint32_t ident;
  int axes;

  // try to find corresponding ax5n
  ax5n_slave = lcec_slave_by_index(master, slave->index - 1);
  if (ax5n_slave == NULL) {
    LCEC_ERR(master, "%s.%s: Unable to find corresponding AX5nxx with index %d.", master->name, slave->name, slave->index - 1);
    return -EINVAL;
  }

  // check for AX5nxx
  if (ax5n_slave->proc_preinit != lcec_ax5100_preinit && ax5n_slave->proc_preinit != lcec_ax5200_preinit) {
    LCEC_ERR(master, "%s.%s: Slave with index %d is not an AX5nxx.", master->name, slave->name, ax5n_slave->index);
    return -EINVAL;
  }

  // The mode comes from the init commands, not from the drive model: a card in
  // a dual-axis drive can legitimately run a one-axis safety module, and only
  // 0x2F10 says which.
  if (find_module_ident(slave, &ident) != 0) {
    LCEC_ERR(master, "%s.%s: no 0x2F10 module select configured. The AX5805 needs its TwinCAT"
        " init command export, referenced with <initCmds filename=\"...\"/>.", master->name, slave->name);
    return -EINVAL;
  }

  axes = LCEC_AX5805_MODULE_AXES(ident);
  if (axes != 1 && axes != 2) {
    LCEC_ERR(master, "%s.%s: unsupported 0x2F10 module ident 0x%08x (expected a 1- or 2-axis safety module).",
        master->name, slave->name, ident);
    return -EINVAL;
  }
  slave->fsoeConf = (axes >= 2) ? &fsoe_conf_2ch : &fsoe_conf_1ch;

  // set PDO count
  // 4 base entries (cmd + connid, each direction) plus a CRC pair per channel.
  // The safety data itself is not registered - see lcec_ax5805_read().
  slave->pdo_entry_count = 4 + 2 * slave->fsoeConf->data_channels;

  return 0;
}

int lcec_ax5805_init(int comp_id, struct lcec_slave *slave, ec_pdo_entry_reg_t **pdo_entry_regs) {
  lcec_master_t *master = slave->master;
  const cmod_env_t *env = master->env;
  lcec_ax5805_data_t *hal_data;
  int err, i;
  const lcec_pindesc_t *slave_pins;

  // initialize callbacks
  slave->proc_read = lcec_ax5805_read;

  // alloc hal memory
  if ((hal_data = env->hal->malloc(env->hal->ctx, sizeof(lcec_ax5805_data_t))) == NULL) {
    LCEC_ERR(master, "hal_malloc() for slave %s.%s failed", master->name, slave->name);
    return -EIO;
  }
  memset(hal_data, 0, sizeof(lcec_ax5805_data_t));
  slave->hal_data = hal_data;

  // initialize POD entries
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE700, 0x01, &hal_data->fsoe_master_cmd_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE700, 0x02, &hal_data->fsoe_master_connid_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE600, 0x01, &hal_data->fsoe_slave_cmd_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE600, 0x02, &hal_data->fsoe_slave_connid_os, NULL);

  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE700, 0x03, &hal_data->fsoe_master_crc0_os, NULL);
  LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE600, 0x03, &hal_data->fsoe_slave_crc0_os, NULL);

  if (slave->fsoeConf->data_channels >= 2) {
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE700, 0x04, &hal_data->fsoe_master_crc1_os, NULL);
    LCEC_PDO_INIT(pdo_entry_regs, slave->index, slave->vid, slave->pid, 0xE600, 0x04, &hal_data->fsoe_slave_crc1_os, NULL);

    slave_pins = slave_pins_2ch;
  } else {
    slave_pins = slave_pins_1ch;
  }

  // export pins
  if ((err = lcec_pin_newf_list(env, comp_id, hal_data, slave_pins, master->instance_name, master->name, slave->name)) != 0) {
    return err;
  }

  // export per-axis safety transparency pins
  for (i = 0; i < slave->fsoeConf->data_channels; i++) {
    if ((err = lcec_pin_newf_list(env, comp_id, &hal_data->axes[i], axis_pins,
            master->instance_name, master->name, slave->name, i)) != 0) {
      return err;
    }
  }

  // Declare the process image the 0x2F10 module select will produce.  Without
  // this the master keeps the layout it scanned before the init commands ran.
  if ((err = init_syncs(slave, hal_data)) != 0) {
    return err;
  }

  return 0;
}

/**
 * @brief Add one safety PDO entry block to the sync-manager declaration.
 *
 * @param syncs    Builder to append to.
 * @param entries  Entry table.
 * @param count    Number of entries in @p entries.
 */
static void add_entries(lcec_syncs_t *syncs, const lcec_ax5805_pdo_entry_t *entries, size_t count) {
  size_t i;

  for (i = 0; i < count; i++) {
    lcec_syncs_add_pdo_entry(syncs, entries[i].index, entries[i].subindex, entries[i].bits);
  }
}

/**
 * @brief Declare the sync managers for the selected safety module.
 *
 * Mirrors the module definitions in the AX5805 ESI: SM2 carries the safety
 * RxPDO 0x1600 plus one standard PDO per axis, SM3 the same on the input side.
 * The assignment must match the card exactly - 0x1C12 / 0x1C13 are read-only,
 * so a declaration that disagrees makes the master attempt a write it cannot
 * do, and the slave configuration fails.
 *
 * @param slave     Slave being configured.
 * @param hal_data  Driver data holding the builder.
 * @return 0 on success, negative errno on failure.
 */
static int init_syncs(struct lcec_slave *slave, lcec_ax5805_data_t *hal_data) {
  lcec_master_t *master = slave->master;
  lcec_syncs_t *syncs = &hal_data->syncs;
  int two_axis = (slave->fsoeConf->data_channels >= 2);

  lcec_syncs_init(syncs, master);
    lcec_syncs_add_sync(syncs, EC_DIR_OUTPUT, EC_WD_DEFAULT);
    lcec_syncs_add_sync(syncs, EC_DIR_INPUT, EC_WD_DEFAULT);

    lcec_syncs_add_sync(syncs, EC_DIR_OUTPUT, EC_WD_DEFAULT);
      lcec_syncs_add_pdo_info(syncs, 0x1600);                    // safety outputs
        lcec_syncs_add_pdo_entry(syncs, 0xe700, 0x01, 8);        // FSOE command
        add_entries(syncs, rx_axis1, sizeof(rx_axis1) / sizeof(rx_axis1[0]));
        if (two_axis) {
          add_entries(syncs, rx_axis2, sizeof(rx_axis2) / sizeof(rx_axis2[0]));
        }
        lcec_syncs_add_pdo_entry(syncs, 0xe700, 0x02, 16);       // FSOE ConnID
      lcec_syncs_add_pdo_info(syncs, 0x1601);                    // standard outputs, axis 1
        lcec_syncs_add_pdo_entry(syncs, 0x2050, 0x00, 32);
        lcec_syncs_add_pdo_entry(syncs, 0x2051, 0x00, 32);
      if (two_axis) {
        lcec_syncs_add_pdo_info(syncs, 0x1602);                  // standard outputs, axis 2
          lcec_syncs_add_pdo_entry(syncs, 0x2850, 0x00, 32);
          lcec_syncs_add_pdo_entry(syncs, 0x2851, 0x00, 32);
      }

    lcec_syncs_add_sync(syncs, EC_DIR_INPUT, EC_WD_DEFAULT);
      lcec_syncs_add_pdo_info(syncs, 0x1a00);                    // safety inputs
        lcec_syncs_add_pdo_entry(syncs, 0xe600, 0x01, 8);        // FSOE command
        add_entries(syncs, tx_axis1, sizeof(tx_axis1) / sizeof(tx_axis1[0]));
        if (two_axis) {
          add_entries(syncs, tx_axis2, sizeof(tx_axis2) / sizeof(tx_axis2[0]));
        }
        lcec_syncs_add_pdo_entry(syncs, 0xe600, 0x02, 16);       // FSOE ConnID
      lcec_syncs_add_pdo_info(syncs, 0x1a01);                    // standard inputs, axis 1
        lcec_syncs_add_pdo_entry(syncs, 0x6611, 0x00, 32);
        lcec_syncs_add_pdo_entry(syncs, 0x6613, 0x00, 32);
      if (two_axis) {
        lcec_syncs_add_pdo_info(syncs, 0x1a02);                  // standard inputs, axis 2
          lcec_syncs_add_pdo_entry(syncs, 0x6e11, 0x00, 32);
          lcec_syncs_add_pdo_entry(syncs, 0x6e13, 0x00, 32);
      }

  if (syncs->overflow) {
    LCEC_ERR(master, "%s.%s: PDO layout exceeds the lcec_syncs_t capacity", master->name, slave->name);
    return -ENOMEM;
  }

  slave->sync_info = &syncs->syncs[0];

  return 0;
}

void lcec_ax5805_read(struct lcec_slave *slave, long period) {
  lcec_master_t *master = slave->master;
  lcec_ax5805_data_t *hal_data = (lcec_ax5805_data_t *) slave->hal_data;
  uint8_t *pd = master->process_data;
  lcec_ax5805_axis_t *axis;
  uint8_t cmd, sts;
  int i;

  copy_fsoe_data(slave, hal_data->fsoe_slave_cmd_os, hal_data->fsoe_master_cmd_os);

  *(hal_data->fsoe_master_cmd) = EC_READ_U8(&pd[hal_data->fsoe_master_cmd_os]);
  *(hal_data->fsoe_master_connid) = EC_READ_U16(&pd[hal_data->fsoe_master_connid_os]);
  *(hal_data->fsoe_slave_cmd) = EC_READ_U8(&pd[hal_data->fsoe_slave_cmd_os]);
  *(hal_data->fsoe_slave_connid) = EC_READ_U16(&pd[hal_data->fsoe_slave_connid_os]);

  *(hal_data->fsoe_master_crc0) = EC_READ_U16(&pd[hal_data->fsoe_master_crc0_os]);
  *(hal_data->fsoe_slave_crc0) = EC_READ_U16(&pd[hal_data->fsoe_slave_crc0_os]);

  if (slave->fsoeConf->data_channels >= 2) {
    *(hal_data->fsoe_master_crc1) = EC_READ_U16(&pd[hal_data->fsoe_master_crc1_os]);
    *(hal_data->fsoe_slave_crc1) = EC_READ_U16(&pd[hal_data->fsoe_slave_crc1_os]);
  }

  // Per-axis safety data, both directions.
  //
  // These are located in the frame rather than registered as PDO entries.  The
  // same object carries the command in 0x1600 and the status in 0x1A00, so a
  // registration resolves to whichever sync manager the master walks first -
  // SM2, the command - and there is no way to ask for the status copy.  That is
  // what the old fsoe-in-sto pins were unknowingly reporting.  The frame is
  //     cmd(1) | [ data(2) crc(2) ] * axes | connid(2)
  // so axis a's byte sits at cmd + 1 + a * 4.  Derived here and not in init
  // because LCEC_PDO_INIT() only records where the master should later store an
  // offset; nothing is resolved until ecrt_domain_reg_pdo_entry_list() runs.
  for (i = 0; i < slave->fsoeConf->data_channels; i++) {
    axis = &hal_data->axes[i];
    cmd = EC_READ_U8(&pd[hal_data->fsoe_master_cmd_os + LCEC_FSOE_CMD_LEN
        + i * (slave->fsoeConf->master_data_len + LCEC_FSOE_CRC_LEN)]);
    sts = EC_READ_U8(&pd[hal_data->fsoe_slave_cmd_os + LCEC_FSOE_CMD_LEN
        + i * (slave->fsoeConf->slave_data_len + LCEC_FSOE_CRC_LEN)]);
    *(axis->cmd_data) = cmd;
    *(axis->cmd_sto) = (cmd >> LCEC_AX5805_BIT_STO) & 1;
    *(axis->cmd_err_ack) = (cmd >> LCEC_AX5805_BIT_ERR) & 1;
    *(axis->sts_data) = sts;
    *(axis->sts_sto) = (sts >> LCEC_AX5805_BIT_STO) & 1;
    *(axis->sts_err) = (sts >> LCEC_AX5805_BIT_ERR) & 1;
  }
}
