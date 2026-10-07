// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"

	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
)

// taskPins is the HAL component named after the milltask instance itself
// ("milltask.ui-auto-disable", "left.task.ui-auto-disable"). It carries the
// pins that belong to the task rather than to halui: they exist whether or not
// halui= was given, and once per instance on a multi-instance server, where
// only one task usually exports halui.
//
// Created in the factory, like halui: the net lines that wire it run right
// after the load line, long before Start.
type taskPins struct {
	comp *hal.Component

	// uiAutoDisable withholds program flow from UIs: see uiGate.
	uiAutoDisable *hal.Pin[bool]
}

func newTaskPins(name string) (*taskPins, error) {
	comp, err := hal.NewComponent(name)
	if err != nil {
		return nil, fmt.Errorf("task pins: hal_init(%s): %w", name, err)
	}
	p := &taskPins{comp: comp}
	if p.uiAutoDisable, err = hal.NewPin[bool](comp, "ui-auto-disable", hal.In); err != nil {
		_ = comp.Exit()
		return nil, err
	}
	if err := comp.Ready(); err != nil {
		_ = comp.Exit()
		return nil, fmt.Errorf("task pins component ready: %w", err)
	}
	return p, nil
}

// check samples the input pins into the task. Called once per monitor tick.
func (p *taskPins) check(t *Task) {
	t.setUIAutoDisabled(p.uiAutoDisable.Get())
}

func (p *taskPins) exit() {
	if p.comp != nil {
		_ = p.comp.Exit()
	}
}
