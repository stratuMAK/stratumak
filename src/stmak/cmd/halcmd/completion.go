// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/halcmdclient"
)

// completionTimeout bounds every REST query issued from completion. TAB
// completion runs inside the raw-mode line editor, where ISIG is off: a query
// against a hung server (accepted socket, no reply) would otherwise block the
// prompt with Ctrl-C reduced to an unread byte — unkillable from the keyboard.
// Commands keep the untimed shared client; only completion must never wedge.
const completionTimeout = 2 * time.Second

var compClient *halcmdclient.HalcmdClient

// installClients builds both clients for url: the untimed shared command
// client and the short-timeout completion client. main() and -U go through
// here; tests that stub `client` directly leave compClient nil and completion
// falls through to the stub.
func installClients(url string) {
	client = halcmdclient.NewHalcmdClient(url)
	compClient = halcmdclient.NewHalcmdClient(url).
		WithHTTPClient(&http.Client{Timeout: completionTimeout})
}

// completionClient returns the client completion queries go through.
func completionClient() *halcmdclient.HalcmdClient {
	if compClient != nil {
		return compClient
	}
	return client
}

// parseCompPoint parses the bash COMP_POINT cursor offset. It mirrors C atoi:
// consume leading digits and stop at the first non-digit, so a stray character
// yields the digits parsed so far rather than a garbage/negative offset.
func parseCompPoint(compPoint string) int {
	point := 0
	for _, ch := range compPoint {
		if ch < '0' || ch > '9' {
			break
		}
		point = point*10 + int(ch-'0')
	}
	return point
}

// relevantCompLine returns the portion of the command line between the end of
// the command name (argv[0] + following whitespace) and the cursor. The cursor
// offset is clamped on BOTH sides: bash reports it as an absolute offset, so a
// cursor inside the command name (point < i) — e.g. a mid-line TAB — would make
// a naive compLine[i:point] panic with a slice-bounds error. 2.9 guards the
// same case (halcmd_main.c: `if (c<0) c=0`).
func relevantCompLine(compLine string, point int) string {
	// Find end of the first word (the command name "halcmd").
	i := 0
	for i < len(compLine) && !unicode.IsSpace(rune(compLine[i])) {
		i++
	}
	// Skip whitespace after the command name.
	for i < len(compLine) && unicode.IsSpace(rune(compLine[i])) {
		i++
	}
	if point > len(compLine) {
		point = len(compLine)
	}
	if point < i {
		point = i
	}
	return compLine[i:point]
}

// runCompletion implements bash's "complete -C" protocol.
// Bash sets COMP_LINE (full command line) and COMP_POINT (cursor position).
// We parse the context, query the REST API, and print matching candidates to stdout.
func runCompletion() {
	compLine := os.Getenv("COMP_LINE")
	compPoint := os.Getenv("COMP_POINT")
	if compLine == "" || compPoint == "" {
		return
	}

	point := parseCompPoint(compPoint)
	line := relevantCompLine(compLine, point)

	_, candidates := completeHead(line)
	for _, c := range candidates {
		fmt.Println(c)
	}
}

// completeHead is the single completion entry point, shared by bash's
// "complete -C" protocol and the interactive line editor's TAB key. head is the
// halcmd command line left of the cursor (without the "halcmd" argv[0]). It
// returns the byte offset in head at which the word being completed starts,
// plus the candidates for that word.
func completeHead(head string) (int, []string) {
	// Skip any leading options (e.g. -k, -q, -s, -U <url>). skipOptions only
	// ever trims from the front, so the length difference is the offset of the
	// remaining text within head.
	line := skipOptions(head)
	offset := len(head) - len(line)

	start := lastWordStart(line)
	fragment := line[start:]
	words := splitWords(line[:start])

	var candidates []string
	if len(words) == 0 {
		// Completing the subcommand itself
		candidates = completeCommand(fragment)
	} else {
		// Completing an argument to a known subcommand
		cmd := strings.ToLower(words[0])
		argPos := len(words) // 1-based argument position
		candidates = completeArg(cmd, argPos, fragment, words[1:])
	}
	return offset + start, candidates
}

// lastWordStart returns the byte offset at which the last word of s begins,
// honouring the same simple quoting rule as splitWords. A line ending in
// whitespace has an empty last word, i.e. the offset is len(s).
func lastWordStart(s string) int {
	start := 0
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c == '"' || c == '\'') && !inQuote:
			inQuote = true
			quoteChar = c
		case inQuote && c == quoteChar:
			inQuote = false
		case !inQuote && (c == ' ' || c == '\t'):
			start = i + 1
		}
	}
	return start
}

// skipOptions strips leading halcmd options from the line (e.g. -k -q -s -U url).
func skipOptions(line string) string {
	for {
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
		if !strings.HasPrefix(line, "-") {
			return line
		}
		// Find end of option
		end := strings.IndexFunc(line, unicode.IsSpace)
		if end == -1 {
			// Option at end of line with no argument yet — might be the subcommand
			if line == "-" || (len(line) > 1 && line[1] != '-') {
				return ""
			}
			return line
		}
		opt := line[:end]
		line = line[end:]

		// Options that take an argument
		switch opt {
		case "-f", "-U":
			// Skip the argument too
			line = strings.TrimLeftFunc(line, unicode.IsSpace)
			end = strings.IndexFunc(line, unicode.IsSpace)
			if end == -1 {
				return ""
			}
			line = line[end:]
		}
	}
}

// splitWords splits text on whitespace, respecting simple quoting.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range s {
		switch {
		case (r == '"' || r == '\'') && !inQuote:
			inQuote = true
			quoteChar = r
		case inQuote && r == quoteChar:
			inQuote = false
		case unicode.IsSpace(r) && !inQuote:
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// --- Subcommand list ---

var subcommands = []string{
	"show", "list", "status", "getp", "setp", "gets", "sets", "ptype", "stype",
	"newsig", "delsig", "net", "linksp", "linkps", "linkpp", "unlinkp",
	"load", "unload",
	"newthread", "delthread", "addf", "delf", "start", "stop",
	"alias", "unalias", "lock", "unlock", "debug", "save",
	"retain", "unretain",
	"source", "echo", "unecho", "help", "quit", "exit",
}

func completeCommand(prefix string) []string {
	return filterPrefix(subcommands, prefix)
}

// --- Argument completion dispatch ---

func completeArg(cmd string, argPos int, prefix string, prevArgs []string) []string {
	switch cmd {
	// Commands with keyword arg1
	case "show":
		if argPos == 1 {
			return filterPrefix(showKeywords, prefix)
		}
		return completeByShowType(prevArgs, prefix)
	case "list":
		if argPos == 1 {
			return filterPrefix(listKeywords, prefix)
		}
		return completeByShowType(prevArgs, prefix)
	case "save":
		if argPos == 1 {
			return filterPrefix(saveKeywords, prefix)
		}
	case "status":
		if argPos == 1 {
			return filterPrefix(statusKeywords, prefix)
		}
	case "lock":
		if argPos == 1 {
			return filterPrefix(lockKeywords, prefix)
		}
	case "unlock":
		if argPos == 1 {
			return filterPrefix(unlockKeywords, prefix)
		}

	// Pin/param value access
	case "getp", "ptype":
		if argPos == 1 {
			return completePinsAndParams(prefix)
		}
	case "setp":
		if argPos == 1 {
			return completeWritablePinsAndParams(prefix)
		}

	// Signal access
	case "gets", "sets", "stype", "delsig", "retain", "unretain":
		if argPos == 1 {
			return completeSignals(prefix)
		}

	// Newsig: arg2 is type
	case "newsig":
		if argPos == 2 {
			return filterPrefix(pinTypes, prefix)
		}

	// Net: arg1=signal, arg2+=pins (type-matched)
	case "net":
		if argPos == 1 {
			return completeSignals(prefix)
		}
		return completeTypedPinsForSignal(prevArgs, prefix)

	// Link commands
	case "linksp":
		if argPos == 1 {
			return completeSignals(prefix)
		}
		if argPos == 2 {
			return completeTypedPinsForSignal(prevArgs, prefix)
		}
	case "linkps":
		if argPos == 1 {
			return completePins(prefix)
		}
		if argPos == 2 {
			return completeTypedSignalsForPin(prevArgs, prefix)
		}
	case "linkpp":
		if argPos == 1 {
			return completePins(prefix)
		}
		if argPos == 2 {
			return completeTypedPinsForPin(prevArgs, prefix)
		}
	case "unlinkp":
		if argPos == 1 {
			return completeLinkedPins(prefix)
		}

	// Module loading
	case "unload":
		if argPos == 1 {
			return completeComponents(prefix)
		}

	// Thread functions
	case "addf":
		if argPos == 1 {
			return completeUnusedFunctions(prefix)
		}
		if argPos == 2 {
			return completeThreads(prefix)
		}
	case "delf":
		if argPos == 1 {
			return completeUsedFunctions(prefix)
		}
		if argPos == 2 {
			return completeThreads(prefix)
		}

	// Alias
	case "alias":
		if argPos == 1 {
			return filterPrefix(aliasKeywords, prefix)
		}
		if argPos == 2 {
			if len(prevArgs) > 0 && prevArgs[0] == "param" {
				return completeParams(prefix)
			}
			return completePins(prefix)
		}
	case "unalias":
		if argPos == 1 {
			return filterPrefix(aliasKeywords, prefix)
		}
		if argPos == 2 {
			// Complete aliases — for simplicity, complete all pins/params
			if len(prevArgs) > 0 && prevArgs[0] == "param" {
				return completeParams(prefix)
			}
			return completePins(prefix)
		}

	// Threads
	case "delthread":
		if argPos == 1 {
			return completeThreads(prefix)
		}

	// Help
	case "help":
		if argPos == 1 {
			return completeCommand(prefix)
		}
	}

	return nil
}

// --- Keyword lists ---

var (
	showKeywords   = []string{"all", "alias", "comp", "pin", "sig", "param", "funct", "thread"}
	listKeywords   = []string{"comp", "alias", "pin", "sig", "param", "funct", "thread"}
	saveKeywords   = []string{"all", "alias", "comp", "sig", "link", "linka", "net", "neta", "param", "thread"}
	statusKeywords = []string{"alias", "lock", "mem", "all"}
	lockKeywords   = []string{"none", "tune", "all"}
	unlockKeywords = []string{"tune", "all"}
	aliasKeywords  = []string{"pin", "param"}
	pinTypes       = []string{"bit", "float", "s32", "u32"}
)

// --- Completion generators using REST API ---

func completePins(prefix string) []string {
	pattern := prefix + "*"
	pins, err := completionClient().ListPins(&pattern)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(pins))
	for _, p := range pins {
		names = append(names, p.Name)
	}
	return names
}

func completeLinkedPins(prefix string) []string {
	pattern := prefix + "*"
	pins, err := completionClient().ListPins(&pattern)
	if err != nil {
		return nil
	}
	var names []string
	for _, p := range pins {
		if p.Linked {
			names = append(names, p.Name)
		}
	}
	return names
}

func completeParams(prefix string) []string {
	pattern := prefix + "*"
	params, err := completionClient().ListParams(&pattern)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(params))
	for _, p := range params {
		names = append(names, p.Name)
	}
	return names
}

func completePinsAndParams(prefix string) []string {
	pins := completePins(prefix)
	params := completeParams(prefix)
	return append(pins, params...)
}

func completeWritablePinsAndParams(prefix string) []string {
	pattern := prefix + "*"
	var results []string

	params, err := completionClient().ListParams(&pattern)
	if err == nil {
		for _, p := range params {
			if p.Dir != "RO" {
				results = append(results, p.Name)
			}
		}
	}

	pins, err := completionClient().ListPins(&pattern)
	if err == nil {
		for _, p := range pins {
			// Settable pins: not linked and not output direction
			if !p.Linked && p.Dir != "OUT" {
				results = append(results, p.Name)
			}
		}
	}

	return results
}

func completeSignals(prefix string) []string {
	pattern := prefix + "*"
	sigs, err := completionClient().ListSignals(&pattern)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(sigs))
	for _, s := range sigs {
		names = append(names, s.Name)
	}
	return names
}

func completeComponents(prefix string) []string {
	pattern := prefix + "*"
	comps, err := completionClient().ListComponents(&pattern)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name)
	}
	return names
}

func completeThreads(prefix string) []string {
	pattern := prefix + "*"
	threads, err := completionClient().ListThreads(&pattern)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(threads))
	for _, t := range threads {
		names = append(names, t.Name)
	}
	return names
}

func completeUnusedFunctions(prefix string) []string {
	pattern := prefix + "*"
	funcs, err := completionClient().ListFunctions(&pattern)
	if err != nil {
		return nil
	}
	var names []string
	for _, f := range funcs {
		if f.Users == 0 {
			names = append(names, f.Name)
		}
	}
	return names
}

func completeUsedFunctions(prefix string) []string {
	pattern := prefix + "*"
	funcs, err := completionClient().ListFunctions(&pattern)
	if err != nil {
		return nil
	}
	var names []string
	for _, f := range funcs {
		if f.Users > 0 {
			names = append(names, f.Name)
		}
	}
	return names
}

// --- Type-matched completion ---

func completeTypedPinsForSignal(prevArgs []string, prefix string) []string {
	if len(prevArgs) == 0 {
		return completePins(prefix)
	}

	// Get signal type from first arg (signal name)
	sigName := prevArgs[0]
	sig, err := completionClient().GetSignal(sigName)
	if err != nil {
		// Signal might not exist yet (net creates it), fall back to all pins
		return completePins(prefix)
	}

	// Get pins matching prefix and filter by type compatibility
	pattern := prefix + "*"
	pins, err := completionClient().ListPins(&pattern)
	if err != nil {
		return nil
	}

	var names []string
	for _, p := range pins {
		if p.Type == sig.Type && !p.Linked {
			names = append(names, p.Name)
		}
	}
	return names
}

func completeTypedSignalsForPin(prevArgs []string, prefix string) []string {
	if len(prevArgs) == 0 {
		return completeSignals(prefix)
	}

	// Get pin type from first arg (pin name)
	pinName := prevArgs[0]
	pin, err := completionClient().GetPin(pinName)
	if err != nil {
		return completeSignals(prefix)
	}

	// Get signals matching prefix and filter by type
	pattern := prefix + "*"
	sigs, err := completionClient().ListSignals(&pattern)
	if err != nil {
		return nil
	}

	var names []string
	for _, s := range sigs {
		if s.Type == pin.Type {
			names = append(names, s.Name)
		}
	}
	return names
}

func completeTypedPinsForPin(prevArgs []string, prefix string) []string {
	if len(prevArgs) == 0 {
		return completePins(prefix)
	}

	// Get pin type from first arg
	pinName := prevArgs[0]
	pin, err := completionClient().GetPin(pinName)
	if err != nil {
		return completePins(prefix)
	}

	pattern := prefix + "*"
	pins, err := completionClient().ListPins(&pattern)
	if err != nil {
		return nil
	}

	var names []string
	for _, p := range pins {
		if p.Type == pin.Type && p.Name != pinName && !p.Linked {
			names = append(names, p.Name)
		}
	}
	return names
}

// --- show/list type-specific completion ---

func completeByShowType(prevArgs []string, prefix string) []string {
	if len(prevArgs) == 0 {
		return nil
	}
	what := strings.ToLower(prevArgs[0])
	switch what {
	case "pin", "pins":
		return completePins(prefix)
	case "sig", "signal", "signals":
		return completeSignals(prefix)
	case "param", "params", "parameter", "parameters":
		return completeParams(prefix)
	case "comp", "component", "components":
		return completeComponents(prefix)
	case "funct", "function", "functions":
		return completeFunctions(prefix)
	case "thread", "threads":
		return completeThreads(prefix)
	case "all":
		return completePinsAndParams(prefix)
	}
	return nil
}

func completeFunctions(prefix string) []string {
	pattern := prefix + "*"
	funcs, err := completionClient().ListFunctions(&pattern)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(funcs))
	for _, f := range funcs {
		names = append(names, f.Name)
	}
	return names
}

// --- Utility ---

func filterPrefix(items []string, prefix string) []string {
	if prefix == "" {
		return items
	}
	var result []string
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return result
}
