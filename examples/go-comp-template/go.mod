module mygomodule

go 1.22

require github.com/stratuMAK/stratumak/src/stmak v0.0.0

// This replace is overridden by go.work during local builds (make compile).
// It is deleted when the module is installed via modcompile add-gomod.
replace github.com/stratuMAK/stratumak/src/stmak => ./src/stmak
