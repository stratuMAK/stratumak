// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/halcmdclient"
)

// These tests exercise the halcmd CLI's line parsing, command dispatch and
// output formatting without a live stmakd: each command is driven against
// an httptest server returning canned JSON, so the real generated REST client
// and the real formatting/validation code run. stdout is captured for the
// happy-path assertions.

// route is one mocked REST response. status 0 means 200.
type route struct {
	status int
	body   interface{}
}

// serveRoutes starts an httptest server answering the given "METHOD /sub/path"
// keys (the path is what follows /api/v1/halcmd, query string excluded) and
// installs the package-level client against it. An unmatched request yields 404
// so a missed mock is a visible failure, not a silent zero value.
func serveRoutes(t *testing.T, routes map[string]route) {
	t.Helper()
	const prefix = "/api/v1/halcmd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		rt, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no mock route for " + key})
			return
		}
		if rt.status != 0 {
			w.WriteHeader(rt.status)
		}
		if rt.body != nil {
			_ = json.NewEncoder(w).Encode(rt.body)
		}
	}))
	t.Cleanup(srv.Close)

	saved := client
	client = halcmdclient.NewHalcmdClient(srv.URL)
	t.Cleanup(func() { client = saved })
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what was
// written. fn must not call t.Fatal — that would leave os.Stdout unrestored.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

// runCmd dispatches a command line through the same path the interactive shell
// and `-f` file mode use, returning stdout and the command's error.
func runCmd(t *testing.T, line string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = executeCommand(parseCommandLine(line))
	})
	return out, err
}

func okResult() halcmdclient.CmdResult { return halcmdclient.CmdResult{Success: true} }

// ===== line parsing =====

// TestParseCommandLine covers the tokenizer the interactive shell and `-f`
// mode both run every line through: quoting, comments and whitespace. A pin
// value containing a space (a string pin) only survives if quotes work.
func TestParseCommandLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"setp a.b 1", []string{"setp", "a.b", "1"}},
		{"setp\ta.b\t1", []string{"setp", "a.b", "1"}},
		{"  setp   a.b    1  ", []string{"setp", "a.b", "1"}},
		{`setp a.b "hello world"`, []string{"setp", "a.b", "hello world"}},
		{`setp a.b 'hello world'`, []string{"setp", "a.b", "hello world"}},
		// The other quote character inside a quoted run is literal.
		{`setp a.b "it's here"`, []string{"setp", "a.b", "it's here"}},
		// A comment ends the line, but the token before it is kept.
		{"setp a.b 1 # trailing comment", []string{"setp", "a.b", "1"}},
		{"# whole-line comment", nil},
		{"setp a.b 1# no space before comment", []string{"setp", "a.b", "1"}},
		// A '#' inside quotes is data, not a comment.
		{`setp a.b "x#y"`, []string{"setp", "a.b", "x#y"}},
	} {
		got := parseCommandLine(tc.line)
		if len(got) != len(tc.want) {
			t.Errorf("parseCommandLine(%q) = %q; want %q", tc.line, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("parseCommandLine(%q) = %q; want %q", tc.line, got, tc.want)
				break
			}
		}
	}
}

// TestStripArrows covers the arrow filter: `net sig a => b` and `linksp sig <= pin`
// are valid HAL-file spellings whose arrows carry no meaning.
func TestStripArrows(t *testing.T) {
	got := stripArrows([]string{"sig", "=>", "a.pin", "<=", "b.pin", "<=>", "c.pin"})
	want := []string{"sig", "a.pin", "b.pin", "c.pin"}
	if len(got) != len(want) {
		t.Fatalf("stripArrows = %q; want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stripArrows = %q; want %q", got, want)
		}
	}
	// A non-arrow token that merely contains an arrow character is kept.
	if got := stripArrows([]string{"a=>b"}); len(got) != 1 || got[0] != "a=>b" {
		t.Errorf("stripArrows(a=>b) = %q; want it kept", got)
	}
}

// ===== value commands =====

// TestCmdGetPPinAndParam covers getp's pin-then-param fallback and its output
// format, plus ptype.
func TestCmdGetPPinAndParam(t *testing.T) {
	serveRoutes(t, map[string]route{
		"GET /pin/comp.p":   {body: halcmdclient.PinInfo{Name: "comp.p", Type: "float", Dir: "IN", Value: "1.5", Owner: "comp"}},
		"GET /pin/comp.q":   {status: 404, body: map[string]string{"error": "not found"}},
		"GET /param/comp.q": {body: halcmdclient.ParamInfo{Name: "comp.q", Type: "s32", Dir: "RW", Value: "7", Owner: "comp"}},
		"GET /pin/nosuch":   {status: 404, body: map[string]string{"error": "not found"}},
		"GET /param/nosuch": {status: 404, body: map[string]string{"error": "not found"}},
	})

	out, err := runCmd(t, "getp comp.p")
	if err != nil {
		t.Fatalf("getp pin: %v", err)
	}
	if out != "float IN comp.p = 1.5\n" {
		t.Errorf("getp pin output = %q", out)
	}

	// Not a pin → the param lookup is tried before giving up.
	out, err = runCmd(t, "getp comp.q")
	if err != nil {
		t.Fatalf("getp param: %v", err)
	}
	if out != "s32 RW comp.q = 7\n" {
		t.Errorf("getp param output = %q", out)
	}

	if _, err := runCmd(t, "getp nosuch"); err == nil {
		t.Error("getp on an unknown name must fail")
	}

	out, err = runCmd(t, "ptype comp.p")
	if err != nil {
		t.Fatalf("ptype: %v", err)
	}
	if out != "float\n" {
		t.Errorf("ptype output = %q; want %q", out, "float\n")
	}
}

// TestCmdSignalCommands covers gets/sets/stype/newsig/delsig against the REST
// surface, including a server-side in-band failure.
func TestCmdSignalCommands(t *testing.T) {
	failMsg := "signal has writers"
	serveRoutes(t, map[string]route{
		"GET /signal/mysig":    {body: halcmdclient.SignalInfo{Name: "mysig", Type: "s32", Value: "42"}},
		"PUT /signal/mysig":    {body: okResult()},
		"POST /signal":         {body: okResult()},
		"DELETE /signal/mysig": {body: okResult()},
		"GET /signal/driven":   {body: halcmdclient.SignalInfo{Name: "driven", Type: "bit", Value: "TRUE"}},
		"PUT /signal/driven":   {body: halcmdclient.CmdResult{Success: false, Error: &failMsg}},
	})

	out, err := runCmd(t, "gets mysig")
	if err != nil {
		t.Fatalf("gets: %v", err)
	}
	if out != "s32 mysig = 42\n" {
		t.Errorf("gets output = %q", out)
	}

	out, err = runCmd(t, "stype mysig")
	if err != nil {
		t.Fatalf("stype: %v", err)
	}
	if out != "s32\n" {
		t.Errorf("stype output = %q", out)
	}

	if _, err := runCmd(t, "sets mysig 9"); err != nil {
		t.Errorf("sets: %v", err)
	}
	if _, err := runCmd(t, "newsig mysig s32"); err != nil {
		t.Errorf("newsig: %v", err)
	}
	if _, err := runCmd(t, "delsig mysig"); err != nil {
		t.Errorf("delsig: %v", err)
	}

	// An in-band {"success":false,"error":...} must surface as a CLI error, not
	// be silently treated as success.
	_, err = runCmd(t, "sets driven 1")
	if err == nil {
		t.Fatal("sets with an in-band failure returned nil")
	}
	if !strings.Contains(err.Error(), failMsg) {
		t.Errorf("error = %q; want it to carry %q", err.Error(), failMsg)
	}
}

// TestCmdArgumentValidation covers the arity checks every command performs
// before touching the network — a missing argument must be a clear local error,
// not a malformed request.
func TestCmdArgumentValidation(t *testing.T) {
	// No routes: any command that reaches the network 404s, so a nil error here
	// would also be a failure.
	serveRoutes(t, map[string]route{})

	for _, line := range []string{
		"getp",
		"setp",
		"setp onlyname",
		"gets",
		"sets",
		"sets onlyname",
		"ptype",
		"stype",
		"newsig",
		"newsig onlyname",
		"delsig",
		"net",
		"net onlysignal",
		"linksp",
		"linksp onlyone",
		"linkps",
		"linkps onlyone",
		"unlinkp",
		"delthread",
		"addf",
		"delf",
	} {
		if _, err := runCmd(t, line); err == nil {
			t.Errorf("%q = nil error; want an argument-validation rejection", line)
		}
	}
}

// TestCmdUnknownCommand: a typo must be reported, not silently ignored — in
// `-f` script mode a swallowed line would leave the machine half-configured.
func TestCmdUnknownCommand(t *testing.T) {
	serveRoutes(t, map[string]route{})
	if _, err := runCmd(t, "notacommand foo"); err == nil {
		t.Error("an unknown command returned nil")
	}
	// An empty line is a no-op, not an error.
	if _, err := runCmd(t, ""); err != nil {
		t.Errorf("empty line = %v; want nil", err)
	}
	if _, err := runCmd(t, "# just a comment"); err != nil {
		t.Errorf("comment-only line = %v; want nil", err)
	}
}

// TestCmdLinkAndNet covers the link forms and net, including arrow stripping on
// the way to the REST call.
func TestCmdLinkAndNet(t *testing.T) {
	var netBody struct {
		Signal string   `json:"signal"`
		Pins   []string `json:"pins"`
	}
	const prefix = "/api/v1/halcmd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")
		if p == "/net" {
			_ = json.NewDecoder(r.Body).Decode(&netBody)
		}
		_ = json.NewEncoder(w).Encode(okResult())
	}))
	t.Cleanup(srv.Close)
	saved := client
	client = halcmdclient.NewHalcmdClient(srv.URL)
	t.Cleanup(func() { client = saved })

	if _, err := runCmd(t, "net mysig a.pin => b.pin <= c.pin"); err != nil {
		t.Fatalf("net: %v", err)
	}
	if netBody.Signal != "mysig" {
		t.Errorf("net signal = %q; want mysig", netBody.Signal)
	}
	want := []string{"a.pin", "b.pin", "c.pin"}
	if len(netBody.Pins) != len(want) {
		t.Fatalf("net pins = %q; want %q (arrows stripped)", netBody.Pins, want)
	}
	for i := range want {
		if netBody.Pins[i] != want[i] {
			t.Errorf("net pins = %q; want %q", netBody.Pins, want)
			break
		}
	}

	for _, line := range []string{
		"linkps a.pin mysig",
		"linksp mysig a.pin",
		"linkpp a.pin b.pin",
		"unlinkp a.pin",
	} {
		if _, err := runCmd(t, line); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
}

// ===== listing / show =====

// TestCmdShowAndList covers the tabular `show` renderers and the bare-name
// `list` output, which scripts parse.
func TestCmdShowAndList(t *testing.T) {
	sig := "mysig"
	serveRoutes(t, map[string]route{
		"GET /pins": {body: []halcmdclient.PinInfo{
			{Name: "comp.a", Type: "bit", Dir: "IN", Value: "FALSE", Owner: "comp"},
			{Name: "comp.b", Type: "float", Dir: "OUT", Value: "2.5", Owner: "comp", Linked: true, Signal: &sig},
		}},
		"GET /params": {body: []halcmdclient.ParamInfo{
			{Name: "comp.p", Type: "s32", Dir: "RW", Value: "3", Owner: "comp"},
		}},
		"GET /signals": {body: []halcmdclient.SignalInfo{
			{Name: "mysig", Type: "float", Value: "2.5", Writers: []string{"comp.b"}},
		}},
		"GET /components": {body: []halcmdclient.ComponentInfo{
			{Name: "comp", Id: 3, Type: "rt", State: "ready"},
		}},
		"GET /functions": {body: []halcmdclient.FunctionInfo{
			{Name: "comp.update", Owner: "comp", Users: 1},
		}},
		"GET /threads": {body: []halcmdclient.ThreadInfo{
			{Name: "servo-thread", Period: 1000000},
		}},
	})

	for _, tc := range []struct {
		line string
		want []string
	}{
		{"show pin", []string{"comp.a", "comp.b", "mysig"}},
		{"show param", []string{"comp.p"}},
		{"show sig", []string{"mysig", "comp.b"}},
		{"show comp", []string{"comp"}},
		{"show funct", []string{"comp.update"}},
		{"show thread", []string{"servo-thread"}},
		{"list pin", []string{"comp.a", "comp.b"}},
		{"list sig", []string{"mysig"}},
		{"list comp", []string{"comp"}},
		{"list funct", []string{"comp.update"}},
		{"list thread", []string{"servo-thread"}},
		{"list param", []string{"comp.p"}},
	} {
		out, err := runCmd(t, tc.line)
		if err != nil {
			t.Errorf("%q: %v", tc.line, err)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%q output is missing %q:\n%s", tc.line, want, out)
			}
		}
	}

	if _, err := runCmd(t, "list notatype"); err == nil {
		t.Error("list with an unknown type must fail")
	}
	if _, err := runCmd(t, "show notatype"); err == nil {
		t.Error("show with an unknown type must fail")
	}
}

// TestCmdStatus covers the status summary rendering.
func TestCmdStatus(t *testing.T) {
	serveRoutes(t, map[string]route{
		"GET /status": {body: halcmdclient.HalStatus{
			ThreadsRunning: true, Components: 4, Pins: 20, Signals: 5,
			Params: 2, Threads: 1, Functions: 3,
		}},
	})
	out, err := runCmd(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"4", "20", "5"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// TestTransportErrorPropagates: when the server is unreachable the CLI must
// report it, not print an empty table and exit 0.
func TestTransportErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	saved := client
	client = halcmdclient.NewHalcmdClient(url)
	t.Cleanup(func() { client = saved })

	for _, line := range []string{"list pin", "show pin", "status", "gets mysig"} {
		if _, err := runCmd(t, line); err == nil {
			t.Errorf("%q against an unreachable server returned nil", line)
		}
	}
}

// TestServerErrorPropagates: an HTTP 5xx must surface with the server's message
// rather than being reported as success.
func TestServerErrorPropagates(t *testing.T) {
	serveRoutes(t, map[string]route{
		"GET /pins": {status: 500, body: map[string]string{"error": "hal shared memory unavailable"}},
	})
	_, err := runCmd(t, "list pin")
	if err == nil {
		t.Fatal("a 500 response returned nil")
	}
	if !strings.Contains(err.Error(), "hal shared memory unavailable") {
		t.Errorf("error = %q; want the server message", err.Error())
	}
}

// TestRunStream drives the `-f` file path: commands are read line by line, and
// without -k the first failure stops the stream.
func TestRunStream(t *testing.T) {
	serveRoutes(t, map[string]route{
		"GET /signal/mysig": {body: halcmdclient.SignalInfo{Name: "mysig", Type: "s32", Value: "1"}},
	})

	savedKeep, savedQuiet := keepGoing, quietMode
	t.Cleanup(func() { keepGoing, quietMode = savedKeep, savedQuiet })
	keepGoing, quietMode = false, true

	script := "# comment\n\ngets mysig\ngets mysig\n"
	out := captureStdout(t, func() {
		if err := runStream(strings.NewReader(script), "test"); err != nil {
			t.Errorf("runStream: %v", err)
		}
	})
	if n := strings.Count(out, "s32 mysig = 1"); n != 2 {
		t.Errorf("runStream ran %d gets; want 2:\n%s", n, out)
	}

	// A failing line stops the stream.
	var err error
	_ = captureStdout(t, func() {
		err = runStream(strings.NewReader("gets nosuchsig\ngets mysig\n"), "test")
	})
	if err == nil {
		t.Error("runStream did not report the failing line")
	}

	// With -k it keeps going and still reports a failure at the end.
	keepGoing = true
	out = captureStdout(t, func() {
		err = runStream(strings.NewReader("gets nosuchsig\ngets mysig\n"), "test")
	})
	if !strings.Contains(out, "s32 mysig = 1") {
		t.Errorf("-k did not continue past the failing line:\n%s", out)
	}
}

// ===== remaining commands =====

// TestCmdRemaining covers the commands not exercised above — the module,
// thread, function, alias, lock and save surfaces — against a server that
// accepts everything, so the assertion is that each dispatches without a local
// error and reaches the expected endpoint.
func TestCmdRemaining(t *testing.T) {
	var seen []string
	const prefix = "/api/v1/halcmd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+strings.TrimPrefix(r.URL.Path, prefix))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResult())
	}))
	t.Cleanup(srv.Close)
	saved := client
	client = halcmdclient.NewHalcmdClient(srv.URL)
	t.Cleanup(func() { client = saved })

	for _, tc := range []struct {
		line string
		want string
	}{
		{"setp comp.p 1", "PUT /pin/comp.p"},
		{"load mymod cfg=1", "POST /load"},
		{"unload mymod", "DELETE /module/mymod"},
		{"newthread servo-thread 1000000", "POST /thread"},
		{"newthread base-thread 50000 nofp", "POST /thread"},
		{"delthread servo-thread", "DELETE /thread/servo-thread"},
		{"addf comp.update servo-thread", "POST /thread/servo-thread/function"},
		{"delf comp.update servo-thread", "DELETE /thread/servo-thread/function/comp.update"},
		{"start", "POST /start"},
		{"stop", "POST /stop"},
		{"alias pin comp.p myalias", "POST /pin/comp.p/alias"},
		{"alias param comp.q myalias", "POST /param/comp.q/alias"},
		{"unalias pin myalias", "DELETE /pin/myalias/alias"},
		{"unalias param myalias", "DELETE /param/myalias/alias"},
		{"lock all", "POST /lock"},
		{"unlock all", "POST /unlock"},
		{"debug 2", "PUT /debug"},
		{"save sig", "GET /save"},
		{"retain mysig", "POST /signal/mysig/retain"},
		{"unretain mysig", "DELETE /signal/mysig/retain"},
	} {
		seen = nil
		if _, err := runCmd(t, tc.line); err != nil {
			t.Errorf("%q: %v", tc.line, err)
			continue
		}
		if len(seen) == 0 {
			t.Errorf("%q made no REST call; want %q", tc.line, tc.want)
			continue
		}
		if seen[len(seen)-1] != tc.want {
			t.Errorf("%q hit %q; want %q", tc.line, seen[len(seen)-1], tc.want)
		}
	}
}

// TestCmdNewThreadValidation covers the newthread argument grammar the CLI
// validates locally: the period must parse, and the fp/nofp/cpu= options are
// recognised. A bad period must be rejected before it reaches the server.
func TestCmdNewThreadValidation(t *testing.T) {
	serveRoutes(t, map[string]route{"POST /thread": {body: okResult()}})

	for _, line := range []string{
		"newthread t 1000000",
		"newthread t 1000000 fp",
		"newthread t 1000000 nofp",
	} {
		if _, err := runCmd(t, line); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
	for _, line := range []string{
		"newthread",
		"newthread onlyname",
		"newthread t notanumber",
	} {
		if _, err := runCmd(t, line); err == nil {
			t.Errorf("%q = nil error; want a rejection", line)
		}
	}
}

// TestCmdHelp covers the built-in help text: bare `help` lists the commands and
// `help <cmd>` describes one. It must never fail — it is what a confused
// operator types first.
func TestCmdHelp(t *testing.T) {
	serveRoutes(t, map[string]route{})

	out, err := runCmd(t, "help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, want := range []string{"setp", "net", "show"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output is missing %q:\n%s", want, out)
		}
	}

	out, err = runCmd(t, "help setp")
	if err != nil {
		t.Fatalf("help setp: %v", err)
	}
	if !strings.Contains(out, "setp") {
		t.Errorf("help setp output does not mention setp:\n%s", out)
	}

	// Help for an unknown command is reported, not silently empty.
	if _, err := runCmd(t, "help notacommand"); err == nil {
		t.Error("help for an unknown command returned nil")
	}

	// printUsage writes the CLI synopsis.
	out = captureStdout(t, printUsage)
	if !strings.Contains(out, "halcmd") {
		t.Errorf("printUsage output does not mention halcmd:\n%s", out)
	}
}

// ===== shell completion =====

// TestSkipOptions covers the option-stripping the completion protocol runs
// before deciding what word is being completed: -U and -f take an argument, the
// flag-only options do not.
func TestSkipOptions(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"show pin", "show pin"},
		{"-k show pin", "show pin"},
		{"-k -q show ", "show "},
		{"-U http://host:1/ show ", "show "},
		{"-f file.hal show ", "show "},
		// A dangling option with no argument yet completes nothing.
		{"-U", ""},
		{"-U http://host:1/", ""},
		{"-k", ""},
	} {
		if got := skipOptions(tc.in); got != tc.want {
			t.Errorf("skipOptions(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestSplitWords covers the completion tokenizer, which must keep a quoted
// value as one word so the fragment being completed is identified correctly.
func TestSplitWords(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"show pin", []string{"show", "pin"}},
		{"  show   pin  ", []string{"show", "pin"}},
		{`setp a.b "two words"`, []string{"setp", "a.b", "two words"}},
		{`setp a.b 'two words'`, []string{"setp", "a.b", "two words"}},
	} {
		got := splitWords(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitWords(%q) = %q; want %q", tc.in, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("splitWords(%q) = %q; want %q", tc.in, got, tc.want)
				break
			}
		}
	}
}

// TestCompleteCommand covers subcommand completion and its prefix filter.
func TestCompleteCommand(t *testing.T) {
	all := completeCommand("")
	if len(all) < 20 {
		t.Fatalf("completeCommand(\"\") returned %d entries; want the full subcommand list", len(all))
	}
	got := completeCommand("link")
	want := map[string]bool{"linksp": true, "linkps": true, "linkpp": true}
	if len(got) != len(want) {
		t.Fatalf("completeCommand(\"link\") = %q; want the three link commands", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("completeCommand(\"link\") returned unexpected %q", g)
		}
	}
	if got := completeCommand("zzz"); len(got) != 0 {
		t.Errorf("completeCommand(\"zzz\") = %q; want none", got)
	}
}

// TestCompleteArg covers argument completion against a mocked server: the
// keyword arguments (show/list types, alias kinds, lock levels) come from a
// static table, the object names come over REST.
func TestCompleteArg(t *testing.T) {
	// Prefix filtering is delegated to the server: the CLI sends
	// ?pattern=<fragment>* and returns whatever comes back. The mock therefore
	// honours the pattern, which also asserts that the CLI sends it at all.
	type obj struct {
		name   string
		typ    string
		dir    string
		linked bool
		users  int32
	}
	objects := map[string][]obj{
		"/pins": {
			{name: "comp.a", typ: "bit", dir: "IN"},
			{name: "comp.b", typ: "float", dir: "IN"},
			{name: "comp.linked", typ: "float", dir: "OUT", linked: true},
			{name: "other.c", typ: "bit", dir: "IO"},
		},
		"/params":     {{name: "comp.p", typ: "s32", dir: "RW"}},
		"/signals":    {{name: "mysig", typ: "float"}, {name: "othersig", typ: "bit"}},
		"/components": {{name: "comp"}},
		"/functions":  {{name: "comp.update"}, {name: "comp.inuse", users: 1}},
		"/threads":    {{name: "servo-thread"}},
	}

	var patterns []string
	const prefix = "/api/v1/halcmd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		w.Header().Set("Content-Type", "application/json")

		// Single-object lookups: the type-matched completers read the already
		// named object to learn its type.
		if name, ok := strings.CutPrefix(p, "/pin/"); ok {
			for _, o := range objects["/pins"] {
				if o.name == name {
					_ = json.NewEncoder(w).Encode(halcmdclient.PinInfo{Name: o.name, Type: o.typ, Dir: o.dir, Linked: o.linked, Owner: "comp"})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such pin"})
			return
		}
		if name, ok := strings.CutPrefix(p, "/signal/"); ok {
			for _, o := range objects["/signals"] {
				if o.name == name {
					_ = json.NewEncoder(w).Encode(halcmdclient.SignalInfo{Name: o.name, Type: o.typ})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such signal"})
			return
		}

		pat := r.URL.Query().Get("pattern")
		patterns = append(patterns, pat)
		want := strings.TrimSuffix(pat, "*")

		var keep []obj
		for _, o := range objects[p] {
			if strings.HasPrefix(o.name, want) {
				keep = append(keep, o)
			}
		}
		switch p {
		case "/pins":
			out := make([]halcmdclient.PinInfo, 0, len(keep))
			for _, o := range keep {
				out = append(out, halcmdclient.PinInfo{Name: o.name, Type: o.typ, Dir: o.dir, Linked: o.linked, Owner: "comp"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/params":
			out := make([]halcmdclient.ParamInfo, 0, len(keep))
			for _, o := range keep {
				out = append(out, halcmdclient.ParamInfo{Name: o.name, Type: o.typ, Dir: o.dir, Owner: "comp"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/signals":
			out := make([]halcmdclient.SignalInfo, 0, len(keep))
			for _, o := range keep {
				out = append(out, halcmdclient.SignalInfo{Name: o.name, Type: o.typ})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/components":
			out := make([]halcmdclient.ComponentInfo, 0, len(keep))
			for _, o := range keep {
				out = append(out, halcmdclient.ComponentInfo{Name: o.name, Id: 1, Type: "rt", State: "ready"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/functions":
			out := make([]halcmdclient.FunctionInfo, 0, len(keep))
			for _, o := range keep {
				out = append(out, halcmdclient.FunctionInfo{Name: o.name, Owner: "comp", Users: o.users})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/threads":
			out := make([]halcmdclient.ThreadInfo, 0, len(keep))
			for _, o := range keep {
				out = append(out, halcmdclient.ThreadInfo{Name: o.name, Period: 1000000})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no mock route for " + p})
		}
	}))
	t.Cleanup(srv.Close)
	saved := client
	client = halcmdclient.NewHalcmdClient(srv.URL)
	t.Cleanup(func() { client = saved })

	has := func(got []string, want string) bool {
		for _, g := range got {
			if g == want {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		name    string
		cmd     string
		argPos  int
		prefix  string
		prev    []string
		want    string
		notWant string
	}{
		{"show type", "show", 1, "", nil, "pin", ""},
		{"list type", "list", 1, "", nil, "sig", ""},
		{"getp pin prefix", "getp", 1, "comp.", nil, "comp.a", "other.c"},
		{"getp includes params", "getp", 1, "comp.p", nil, "comp.p", "comp.a"},
		{"gets signal", "gets", 1, "", nil, "mysig", ""},
		{"delsig signal prefix", "delsig", 1, "other", nil, "othersig", "mysig"},
		{"unload component", "unload", 1, "", nil, "comp", ""},
		{"delthread thread", "delthread", 1, "", nil, "servo-thread", ""},
		{"addf funct", "addf", 1, "", nil, "comp.update", ""},
		{"addf thread", "addf", 2, "", []string{"comp.update"}, "servo-thread", ""},
		{"alias kind", "alias", 1, "", nil, "pin", ""},
		{"lock level", "lock", 1, "", nil, "all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := completeArg(tc.cmd, tc.argPos, tc.prefix, tc.prev)
			if tc.want != "" && !has(got, tc.want) {
				t.Errorf("completeArg(%s, %d, %q) = %q; want it to contain %q", tc.cmd, tc.argPos, tc.prefix, got, tc.want)
			}
			if tc.notWant != "" && has(got, tc.notWant) {
				t.Errorf("completeArg(%s, %d, %q) = %q; must not contain %q", tc.cmd, tc.argPos, tc.prefix, got, tc.notWant)
			}
		})
	}

	// Type-matched completion: the second argument of a link/net command is
	// filtered by the type of the object already named, so a tab-complete
	// cannot offer a pin that HAL would refuse to link.
	for _, tc := range []struct {
		name    string
		cmd     string
		argPos  int
		prev    []string
		want    string
		notWant string
	}{
		// mysig is float → only the float pin may be offered.
		{"net typed pins", "net", 2, []string{"mysig"}, "comp.b", "comp.a"},
		{"linksp typed pins", "linksp", 2, []string{"mysig"}, "comp.b", "comp.a"},
		// comp.a is bit → only a bit signal may be offered.
		{"linkps typed signals", "linkps", 2, []string{"comp.a"}, "othersig", "mysig"},
		// comp.a is bit → only other bit pins.
		{"linkpp typed pins", "linkpp", 2, []string{"comp.a"}, "other.c", "comp.b"},
		// unlinkp only offers pins that are actually linked.
		{"unlinkp linked pins", "unlinkp", 1, nil, "comp.linked", "comp.a"},
		// delf only offers functions already added to a thread.
		{"delf used functions", "delf", 1, nil, "comp.inuse", "comp.update"},
		// addf only offers functions not yet added.
		{"addf unused functions", "addf", 1, nil, "comp.update", "comp.inuse"},
		// show/list second argument completes objects of the named type.
		{"show pin names", "show", 2, []string{"pin"}, "comp.a", "mysig"},
		{"list sig names", "list", 2, []string{"sig"}, "mysig", "comp.a"},
		{"show funct names", "show", 2, []string{"funct"}, "comp.update", "mysig"},
		{"show thread names", "show", 2, []string{"thread"}, "servo-thread", "mysig"},
		{"show comp names", "show", 2, []string{"comp"}, "comp", "mysig"},
		{"show param names", "show", 2, []string{"param"}, "comp.p", "mysig"},
		// alias/unalias arg2 follows the kind keyword.
		{"alias param names", "alias", 2, []string{"param"}, "comp.p", "comp.a"},
		{"alias pin names", "alias", 2, []string{"pin"}, "comp.a", "comp.p"},
		{"unalias param names", "unalias", 2, []string{"param"}, "comp.p", "comp.a"},
		// keyword argument lists
		{"newsig type", "newsig", 2, []string{"mysig"}, "float", ""},
		{"save type", "save", 1, nil, "all", ""},
		{"status keyword", "status", 1, nil, "lock", ""},
		{"unlock level", "unlock", 1, nil, "tune", ""},
		{"help subcommand", "help", 1, nil, "setp", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := completeArg(tc.cmd, tc.argPos, "", tc.prev)
			if tc.want != "" && !has(got, tc.want) {
				t.Errorf("completeArg(%s, %d, prev=%q) = %q; want it to contain %q", tc.cmd, tc.argPos, tc.prev, got, tc.want)
			}
			if tc.notWant != "" && has(got, tc.notWant) {
				t.Errorf("completeArg(%s, %d, prev=%q) = %q; must not contain %q", tc.cmd, tc.argPos, tc.prev, got, tc.notWant)
			}
		})
	}

	// The typed fragment must have reached the server as a glob — that is where
	// the filtering happens, so a dropped pattern would silently list every
	// object on the machine.
	if !has(patterns, "comp.*") {
		t.Errorf("the typed fragment was never sent as a pattern; saw %q", patterns)
	}

	// An unknown command completes nothing rather than erroring.
	if got := completeArg("notacommand", 1, "", nil); len(got) != 0 {
		t.Errorf("completeArg on an unknown command = %q; want none", got)
	}
}

// TestFilterPrefix covers the static-candidate filter used for keyword
// arguments (show/list types, lock levels), which are not server-side.
func TestFilterPrefix(t *testing.T) {
	items := []string{"pin", "param", "sig", "thread"}
	if got := filterPrefix(items, ""); len(got) != len(items) {
		t.Errorf("filterPrefix with an empty prefix = %q; want all", got)
	}
	got := filterPrefix(items, "p")
	if len(got) != 2 || got[0] != "pin" || got[1] != "param" {
		t.Errorf("filterPrefix(p) = %q; want [pin param]", got)
	}
	if got := filterPrefix(items, "zzz"); len(got) != 0 {
		t.Errorf("filterPrefix(zzz) = %q; want none", got)
	}
}

// TestRunCompletionProtocol drives the bash `complete -C` entry point through
// its environment-variable contract, including the no-environment case that
// must produce nothing at all.
func TestRunCompletionProtocol(t *testing.T) {
	serveRoutes(t, map[string]route{
		"GET /signals": {body: []halcmdclient.SignalInfo{{Name: "mysig", Type: "float", Value: "0"}}},
	})

	t.Setenv("COMP_LINE", "halcmd link")
	t.Setenv("COMP_POINT", "12")
	out := captureStdout(t, runCompletion)
	for _, want := range []string{"linksp", "linkps", "linkpp"} {
		if !strings.Contains(out, want) {
			t.Errorf("completion output is missing %q:\n%s", want, out)
		}
	}

	// Completing an argument reaches the REST API.
	t.Setenv("COMP_LINE", "halcmd gets ")
	t.Setenv("COMP_POINT", "13")
	out = captureStdout(t, runCompletion)
	if !strings.Contains(out, "mysig") {
		t.Errorf("argument completion output is missing mysig:\n%s", out)
	}

	// Without the bash environment there is nothing to complete.
	t.Setenv("COMP_LINE", "")
	t.Setenv("COMP_POINT", "")
	if out := captureStdout(t, runCompletion); out != "" {
		t.Errorf("runCompletion without COMP_LINE printed %q; want nothing", out)
	}
}

// TestShowAndSourceFile covers the remaining two file/output paths: the alias
// listing (which only prints a section when something is aliased) and `source`,
// which runs a HAL file through the same stream reader as -f.
func TestShowAndSourceFile(t *testing.T) {
	pinAlias, paramAlias := "myalias", "myparamalias"
	serveRoutes(t, map[string]route{
		"GET /pins": {body: []halcmdclient.PinInfo{
			{Name: "comp.a", Type: "bit", Dir: "IN", Value: "FALSE", Owner: "comp", Alias: &pinAlias},
			{Name: "comp.b", Type: "float", Dir: "OUT", Value: "0", Owner: "comp"},
		}},
		"GET /params": {body: []halcmdclient.ParamInfo{
			{Name: "comp.p", Type: "s32", Dir: "RW", Value: "0", Owner: "comp", Alias: &paramAlias},
		}},
		"GET /signal/mysig": {body: halcmdclient.SignalInfo{Name: "mysig", Type: "s32", Value: "1"}},
	})

	out, err := runCmd(t, "show alias")
	if err != nil {
		t.Fatalf("show alias: %v", err)
	}
	for _, want := range []string{"Pin Aliases", pinAlias, "comp.a", "Parameter Aliases", paramAlias, "comp.p"} {
		if !strings.Contains(out, want) {
			t.Errorf("show alias output is missing %q:\n%s", want, out)
		}
	}

	// `source` reads a real file through runFile → runStream.
	savedQuiet := quietMode
	t.Cleanup(func() { quietMode = savedQuiet })
	quietMode = true

	dir := t.TempDir()
	path := dir + "/script.hal"
	if err := os.WriteFile(path, []byte("# a comment\ngets mysig\n"), 0o644); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	out, err = runCmd(t, "source "+path)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if !strings.Contains(out, "s32 mysig = 1") {
		t.Errorf("source did not run the script:\n%s", out)
	}

	if _, err := runCmd(t, "source "+dir+"/nosuchfile.hal"); err == nil {
		t.Error("source of a missing file returned nil")
	}
	if _, err := runCmd(t, "source"); err == nil {
		t.Error("source without a filename returned nil")
	}
}

// TestCompleteWritablePinsAndParams covers setp's completer: it must offer only
// what setp can actually write — a read-only param or an output/linked pin is
// driven by HAL, so offering it would produce a command the server rejects.
func TestCompleteWritablePinsAndParams(t *testing.T) {
	sig := "mysig"
	serveRoutes(t, map[string]route{
		"GET /pins": {body: []halcmdclient.PinInfo{
			{Name: "comp.in", Type: "bit", Dir: "IN", Owner: "comp"},
			{Name: "comp.out", Type: "bit", Dir: "OUT", Owner: "comp"},
			{Name: "comp.linked", Type: "bit", Dir: "IN", Owner: "comp", Linked: true, Signal: &sig},
		}},
		"GET /params": {body: []halcmdclient.ParamInfo{
			{Name: "comp.rw", Type: "s32", Dir: "RW", Owner: "comp"},
			{Name: "comp.ro", Type: "s32", Dir: "RO", Owner: "comp"},
		}},
	})

	got := completeArg("setp", 1, "", nil)
	has := func(want string) bool {
		for _, g := range got {
			if g == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"comp.in", "comp.rw"} {
		if !has(want) {
			t.Errorf("setp completion = %q; want it to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"comp.out", "comp.linked", "comp.ro"} {
		if has(notWant) {
			t.Errorf("setp completion = %q; must not offer %q", got, notWant)
		}
	}
}
