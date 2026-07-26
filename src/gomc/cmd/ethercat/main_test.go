// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import "testing"

// TestParseArgs covers the getopt forms the IgH tool accepts, so cmd/ethercat
// stays a faithful drop-in: separated / attached / long-with-equals values and
// clustered short options, plus command dispatch, "--", help, and errors.
func TestParseArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCmd  string
		wantArgs []string
		wantHelp bool
		wantErr  bool
		check    func(*GlobalOpts) bool
	}{
		{name: "separated value", args: []string{"-p", "0", "upload"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.Positions == "0" }},
		{name: "attached value", args: []string{"-p0", "upload"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.Positions == "0" }},
		{name: "long equals", args: []string{"--position=0", "upload"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.Positions == "0" }},
		{name: "long separated", args: []string{"--position", "3", "slaves"}, wantCmd: "slaves",
			check: func(o *GlobalOpts) bool { return o.Positions == "3" }},
		{name: "clustered booleans", args: []string{"-fq", "slaves"}, wantCmd: "slaves",
			check: func(o *GlobalOpts) bool { return o.Force && o.Verbosity == Quiet }},
		{name: "cluster bool then attached value", args: []string{"-fp0", "upload"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.Force && o.Positions == "0" }},
		{name: "cluster bool then separated value", args: []string{"-fp", "2", "upload"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.Force && o.Positions == "2" }},
		{name: "attached type", args: []string{"-tuint32", "upload"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.DataType == "uint32" }},
		{name: "verbose", args: []string{"-v", "slaves"}, wantCmd: "slaves",
			check: func(o *GlobalOpts) bool { return o.Verbosity == Verbose }},
		{name: "option after command", args: []string{"upload", "-p0"}, wantCmd: "upload",
			check: func(o *GlobalOpts) bool { return o.Positions == "0" }},
		{name: "command with args", args: []string{"upload", "0x2000", "0x00"}, wantCmd: "upload",
			wantArgs: []string{"0x2000", "0x00"}},
		{name: "double dash ends options", args: []string{"--", "-p0"}, wantCmd: "-p0"},
		{name: "help short", args: []string{"-h"}, wantHelp: true},
		{name: "help long", args: []string{"--help"}, wantHelp: true},
		{name: "unknown short", args: []string{"-z"}, wantErr: true},
		{name: "unknown short in cluster", args: []string{"-fz"}, wantErr: true},
		{name: "unknown long", args: []string{"--bogus"}, wantErr: true},
		{name: "missing short value", args: []string{"-p"}, wantErr: true},
		{name: "missing long value", args: []string{"--position"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, cmdName, cmdArgs, help, err := parseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if help != tt.wantHelp {
				t.Fatalf("help = %v, want %v", help, tt.wantHelp)
			}
			if tt.wantHelp {
				return
			}
			if cmdName != tt.wantCmd {
				t.Errorf("cmdName = %q, want %q", cmdName, tt.wantCmd)
			}
			if tt.wantArgs != nil {
				if len(cmdArgs) != len(tt.wantArgs) {
					t.Errorf("cmdArgs = %v, want %v", cmdArgs, tt.wantArgs)
				} else {
					for i := range tt.wantArgs {
						if cmdArgs[i] != tt.wantArgs[i] {
							t.Errorf("cmdArgs = %v, want %v", cmdArgs, tt.wantArgs)
							break
						}
					}
				}
			}
			if tt.check != nil && !tt.check(opts) {
				t.Errorf("option check failed: %+v", opts)
			}
		})
	}
}
