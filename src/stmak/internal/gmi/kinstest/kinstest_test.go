// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package kinstest

import (
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
)

func TestTrivkinsLoadAndRegister(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	defer apiserver.SetDefaultRegistry(nil)

	mod, err := loadTrivkins()
	if err != nil {
		t.Fatalf("loadTrivkins: %v", err)
	}
	defer unloadTrivkins(mod)

	// The .so's New() should have called kins_api_register(name, ...) with name="trivkins".
	cbs := getKinsCallbacks()
	if cbs == nil {
		t.Fatal("kins_api_get returned nil — registration failed")
	}
}

func TestTrivkinsForward(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	defer apiserver.SetDefaultRegistry(nil)

	mod, err := loadTrivkins()
	if err != nil {
		t.Fatalf("loadTrivkins: %v", err)
	}
	defer unloadTrivkins(mod)

	cbs := getKinsCallbacks()
	if cbs == nil {
		t.Fatal("kins_api_get returned nil")
	}

	// Forward: joints → world (identity: joint[i] = axis[i])
	var joints [16]float64
	joints[0] = 10.0 // X
	joints[1] = 20.0 // Y
	joints[2] = 30.0 // Z

	world, rc := callForward(cbs, joints)
	if rc != 0 {
		t.Fatalf("forward returned %d", rc)
	}
	if float64(world.x) != 10.0 || float64(world.y) != 20.0 || float64(world.z) != 30.0 {
		t.Errorf("forward: got (%.1f, %.1f, %.1f), want (10.0, 20.0, 30.0)",
			float64(world.x), float64(world.y), float64(world.z))
	}
}

func TestTrivkinsInverse(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	defer apiserver.SetDefaultRegistry(nil)

	mod, err := loadTrivkins()
	if err != nil {
		t.Fatalf("loadTrivkins: %v", err)
	}
	defer unloadTrivkins(mod)

	cbs := getKinsCallbacks()
	if cbs == nil {
		t.Fatal("kins_api_get returned nil")
	}

	// Inverse: world → joints (identity)
	world := callForward // reuse forward to build a world pose
	_ = world

	var joints [16]float64
	joints[0] = 1.0
	joints[1] = 2.0
	joints[2] = 3.0
	pose, _ := callForward(cbs, joints)

	result, rc := callInverse(cbs, pose)
	if rc != 0 {
		t.Fatalf("inverse returned %d", rc)
	}
	if result[0] != 1.0 || result[1] != 2.0 || result[2] != 3.0 {
		t.Errorf("inverse: got (%.1f, %.1f, %.1f), want (1.0, 2.0, 3.0)",
			result[0], result[1], result[2])
	}
}

// TestTrivkinsGappedCoords exercises a lathe-style coordinates=XZ config,
// where the coordinate letters are not contiguous: axis X is Cartesian
// index 0 -> joint 0, but axis Z is Cartesian index 2 -> joint 1.  A
// straight index copy (the old bug) would drive joint 1 from axis Y and
// send axis Z to a nonexistent joint 2, so world-jogging Z moved nothing.
func TestTrivkinsGappedCoords(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	defer apiserver.SetDefaultRegistry(nil)

	mod, err := loadTrivkinsCoords("XZ")
	if err != nil {
		t.Fatalf("loadTrivkinsCoords: %v", err)
	}
	defer unloadTrivkins(mod)

	cbs := getKinsCallbacks()
	if cbs == nil {
		t.Fatal("kins_api_get returned nil")
	}

	// inverse: axis X=5, Z=7 -> joint0=5 (X), joint1=7 (Z).  The bug would
	// give joint1=0 (it read axis Y).
	world := makePose(5.0, 0.0, 7.0)
	joints, rc := callInverse(cbs, world)
	if rc != 0 {
		t.Fatalf("inverse returned %d", rc)
	}
	if joints[0] != 5.0 {
		t.Errorf("inverse joint0 (X) = %.1f, want 5.0", joints[0])
	}
	if joints[1] != 7.0 {
		t.Errorf("inverse joint1 (Z) = %.1f, want 7.0 (axis Z); 0.0 = the pre-fix bug", joints[1])
	}

	// forward: joint0=3 (X), joint1=9 (Z) -> axis X=3, Z=9, Y=0.
	var jf [16]float64
	jf[0] = 3.0
	jf[1] = 9.0
	fwd, rc := callForward(cbs, jf)
	if rc != 0 {
		t.Fatalf("forward returned %d", rc)
	}
	if float64(fwd.x) != 3.0 {
		t.Errorf("forward axis X = %.1f, want 3.0", float64(fwd.x))
	}
	if float64(fwd.z) != 9.0 {
		t.Errorf("forward axis Z = %.1f, want 9.0 (joint 1); the bug put it on Y", float64(fwd.z))
	}
	if float64(fwd.y) != 0.0 {
		t.Errorf("forward axis Y = %.1f, want 0.0 (no joint maps to Y)", float64(fwd.y))
	}
}

func TestTrivkinsType(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	defer apiserver.SetDefaultRegistry(nil)

	mod, err := loadTrivkins()
	if err != nil {
		t.Fatalf("loadTrivkins: %v", err)
	}
	defer unloadTrivkins(mod)

	cbs := getKinsCallbacks()
	if cbs == nil {
		t.Fatal("kins_api_get returned nil")
	}

	ktype, rc := callType(cbs)
	if rc != 0 {
		t.Fatalf("type returned %d", rc)
	}
	// Default kinstype='1' → KINS_IDENTITY = 1
	if ktype != 1 {
		t.Errorf("type = %d, want 1 (IDENTITY)", ktype)
	}
}

func TestTrivkinsSwitchable(t *testing.T) {
	reg := apiserver.NewRegistry()
	apiserver.SetDefaultRegistry(reg)
	defer apiserver.SetDefaultRegistry(nil)

	mod, err := loadTrivkins()
	if err != nil {
		t.Fatalf("loadTrivkins: %v", err)
	}
	defer unloadTrivkins(mod)

	cbs := getKinsCallbacks()
	if cbs == nil {
		t.Fatal("kins_api_get returned nil")
	}

	sw := callSwitchable(cbs)
	if sw != 0 {
		t.Errorf("switchable = %d, want 0 (not switchable)", sw)
	}
}
