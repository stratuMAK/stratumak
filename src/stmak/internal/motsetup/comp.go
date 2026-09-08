// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package motsetup

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
)

// LoadJointComp parses a leadscrew-compensation file and pushes each triplet to
// motion, matching C++ usrmotLoadComp. Each data line is "nominal forward
// reverse". compType 0: the file holds nominal/forward/reverse POSITIONS and the
// motion trims are the diffs (nominal - value); any other type: the values are
// already trims and are passed through. Blank / non-triplet lines are skipped
// (C++ stops at the first such line; skipping is a strict superset that loads
// the same pure-triplet files identically and tolerates comments/headers).
func LoadJointComp(joint int32, file string, compType int, setComp func(joint int32, nominal, fwd, rev float64) error) error {
	// [JOINT_n]COMP_FILE is a configuration path: resolved server-side and
	// contained by the shared rule (internal/pathres).
	path, err := pathres.Resolve(file, pathres.Read)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		nom, e1 := strconv.ParseFloat(fields[0], 64)
		fwd, e2 := strconv.ParseFloat(fields[1], 64)
		rev, e3 := strconv.ParseFloat(fields[2], 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		if compType == 0 {
			fwd = nom - fwd // positions -> trims (diffs), as C++ does
			rev = nom - rev
		}
		if err := setComp(joint, nom, fwd, rev); err != nil {
			return err
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no compensation triplets found")
	}
	return nil
}
