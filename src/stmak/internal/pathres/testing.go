// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

// TestingTB is the subset of testing.TB the helper below needs, declared here
// so this package does not import "testing" into the production build.
type TestingTB interface {
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// SetDefaultForTest installs a resolver rooted at baseDir (plus any extra
// library directories) for the duration of the test, restoring the previous
// one afterwards.
//
// Tests that exercise a module's path handling need this because the launcher
// — which normally publishes the resolver — is not running.
func SetDefaultForTest(tb TestingTB, baseDir string, libDirs ...string) *Resolver {
	r, err := New(baseDir, joinDirs(libDirs))
	if err != nil {
		tb.Fatalf("pathres.SetDefaultForTest: %v", err)
		return nil
	}
	prev := Default()
	SetDefault(r)
	tb.Cleanup(func() { SetDefault(prev) })
	return r
}

// joinDirs builds a HALLIB_PATH-style list.
func joinDirs(dirs []string) string {
	out := ""
	for _, d := range dirs {
		if out != "" {
			out += ":"
		}
		out += d
	}
	return out
}
