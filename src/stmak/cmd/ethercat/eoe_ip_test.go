// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/ethercatclient"
)

// The EoE IP request is a partial update: `ethercat ip <slave> addr 1.2.3.4`
// must change the address and leave the slave's other network settings alone.
// The C handler implements that by asking whether each field was supplied —
// `if (req->hostname)`, `if (req->gateway)` — so "not supplied" has to travel
// the whole way as a null, and an omitted field must not appear in the JSON at
// all.
//
// It did not use to. `string?` was demoted to a plain Go `string` on the
// provider side, so the server unmarshalled an omitted `hostname` to "" and
// handed C a non-NULL pointer to an empty string. Five fields survived that by
// luck, because `inet_pton`/`sscanf` reject "" and leave `*_included` clear;
// `hostname` has no parse step, so it took the empty name and set
// `name_included = 1`. Setting only the IP address wiped the slave's hostname.
//
// This pins the client half of that contract — an unset field is absent from
// the wire, and a deliberately empty one is present. The server half (nil → a
// NULL C pointer) is pinned in internal/gmicompile/cgen's nullable-string
// tests.

func TestEoeIpRequestOmitsUnsetFields(t *testing.T) {
	addr := "192.168.1.5"
	req := ethercatclient.EoeIpRequest{IpAddress: &addr}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)

	if !strings.Contains(got, `"ip_address":"192.168.1.5"`) {
		t.Errorf("the supplied field is missing from %s", got)
	}
	for _, field := range []string{"hostname", "mac_address", "subnet_mask", "gateway", "dns"} {
		if strings.Contains(got, `"`+field+`"`) {
			t.Errorf("unset field %q appears in the request (%s); the C handler reads its "+
				"presence as an instruction to change it", field, got)
		}
	}
}

// TestEoeIpRequestKeepsExplicitEmpty is the other half: clearing a value is a
// real operation, and it must stay distinguishable from not touching it. If
// this ever starts omitting the field, `omitempty` has been applied to a plain
// string again and the two cases have re-merged.
func TestEoeIpRequestKeepsExplicitEmpty(t *testing.T) {
	empty := ""
	req := ethercatclient.EoeIpRequest{Hostname: &empty}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); !strings.Contains(got, `"hostname":""`) {
		t.Errorf("an explicitly empty hostname was dropped from %s — "+
			"clearing a value is indistinguishable from leaving it alone", got)
	}
}

// TestEoeIpRequestRoundTrip pins that a decoder sees exactly the same
// distinction the encoder wrote: absent stays nil, empty stays a non-nil "".
func TestEoeIpRequestRoundTrip(t *testing.T) {
	var got ethercatclient.EoeIpRequest
	if err := json.Unmarshal([]byte(`{"ip_address":"1.2.3.4","hostname":""}`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Hostname == nil {
		t.Error("an explicitly empty hostname decoded as absent")
	} else if *got.Hostname != "" {
		t.Errorf("hostname = %q, want \"\"", *got.Hostname)
	}
	if got.Gateway != nil {
		t.Errorf("an omitted gateway decoded as %q, want nil", *got.Gateway)
	}
	if got.IpAddress == nil || *got.IpAddress != "1.2.3.4" {
		t.Errorf("ip_address = %v, want \"1.2.3.4\"", got.IpAddress)
	}
}
