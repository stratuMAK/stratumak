// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"strings"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/emcstat"
	"github.com/sittner/linuxcnc/src/gomc/internal/apiserver"
	"github.com/sittner/linuxcnc/src/gomc/internal/halcmd"
)

// reportedPeer is a module milltask names for its clients but never calls: the
// api is the kind the name must resolve to, so that a name pointing at the
// wrong kind of module is rejected instead of silently accepted.
type reportedPeer struct {
	param    string // "load milltask" parameter that set it, for the error message
	api      string // GMI api the instance must provide
	instance string // configured name, "" = feature not configured
}

func (m *milltaskModule) reportedPeers() []reportedPeer {
	return []reportedPeer{
		{"preview_instance", "ngcpreview", m.previewInstance},
		{"mtc_instance", "manualtoolchange", m.mtcInstance},
		{"pyvcp_instance", "pyvcp", m.pyvcpInstance},
	}
}

// checkReportedPeers resolves every configured peer name at startup so a typo
// fails the config load instead of surfacing much later as a client-side 404.
//
// An unset name is not an error: it states that the feature is absent, which is
// exactly what GetInfo passes on so the client skips it entirely.
func (m *milltaskModule) checkReportedPeers(reg *apiserver.Registry) error {
	for _, p := range m.reportedPeers() {
		if p.instance == "" {
			continue
		}
		if reg.GetByAPI(p.api, p.instance) == nil {
			msg := fmt.Sprintf("milltask: %s=%s does not name a loaded %s module",
				p.param, p.instance, p.api)
			if loaded := reg.InstancesOfAPI(p.api); len(loaded) > 0 {
				msg += fmt.Sprintf(" (loaded %s instances: %s)",
					p.api, strings.Join(loaded, ", "))
			}
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}

// The four spindle 0 speed pins, reported as one flag because no client has
// ever distinguished them: a machine drives whichever scaling its VFD wants and
// the UI question is only "is a speed commanded at all". The direction pins are
// NOT collapsed with them — see TaskCaps in the IDL.
var spindleSpeedPins = []string{
	"speed-out", "speed-out-abs", "speed-out-rps", "speed-out-rps-abs",
}

// buildCaps reports what the machine has wired, by asking HAL rather than the
// INI: a config can declare SPINDLES = 1 and wire nothing, and 2.9 showed the
// spindle controls only when something actually drove those pins.
//
// Computed per call rather than cached at Start because net/setp over the
// halcmd REST API can rewire pins while the machine is up.
func (m *milltaskModule) buildCaps() (emcstat.TaskCaps, error) {
	driven, err := drivenPins()
	if err != nil {
		return emcstat.TaskCaps{}, err
	}
	return m.capsFrom(driven), nil
}

// drivenPins returns the set of HAL pins whose signal has a writer.
//
// One HAL walk, then exact string matching in capsFrom. Matching server-side
// with a glob would mean building fnmatch patterns out of an instance name, and
// any *, ? or [ in that name would read as a metacharacter.
func drivenPins() (map[string]bool, error) {
	res, err := halcmd.Show("pin")
	if err != nil {
		return nil, fmt.Errorf("milltask: HAL pin query: %w", err)
	}
	driven := make(map[string]bool, len(res.Pins))
	for _, p := range res.Pins {
		if p.HasWriter {
			driven[p.Name] = true
		}
	}
	return driven, nil
}

// capsFrom maps a driven-pin set to capability flags. Split from the HAL query
// so the part that can actually be wrong — qualifying pin names with the right
// peer instance, and bounding the joint scan — is testable directly.
//
// Two prefixes, because the pins live in two different modules: spindle and
// joint pins belong to motion, coolant pins to io. Defaults match the ones
// Start applies when the corresponding load parameter is absent.
func (m *milltaskModule) capsFrom(driven map[string]bool) emcstat.TaskCaps {
	mot := m.motInstance
	if mot == "" {
		mot = "motmod"
	}
	mot += "."

	io := m.ioInstance
	if io == "" {
		io = "iocontrol"
	}
	io += "."

	caps := emcstat.TaskCaps{
		SpindleForward: driven[mot+"spindle.0.forward"],
		SpindleReverse: driven[mot+"spindle.0.reverse"],
		SpindleOn:      driven[mot+"spindle.0.on"],
		SpindleBrake:   driven[mot+"spindle.0.brake"],
		CoolantMist:    driven[io+"coolant-mist"],
		CoolantFlood:   driven[io+"coolant-flood"],
	}
	for _, suffix := range spindleSpeedPins {
		if driven[mot+"spindle.0."+suffix] {
			caps.SpindleSpeed = true
			break
		}
	}
	// Bounded by the joint count this task actually configured, not MAX_JOINTS:
	// probing all 16 asks about joints that do not exist.
	for j := 0; j < int(m.task.numJoints); j++ {
		if driven[fmt.Sprintf("%sjoint.%d.neg-lim-sw-in", mot, j)] ||
			driven[fmt.Sprintf("%sjoint.%d.pos-lim-sw-in", mot, j)] {
			caps.LimitSwitchOverride = true
			break
		}
	}
	return caps
}
