// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package emccalib

// Blank-import hallibtest so this package's test binary links the HAL C symbols
// referenced through the cgo pkg/hal import.  See internal/hallib/hallibtest
// for why one such file per test binary is required.
import _ "github.com/stratuMAK/stratumak/src/stmak/internal/hallib/hallibtest"
