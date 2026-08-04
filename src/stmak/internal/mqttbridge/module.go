// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package mqttbridge registers the MQTT bridge module with the stratuMAK module
// registry. It creates HAL pins from an XML config and bridges them
// bidirectionally to an MQTT broker.
//
// Usage in a HAL file:
//
//	load mqtt-bridge config=mqtt.xml
//	load mqtt-bridge [mqtt] config=mqtt.xml
//
// Parameters:
//   - config=<path> — path to the XML config file (required; resolved relative
//     to the INI file directory if not absolute)
package mqttbridge

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

func init() {
	stmak.RegisterModule("mqtt-bridge", newMQTTBridge)
}

// mqttModule implements stmak.Module.
type mqttModule struct {
	logger *slog.Logger
	comp   *hal.Component
	bridge *bridge
}

func (m *mqttModule) Start() error {
	return m.bridge.start()
}

func (m *mqttModule) Stop() {
	m.bridge.stop()
}

func (m *mqttModule) Destroy() {
	if m.comp != nil {
		if err := m.comp.Exit(); err != nil {
			m.logger.Debug("mqtt-bridge HAL component exit error", "error", err)
		}
	}
}

func newMQTTBridge(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	configPath := parseConfigArg(args)
	if configPath == "" {
		return nil, fmt.Errorf("mqtt-bridge: missing required config= parameter")
	}

	// Configuration paths are server-side paths resolved by the shared rule
	// (config dir, then HALLIB_PATH, contained within them) — see
	// internal/pathres.
	configPath, err := pathres.Resolve(configPath, pathres.Read)
	if err != nil {
		return nil, fmt.Errorf("mqtt-bridge: config=: %w", err)
	}

	logger = logger.With("module", name)
	logger.Info("loading MQTT bridge config", "path", configPath)

	cfg, err := parseConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("mqtt-bridge config %q: %w", configPath, err)
	}

	// Create HAL component.
	comp, err := hal.NewComponent(name)
	if err != nil {
		return nil, fmt.Errorf("mqtt-bridge %q: creating HAL component: %w", name, err)
	}

	// Create bridge (pins + MQTT wiring).
	b, err := newBridge(comp, name, cfg, logger, parseDryrunArg(args))
	if err != nil {
		_ = comp.Exit()
		return nil, fmt.Errorf("mqtt-bridge %q: %w", name, err)
	}

	// Mark HAL component ready.
	if err := comp.Ready(); err != nil {
		_ = comp.Exit()
		return nil, fmt.Errorf("mqtt-bridge %q: hal ready: %w", name, err)
	}

	logger.Info("mqtt-bridge initialized",
		"broker", cfg.Broker,
		"topics", len(cfg.Topics),
	)

	return &mqttModule{
		logger: logger,
		comp:   comp,
		bridge: b,
	}, nil
}

func parseConfigArg(args []string) string {
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if ok && k == "config" {
			return v
		}
	}
	return ""
}

// parseDryrunArg reports whether a bare "dryrun" load argument was given
// (e.g. `load mqtt-bridge config=mqtt.xml dryrun`). In dryrun the bridge never
// connects to a broker but still runs its publish loops and advances the
// publish-count pin — for offline testing/diagnostics.
func parseDryrunArg(args []string) bool {
	for _, arg := range args {
		if arg == "dryrun" {
			return true
		}
	}
	return false
}
