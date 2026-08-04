/**
 * @file lcec_ecrt.h
 * @brief Single include point for the EtherLab master header, with the
 *        RT-annotation hand-over applied.
 *
 * The master library declares its documented realtime interface with
 * ECRT_RT_ATTR (master @c include/ecrt.h -> @c ecrt_rt.h), an overrideable
 * annotation macro.  Defining it as STMAK_NONBLOCKING @em before @c ecrt.h
 * is first pulled hands those annotations to stmak's function-effects check
 * ("make rt-effects-check"): the documented rt_safe subset
 * (send/receive/domain/DC/state + the real/lreal PDO accessors) verifies
 * when called from RT context, and any other ecrt call (SDO/EoE/config)
 * from RT code is a diagnosable error.
 *
 * Every lcec translation unit must reach @c ecrt.h through this header
 * (never @c "ecrt.h" directly), so the override is in force before the
 * first declaration is seen regardless of include order.  Because @c
 * ecrt_rt.h guards its own definition with @c \#ifndef ECRT_RT_ATTR,
 * routing @c conf.h and @c lcec.h through here also keeps the build free
 * of the "ECRT_RT_ATTR redefined" warning that a late override triggers.
 *
 * Replaces the former hand-maintained @c ecrt_rt_api.h re-declaration of
 * the RT subset, which had to be kept in sync with the master by hand.
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

#ifndef _LCEC_ECRT_H_
#define _LCEC_ECRT_H_

#include "stmak/pkg/cmodule/stmak_rt_check.h"

#ifndef ECRT_RT_ATTR
#define ECRT_RT_ATTR STMAK_NONBLOCKING
#endif

#include "ecrt.h"

#endif /* _LCEC_ECRT_H_ */
