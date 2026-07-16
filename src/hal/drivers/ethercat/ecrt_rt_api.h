/**
 * @file ecrt_rt_api.h
 * @brief Nonblocking re-declarations of the EtherLab master's RT API.
 *
 * The IgH/EtherLab master documents a subset of its application interface
 * as callable from realtime context ("realtime interface"): the cyclic
 * send/receive/domain functions, the distributed-clock functions and the
 * cached state getters.  Their declarations in ecrt.h carry no
 * function-effects annotation (the master is an external project — its
 * own verification is a separate work item), so this header re-declares
 * exactly that RT subset with GOMC_NONBLOCKING.  C merges attributes on
 * compatible re-declarations, so lcec RT code calling these functions
 * passes "make rt-effects-check".
 *
 * TRUST BOUNDARY: the implementations are NOT verified by the effects
 * check.  Justification: the EtherLab documentation designates these
 * functions as realtime-safe; in the userspace master library they
 * perform ioctl-based process-data exchange whose latency behaviour is
 * an accepted property of the chosen master architecture, audited
 * separately (see RT_HARDENING_CHECKLIST.md).
 *
 * Any ecrt function NOT re-declared here (SDO/EoE/config/request APIs)
 * stays unannotated on purpose: calling it from lcec RT code is a
 * diagnosable error.
 *
 * @copyright Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: GPL Version 2
 */

#ifndef _LCEC_ECRT_RT_API_H_
#define _LCEC_ECRT_RT_API_H_

#include "ecrt.h"
#include "gomc/pkg/cmodule/gomc_rt_check.h"

EC_PUBLIC_API int ecrt_master_send(ec_master_t *master) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_master_receive(ec_master_t *master) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_master_state(const ec_master_t *master,
    ec_master_state_t *state) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_master_application_time(ec_master_t *master,
    uint64_t app_time) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_master_sync_reference_clock_to(ec_master_t *master,
    uint64_t sync_time) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_master_sync_slave_clocks(ec_master_t *master) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_master_reference_clock_time(const ec_master_t *master,
    uint32_t *time) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_slave_config_state(const ec_slave_config_t *sc,
    ec_slave_config_state_t *state) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_domain_process(ec_domain_t *domain) GOMC_NONBLOCKING;
EC_PUBLIC_API int ecrt_domain_queue(ec_domain_t *domain) GOMC_NONBLOCKING;

/* PDO value accessors (pure byte-order conversion on process-data memory;
 * the integer-sized EC_READ_/EC_WRITE_ variants are plain macros). */
EC_PUBLIC_API float ecrt_read_real(const void *data) GOMC_NONBLOCKING;
EC_PUBLIC_API double ecrt_read_lreal(const void *data) GOMC_NONBLOCKING;
EC_PUBLIC_API void ecrt_write_real(void *data, float value) GOMC_NONBLOCKING;
EC_PUBLIC_API void ecrt_write_lreal(void *data, double value) GOMC_NONBLOCKING;

#endif /* _LCEC_ECRT_RT_API_H_ */
