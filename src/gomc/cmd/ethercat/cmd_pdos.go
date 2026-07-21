// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"fmt"
)

func init() {
	registerCommand(&Command{
		Name:  "pdos",
		Brief: "List Sync Manager PDO assignment.",
		Run:   cmdPdos,
	})
}

func cmdPdos(client *EthercatClient, opts *GlobalOpts, args []string) error {
	masterIndex := parseMasterIndex(opts.Masters)
	master, err := client.GetMaster(masterIndex)
	if err != nil {
		return err
	}

	positions := parsePositionList(opts.Positions)
	if positions == nil {
		positions = make([]uint16, master.SlaveCount)
		for i := range positions {
			positions[i] = uint16(i)
		}
	}

	for _, pos := range positions {
		slave, err := client.GetSlave(masterIndex, pos)
		if err != nil {
			return err
		}
		for smIdx := uint32(0); smIdx < uint32(slave.SyncCount); smIdx++ {
			sm, err := client.GetSlaveSync(masterIndex, pos, smIdx)
			if err != nil {
				continue
			}
			enableStr := uint8(0)
			if sm.Enable {
				enableStr = 1
			}
			// Match the IgH tool's format exactly (CommandPdos.cpp): no
			// direction annotation on the SM line, DefaultSize right-aligned
			// to width 4.
			fmt.Printf("SM%d: PhysAddr 0x%04x, DefaultSize %4d, ControlRegister 0x%02x, Enable %d\n",
				smIdx, sm.PhysicalStartAddress, sm.DefaultSize, sm.ControlRegister, enableStr)

			for pdoIdx := uint32(0); pdoIdx < uint32(sm.PdoCount); pdoIdx++ {
				pdo, err := client.GetSlaveSyncPdo(masterIndex, pos, smIdx, pdoIdx)
				if err != nil {
					continue
				}
				// SM control-register direction bit (0x04): set = process data
				// output (master->slave) = RxPDO, clear = input = TxPDO
				// (CommandPdos.cpp: control_register & 0x04 ? "R" : "T").
				pdoType := "TxPDO"
				if sm.ControlRegister&0x04 != 0 {
					pdoType = "RxPDO"
				}
				fmt.Printf("  %s 0x%04x \"%s\"\n", pdoType, pdo.Index, pdo.Name)

				for entryIdx := uint32(0); entryIdx < uint32(pdo.EntryCount); entryIdx++ {
					entry, err := client.GetSlaveSyncPdoEntry(masterIndex, pos, smIdx, pdoIdx, entryIdx)
					if err != nil {
						continue
					}
					fmt.Printf("    PDO entry 0x%04x:%02x, %2d bit, \"%s\"\n",
						entry.Index, entry.Subindex, entry.BitLength, entry.Name)
				}
			}
		}
	}
	return nil
}
