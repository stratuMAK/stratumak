/**
 * @file data_ok.h
 * @brief The cycle count behind a master's @c data-ok pin.
 *
 * On its own, without the rest of the driver, so that a test can compile it
 * (tests/ethercat/data-ok-count).
 *
 * @copyright Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
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
 */

#ifndef _LCEC_DATA_OK_H_
#define _LCEC_DATA_OK_H_

/** @brief Default for the master's @c dataLossCycles attribute. */
#define LCEC_DATA_LOSS_CYCLES_DEFAULT 3

/**
 * @brief Advance the data-ok count by one cycle.
 *
 * High on every cycle whose working counter is complete; low once
 * @p loss_cycles cycles in a row were not.  A single lost frame therefore
 * does not take it down (with @p loss_cycles > 1), and the first complete
 * cycle brings it back.
 *
 * @param bad_cycles  Incomplete cycles in a row, saturating at
 *                    @p loss_cycles.  Start it at @p loss_cycles, so the pin
 *                    is low until the first complete cycle.
 * @param complete    Non-zero when this cycle's working counter is complete.
 * @param loss_cycles Incomplete cycles in a row that make it low (>= 1).
 * @return Non-zero for data-ok.
 */
static inline int lcec_data_ok_update(int *bad_cycles, int complete, int loss_cycles) {
  if (complete) {
    *bad_cycles = 0;
    return 1;
  }
  if (*bad_cycles < loss_cycles) {
    (*bad_cycles)++;
  }
  return *bad_cycles < loss_cycles;
}

#endif /* _LCEC_DATA_OK_H_ */
