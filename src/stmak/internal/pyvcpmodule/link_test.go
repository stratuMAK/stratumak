// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Blank-import hallibtest so this package's test binary links the HAL C symbols
// referenced through the cgo pkg/hal import. See internal/hallib/hallibtest for
// why (Go requires one such file per test binary).
package pyvcpmodule

import _ "github.com/stratuMAK/stratumak/src/stmak/internal/hallib/hallibtest"
