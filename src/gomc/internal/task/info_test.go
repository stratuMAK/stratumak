// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests for GET /info — the endpoint that exists so a UI client never has to
// guess a peer instance name or probe unqualified HAL pins.
//
// Both halves are guessing bugs that already shipped: a client deriving
// "<task>-preview" and hardcoding "tooltable" made a multi-instance config
// answer every tool table read with a 404 at 10 Hz, and probing the unqualified
// "spindle.0.forward" hid the spindle controls on every multi-instance config,
// because the real pin is "<motion-instance>.spindle.0.forward".
package task

import (
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
)

// registerFake puts an api:instance pair in the registry. Only the name pair
// matters here — checkReportedPeers resolves names, it never calls the peer.
func registerFake(t *testing.T, reg *apiserver.Registry, apiName, instance string) {
	t.Helper()
	if err := reg.RegisterNoREST(apiName, 1, instance, nil); err != nil {
		t.Fatalf("register %s:%s: %v", apiName, instance, err)
	}
}

func TestCheckReportedPeersAcceptsConfiguredNames(t *testing.T) {
	reg := apiserver.NewRegistry()
	registerFake(t, reg, "ngcpreview", "pnp.task-preview")
	registerFake(t, reg, "manualtoolchange", "pnp.mtc")
	registerFake(t, reg, "pyvcp", "pnp.panel")

	m := &milltaskModule{
		previewInstance: "pnp.task-preview",
		mtcInstance:     "pnp.mtc",
		pyvcpInstance:   "pnp.panel",
	}
	if err := m.checkReportedPeers(reg); err != nil {
		t.Fatalf("all three peers loaded, want accept, got %v", err)
	}
}

// An unset peer is a statement that the feature is absent, not a broken config.
// It is what lets the client skip the feature entirely instead of probing for
// it — the manualtoolchange probe was the one remaining legitimate 404 at AXIS
// startup.
func TestCheckReportedPeersAllowsUnset(t *testing.T) {
	reg := apiserver.NewRegistry()
	m := &milltaskModule{} // nothing configured

	if err := m.checkReportedPeers(reg); err != nil {
		t.Fatalf("no peers configured, want accept, got %v", err)
	}
}

// The whole point of resolving at startup: a typo must stop the config load,
// where the operator is looking, instead of reaching a client as a 404 later.
func TestCheckReportedPeersRejectsMissing(t *testing.T) {
	reg := apiserver.NewRegistry()
	registerFake(t, reg, "ngcpreview", "pnp.task-preview")

	m := &milltaskModule{previewInstance: "pnp.task-preveiw"} // typo
	err := m.checkReportedPeers(reg)
	if err == nil {
		t.Fatal("misspelled preview_instance accepted; it must fail the config load")
	}
	// The message has to name the parameter and offer what IS loaded, or the
	// operator is left diffing two names by eye.
	for _, want := range []string{"preview_instance", "pnp.task-preveiw", "pnp.task-preview"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Resolving by instance name alone would accept any module that happens to
// carry the name, so the check is by api:instance.
func TestCheckReportedPeersRejectsWrongKind(t *testing.T) {
	reg := apiserver.NewRegistry()
	registerFake(t, reg, "tooltable", "pnp.tt")

	m := &milltaskModule{previewInstance: "pnp.tt"}
	if err := m.checkReportedPeers(reg); err == nil {
		t.Fatal("preview_instance pointing at the tool table accepted; " +
			"the check must be by api:instance, not instance alone")
	}
}

// capsFrom must qualify every pin with this task's motion instance. Feeding it
// the unqualified names AXIS used to probe must produce no capabilities at all
// — that mismatch is exactly what hid the spindle controls.
func TestCapsFromRequiresMotionInstancePrefix(t *testing.T) {
	m := &milltaskModule{motInstance: "pnp.mot"}
	m.task = &Task{numJoints: 3}

	unqualified := map[string]bool{
		"spindle.0.forward":     true,
		"spindle.0.brake":       true,
		"joint.0.neg-lim-sw-in": true,
	}
	if caps := m.capsFrom(unqualified); caps.SpindleForward || caps.SpindleBrake || caps.LimitSwitchOverride {
		t.Errorf("unqualified pin names matched on a pnp.mot machine: %+v", caps)
	}

	qualified := map[string]bool{
		"pnp.mot.spindle.0.forward":     true,
		"pnp.mot.spindle.0.brake":       true,
		"pnp.mot.joint.2.pos-lim-sw-in": true,
	}
	caps := m.capsFrom(qualified)
	if !caps.SpindleForward || !caps.SpindleBrake || !caps.LimitSwitchOverride {
		t.Errorf("qualified pin names did not match: %+v", caps)
	}
}

// The direction pins are reported separately, because a machine that wires only
// one of them must get only the button for that direction. Collapsing them into
// one "spindle is wired" flag is what this pins against.
func TestCapsFromKeepsSpindleDirectionsApart(t *testing.T) {
	m := &milltaskModule{motInstance: "mot"}
	m.task = &Task{numJoints: 1}

	fwdOnly := m.capsFrom(map[string]bool{"mot.spindle.0.forward": true})
	if !fwdOnly.SpindleForward || fwdOnly.SpindleReverse {
		t.Errorf("forward-only machine: %+v", fwdOnly)
	}
	revOnly := m.capsFrom(map[string]bool{"mot.spindle.0.reverse": true})
	if !revOnly.SpindleReverse || revOnly.SpindleForward {
		t.Errorf("reverse-only machine: %+v", revOnly)
	}
	// ...and neither direction pin may be inferred from a speed pin.
	speedOnly := m.capsFrom(map[string]bool{"mot.spindle.0.speed-out": true})
	if !speedOnly.SpindleSpeed || speedOnly.SpindleForward || speedOnly.SpindleReverse ||
		speedOnly.SpindleOn {
		t.Errorf("speed-only machine: %+v", speedOnly)
	}
	onOnly := m.capsFrom(map[string]bool{"mot.spindle.0.on": true})
	if !onOnly.SpindleOn || onOnly.SpindleForward || onOnly.SpindleSpeed {
		t.Errorf("on-only machine: %+v", onOnly)
	}
}

// Coolant lives on the io module, not motion, so it needs the OTHER instance
// name. Qualifying it with the motion prefix would hide the coolant buttons on
// exactly the configs this endpoint exists to fix.
func TestCapsFromQualifiesCoolantWithIoInstance(t *testing.T) {
	m := &milltaskModule{motInstance: "pnp.mot", ioInstance: "pnp.io"}
	m.task = &Task{numJoints: 3}

	caps := m.capsFrom(map[string]bool{
		"pnp.io.coolant-mist":  true,
		"pnp.io.coolant-flood": true,
	})
	if !caps.CoolantMist || !caps.CoolantFlood {
		t.Errorf("io-qualified coolant pins did not match: %+v", caps)
	}

	wrongPrefix := m.capsFrom(map[string]bool{
		"pnp.mot.coolant-mist":  true,
		"pnp.mot.coolant-flood": true,
	})
	if wrongPrefix.CoolantMist || wrongPrefix.CoolantFlood {
		t.Errorf("coolant matched under the motion prefix: %+v", wrongPrefix)
	}
}

func TestCapsFromDefaultsToIocontrol(t *testing.T) {
	m := &milltaskModule{} // no iocontrol_instance= given
	m.task = &Task{numJoints: 1}

	if caps := m.capsFrom(map[string]bool{"iocontrol.coolant-flood": true}); !caps.CoolantFlood {
		t.Error("iocontrol.coolant-flood did not enable flood coolant")
	}
}

// Single-instance configs carry no prefix on the motion module, and the default
// instance name is what makes their pins resolve.
func TestCapsFromDefaultsToMotmod(t *testing.T) {
	m := &milltaskModule{} // no motion_instance= given
	m.task = &Task{numJoints: 1}

	caps := m.capsFrom(map[string]bool{"motmod.spindle.0.speed-out": true})
	if !caps.SpindleSpeed {
		t.Error("motmod.spindle.0.speed-out did not register as a speed pin")
	}
}

// Any one of the four speed pins is enough — a config nets whichever scaling
// its VFD wants, and no client has ever distinguished them.
func TestCapsFromAcceptsAnySpeedPin(t *testing.T) {
	m := &milltaskModule{motInstance: "mot"}
	m.task = &Task{numJoints: 1}

	for _, pin := range spindleSpeedPins {
		caps := m.capsFrom(map[string]bool{"mot.spindle.0." + pin: true})
		if !caps.SpindleSpeed {
			t.Errorf("%q alone did not register as a speed pin", pin)
		}
		if caps.SpindleBrake {
			t.Errorf("%q wrongly reported a brake", pin)
		}
	}
}

// The joint scan is bounded by the configured joint count, so a limit switch on
// a joint this task does not have cannot enable the override button.
func TestCapsFromBoundsJointScan(t *testing.T) {
	m := &milltaskModule{motInstance: "mot"}
	m.task = &Task{numJoints: 3}

	beyond := map[string]bool{"mot.joint.5.neg-lim-sw-in": true}
	if caps := m.capsFrom(beyond); caps.LimitSwitchOverride {
		t.Error("limit switch on joint 5 of a 3-joint machine enabled the override")
	}

	within := map[string]bool{"mot.joint.2.neg-lim-sw-in": true}
	if caps := m.capsFrom(within); !caps.LimitSwitchOverride {
		t.Error("limit switch on joint 2 of a 3-joint machine did not enable the override")
	}
}

// A client reads /info once and caches it, so answering before Start with an
// empty-but-plausible struct would permanently disable features that exist.
func TestGetInfoRefusesBeforeStart(t *testing.T) {
	m := &milltaskModule{} // task == nil: Start has not run
	if _, err := m.GetInfo(); err == nil {
		t.Fatal("GetInfo answered before Start; a cached empty answer is worse than an error")
	}
}
