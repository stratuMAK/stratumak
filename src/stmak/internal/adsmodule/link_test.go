// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package adsmodule

// Blank-import hallibtest so this package's test binary links the HAL C symbols
// referenced by the cgo pkg/hal import (this package calls hal.NewComponent).
// See internal/hallib/hallibtest for why one such file per test binary is
// required.
import _ "github.com/stratuMAK/stratumak/src/stmak/internal/hallib/hallibtest"
