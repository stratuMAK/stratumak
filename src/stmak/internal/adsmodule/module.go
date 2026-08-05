// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package adsmodule registers the ADS/AMS protocol server module with the stratuMAK
// module registry. When compiled into the stmakd binary, this package's
// init() function registers a factory that creates ADS server instances in
// response to HAL "load ads-server" commands.
//
// Usage in a HAL file:
//
//	load ads-server [ads] config=galv-hmi.conf
//	load ads-server [ads] config=galv-hmi.conf debug=1
//
// Parameters:
//   - config=<path>  — path to the ADS .conf file (required; resolved relative
//     to the INI file directory if not absolute)
//   - debug=1        — enable verbose ADS protocol logging
package adsmodule

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/ads"
	"github.com/stratuMAK/stratumak/src/stmak/internal/adsbridge"
	"github.com/stratuMAK/stratumak/src/stmak/internal/adsconfig"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/hal"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

// DefaultAMSPort is the default AMS port used for the ADS server.
const DefaultAMSPort = 851

func init() {
	stmak.RegisterModule("ads-server", newADSModule)
}

// adsModule implements stmak.Module for the ADS server.
type adsModule struct {
	logger  *slog.Logger
	comp    *hal.Component
	server  *ads.Server
	bridge  *adsbridge.Bridge
	symbols *ads.SymbolTable
	conf    *adsconfig.ServerConf
}

func (m *adsModule) Start() error {
	if err := m.server.Start(); err != nil {
		return fmt.Errorf("ADS %q: start server: %w", m.conf.Name, err)
	}
	m.logger.Info("ADS server started", "name", m.conf.Name)
	return nil
}

func (m *adsModule) Stop() {
	m.logger.Debug("stopping ADS server", "name", m.conf.Name)
	m.server.Stop()
}

func (m *adsModule) Destroy() {
	if m.comp != nil {
		if err := m.comp.Exit(); err != nil {
			m.logger.Debug("ADS HAL component exit error", "name", m.conf.Name, "error", err)
		}
	}
}

// parseArgs extracts key=value parameters from the load command args.
func parseArgs(args []string) (configPath string, debug bool) {
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		switch k {
		case "config":
			configPath = v
		case "debug":
			debug = v == "1"
		}
	}
	return
}

func newADSModule(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	configPath, debug := parseArgs(args)
	if configPath == "" {
		return nil, fmt.Errorf("ads-server: missing required config= parameter")
	}

	// Configuration paths are server-side paths resolved by the shared rule
	// (config dir, then HALLIB_PATH, contained within them) — see
	// internal/pathres.
	configPath, err := pathres.Resolve(configPath, pathres.Read)
	if err != nil {
		return nil, fmt.Errorf("ads-server: config=: %w", err)
	}

	logger = logger.With("plugin", name)
	logger.Info("loading ADS config", "path", configPath)

	conf, aliases, tree, err := adsconfig.ParseConfFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("ADS config %q: %w", configPath, err)
	}

	// Override the conf name with the instance name from the load command.
	conf.Name = name

	// Compute layout (byte offsets for all symbols).
	pins, err := adsconfig.ComputeLayout(tree, aliases)
	if err != nil {
		return nil, fmt.Errorf("ADS config %q: computing layout: %w", configPath, err)
	}

	// Create HAL component.
	comp, err := hal.NewComponent(name)
	if err != nil {
		return nil, fmt.Errorf("ADS %q: creating HAL component: %w", name, err)
	}
	// Release the HAL component if construction fails after this point, so a
	// failed load does not leak a HAL component slot (docs/dev/ADS_REVIEW_FINDINGS.md A12).
	ok := false
	defer func() {
		if !ok {
			if exitErr := comp.Exit(); exitErr != nil {
				logger.Debug("ADS HAL component exit error during failed load", "name", name, "error", exitErr)
			}
		}
	}()

	// Create symbol table and bridge (HAL pins + ADS symbol registrations).
	st := ads.NewSymbolTable()
	bridge, err := adsbridge.NewBridge(comp, pins, st, aliases)
	if err != nil {
		return nil, fmt.Errorf("ADS %q: creating bridge: %w", name, err)
	}

	// Apply struct/enum type info to container groups.
	adsbridge.ApplyContainerTypeInfo(tree, "", st, aliases)

	// Mark HAL component ready so that HAL files can wire its pins.
	if err := comp.Ready(); err != nil {
		return nil, fmt.Errorf("ADS %q: hal ready: %w", name, err)
	}

	// Parse AMS Net ID.
	netID, err := ads.ParseAMSNetID(conf.AMSNetID)
	if err != nil {
		return nil, fmt.Errorf("ADS %q: invalid AMS Net ID %q: %w", name, conf.AMSNetID, err)
	}

	// Create ADS TCP server (not started yet).
	addr := fmt.Sprintf("%s:%d", conf.Bind, conf.Port)
	server := ads.NewServer(addr, netID, DefaultAMSPort, st, conf.MaxConnections, conf.MaxSubscriptions, debug, logger)

	logger.Info("ADS instance initialized",
		"name", name,
		"addr", addr,
		"ams-net-id", conf.AMSNetID,
		"pins", len(pins),
		"max-connections", conf.MaxConnections,
		"max-subscriptions", conf.MaxSubscriptions,
	)

	ok = true // construction succeeded — keep the HAL component
	return &adsModule{
		logger:  logger,
		comp:    comp,
		server:  server,
		bridge:  bridge,
		symbols: st,
		conf:    conf,
	}, nil
}
