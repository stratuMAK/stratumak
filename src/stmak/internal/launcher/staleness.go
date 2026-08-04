// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package launcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// stmakVersionFile is the version stamp installed alongside the stratuMAK sources.
// It records which release those sources came from, which is the only way a
// running server can tell whether it was built from them or from an older set.
const stmakVersionFile = "VERSION"

// WarnIfStale reports a locally rebuilt stmakd that no longer matches the
// installed stratuMAK sources.
//
// A server built from the sources of an older release goes on running happily
// against newer C modules, and the mismatch shows up later as behaviour nobody
// can account for. The version is baked into each binary at build time, so
// comparing it against the installed tree's stamp costs one file read.
//
// This is a warning, not a refusal: the skew it detects is usually benign, and
// the administrator is the one who decides when to spend the minutes a rebuild
// takes. The fatal case — a C module whose ABI the server does not speak — is
// refused at load time instead, by checkCModuleABI.
func WarnIfStale(logger *slog.Logger) {
	// Only a layout that keeps a package-owned server separate from the
	// running one can be stale in this sense. Elsewhere the binary and the
	// sources are rebuilt together by whoever owns both.
	localBin := config.LocalBinDir()
	if localBin == "" {
		return
	}

	// A symlink means no local rebuild has happened: the package's own binary
	// is what runs, and an upgrade of it is live the moment dpkg unpacks it.
	fi, err := os.Lstat(filepath.Join(localBin, "stmakd"))
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return
	}

	data, err := os.ReadFile(filepath.Join(config.StmakDir(), stmakVersionFile))
	if err != nil {
		// No stamp to compare against — an installation from before the
		// stamp existed. Nothing can be concluded, so nothing is said.
		return
	}
	installed := strings.TrimSpace(string(data))
	if installed == "" || installed == config.EMC2Version {
		return
	}

	logger.Warn("this stmakd was built locally from older sources; run `sudo modcompile rebuild`",
		"server_built_from", config.EMC2Version,
		"installed_sources", installed,
		"sources", config.StmakDir())
}
