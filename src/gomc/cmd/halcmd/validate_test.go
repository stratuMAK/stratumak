// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"strings"
	"testing"
)

func TestCheckInputLineAcceptsOrdinaryCommands(t *testing.T) {
	for _, line := range []string{
		"newthread loop 1000000",
		"net x-pos-cmd\taxis.x.pos-cmd => joint.0.motor-pos-cmd",
		"setp foo.bar 1.5",
		"loadrt mux4 names=mü-x", // non-ASCII, but printable
		"",
	} {
		if err := checkInputLine(line); err != nil {
			t.Errorf("checkInputLine(%q) = %v; want nil", line, err)
		}
	}
}

// The shape of the bad input from issue #265: a cursor key recorded literally
// into the command line. The complaint must name the escape, not surface as a
// strconv message about a value nobody typed.
func TestCheckInputLineRejectsEscapeSequence(t *testing.T) {
	err := checkInputLine("newthread loop 1\x1b[A000000")
	if err == nil {
		t.Fatal("escape sequence accepted")
	}
	if !strings.Contains(err.Error(), "escape sequence") {
		t.Errorf("unhelpful message: %v", err)
	}
}

func TestCheckInputLineRejectsControlAndInvalidUTF8(t *testing.T) {
	if err := checkInputLine("setp foo\x07 1"); err == nil {
		t.Error("BEL accepted")
	}
	if err := checkInputLine("setp foo\xff 1"); err == nil {
		t.Error("invalid UTF-8 byte accepted")
	}
}

func TestCheckHALName(t *testing.T) {
	ok := []string{"loop", "servo-thread", "joint.0.motor-pos-cmd", "a_b:c"}
	for _, n := range ok {
		if err := checkHALName("thread", n); err != nil {
			t.Errorf("checkHALName(%q) = %v; want nil", n, err)
		}
	}
	bad := map[string]string{
		"":                       "empty",
		"loop\x1b[A":             "invalid character",
		strings.Repeat("x", 128): "too long",
		"has space":              "invalid character",
	}
	for n, want := range bad {
		err := checkHALName("thread", n)
		if err == nil {
			t.Errorf("checkHALName(%q) accepted", n)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("checkHALName(%q) = %v; want a message containing %q", n, err, want)
		}
	}
	// Exactly at the limit is still accepted (HAL_NAME_LEN).
	if err := checkHALName("thread", strings.Repeat("x", halNameMaxLen)); err != nil {
		t.Errorf("name of exactly %d characters rejected: %v", halNameMaxLen, err)
	}
}

func TestParseIntArg(t *testing.T) {
	v, err := parseIntArg("period", " 1000000 ", 64, "nanoseconds")
	if err != nil || v != 1000000 {
		t.Fatalf("parseIntArg = %d, %v", v, err)
	}

	_, err = parseIntArg("period", "1ß000000", 64, "nanoseconds")
	if err == nil {
		t.Fatal("garbage accepted")
	}
	if strings.Contains(err.Error(), "strconv") {
		t.Errorf("message leaks strconv internals: %v", err)
	}
	if !strings.Contains(err.Error(), "nanoseconds") {
		t.Errorf("message lacks the unit hint: %v", err)
	}

	if _, err := parseIntArg("cpu", "99999999999", 32, ""); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Errorf("range error = %v; want an out-of-range message", err)
	}
}
