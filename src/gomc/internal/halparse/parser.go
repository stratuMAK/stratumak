// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package halparse

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/sittner/linuxcnc/src/gomc/internal/calibreg"
	hal "github.com/sittner/linuxcnc/src/gomc/pkg/hal"
)

// stripComments removes a '#' comment (to end of line) from a single line,
// respecting single- and double-quoted strings — a '#' inside quotes is NOT a
// comment. It mirrors 2.9's strip_comments and runs BEFORE variable
// substitution, so a '#' introduced by a substituted INI/ENV value (e.g. an INI
// value "abc#1") is not treated as a comment. Returns an error for an
// unterminated quoted string, matching 2.9's strip_comments (-1).
func stripComments(line string) (string, error) {
	const (
		normal = iota
		single
		double
	)
	state := normal
	for i := 0; i < len(line); i++ {
		switch state {
		case normal:
			switch line[i] {
			case '#':
				return line[:i], nil
			case '\'':
				state = single
			case '"':
				state = double
			}
		case single:
			if line[i] == '\'' {
				state = normal
			}
		case double:
			if line[i] == '"' {
				state = normal
			}
		}
	}
	if state != normal {
		return "", fmt.Errorf("unterminated quoted string")
	}
	return line, nil
}

// tokenizeLine splits a single HAL config line into raw string tokens. Comment
// removal (stripComments) and variable substitution (substituteVars) are done
// by the caller FIRST, matching 2.9's strip_comments → replace_vars → tokenize
// ordering — so this no longer treats '#' as a comment. Double- and
// single-quoted strings protect embedded whitespace; the surrounding quotes are
// removed but their contents are taken literally, and a backslash is an
// ordinary character everywhere (2.9's tokenize does NO escape processing).
// Returns an error for an unterminated quote (a quote can still appear in the
// post-substitution line via a substituted value).
func tokenizeLine(line string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inToken := false
	i := 0

	for i < len(line) {
		ch := line[i]

		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			// Whitespace ends current token
			if inToken {
				tokens = append(tokens, current.String())
				current.Reset()
				inToken = false
			}
			i++
			continue
		}

		if ch == '"' {
			// Double-quoted string — contents literal, no escape processing.
			inToken = true
			i++ // skip opening quote
			for {
				if i >= len(line) {
					return nil, fmt.Errorf("unterminated double quote")
				}
				c := line[i]
				if c == '"' {
					i++ // skip closing quote
					break
				}
				current.WriteByte(c)
				i++
			}
			continue
		}

		if ch == '\'' {
			// Single-quoted string — contents literal.
			inToken = true
			i++ // skip opening quote
			for {
				if i >= len(line) {
					return nil, fmt.Errorf("unterminated single quote")
				}
				c := line[i]
				if c == '\'' {
					i++ // skip closing quote
					break
				}
				current.WriteByte(c)
				i++
			}
			continue
		}

		// Regular character (backslash included — no escape processing).
		inToken = true
		current.WriteByte(ch)
		i++
	}

	if inToken {
		tokens = append(tokens, current.String())
	}

	return tokens, nil
}

// joinContinuationLines normalizes CRLF and joins a line ending in a backslash
// to the next with NO separator, matching 2.9's continuation loop
// (halcmd_main.c: strip the trailing '\' and concatenate the next line's text).
func joinContinuationLines(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\\\n", "")
	return content
}

// iniVarPattern matches [SECTION]KEY in a HAL line.
// Section names and keys may contain letters, digits, underscores, or hyphens
// (e.g. [JOINT_0]MIN-LIMIT, [DISPLAY]PYVCP-PANEL).
var iniVarPattern = regexp.MustCompile(`\[([A-Za-z0-9_-]+)\]([A-Za-z0-9_-]+)`)

// envVarPattern matches ${VARNAME} and $VARNAME in a HAL line.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// substituteVars replaces [SECTION]KEY with INI values and $ENV_VAR / ${ENV_VAR}
// with environment variable values. Called per-line AFTER stripComments and
// BEFORE tokenizing (2.9 order). A reference to a variable that does not exist
// is a hard error (2.9's replace_vars returns -5 for a missing INI var and -4
// for a missing env var, aborting the run) — this fails bring-up loudly on a
// typo'd [SEC]KEY instead of silently substituting "".
func substituteVars(line string, ini INILookup) (string, error) {
	var subErr error

	// Replace [SECTION]KEY with INI values.
	line = iniVarPattern.ReplaceAllStringFunc(line, func(match string) string {
		if subErr != nil {
			return match
		}
		groups := iniVarPattern.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		val, found, err := ini.Get(groups[1], groups[2])
		if err != nil {
			subErr = fmt.Errorf("INI variable %s not found: %w", match, err)
			return match
		}
		if !found {
			subErr = fmt.Errorf("INI variable %s not found", match)
			return match
		}
		return val
	})
	if subErr != nil {
		return "", subErr
	}

	// Replace ${VARNAME} and $VARNAME with environment variables.
	line = envVarPattern.ReplaceAllStringFunc(line, func(match string) string {
		if subErr != nil {
			return match
		}
		groups := envVarPattern.FindStringSubmatch(match)
		name := ""
		if len(groups) > 1 && groups[1] != "" {
			name = groups[1] // ${VARNAME}
		} else if len(groups) > 2 && groups[2] != "" {
			name = groups[2] // $VARNAME
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			subErr = fmt.Errorf("environment variable %s not found", name)
			return match
		}
		return val
	})
	if subErr != nil {
		return "", subErr
	}

	return line, nil
}

// --- calibreg interceptor helpers ---

// iniRef represents a [SECTION]KEY reference found in a raw HAL line.
type iniRef struct {
	Section string
	Key     string
}

// extractINIRefs returns all [SECTION]KEY patterns found in a string.
func extractINIRefs(s string) []iniRef {
	matches := iniVarPattern.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	refs := make([]iniRef, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 3 {
			refs = append(refs, iniRef{Section: m[1], Key: m[2]})
		}
	}
	return refs
}

// recordSetpMapping records an INI→pin mapping in calibreg if the raw setp
// line's value argument contained an INI reference. pinName is the resolved
// pin name, resolvedValue is the substituted value string.
func recordSetpMapping(rawLine, pinName, resolvedValue string) {
	// Tokenize the raw (pre-substitution) line to find INI refs in the value.
	rawTokens, err := tokenizeLine(rawLine)
	if err != nil || len(rawTokens) < 3 {
		return
	}
	// rawTokens[2] is the raw value token (may contain [SECTION]KEY).
	refs := extractINIRefs(rawTokens[2])
	if len(refs) == 0 {
		return
	}
	val, err2 := strconv.ParseFloat(resolvedValue, 64)
	if err2 != nil {
		// Non-numeric values are not tunable — skip.
		return
	}
	// Typically there's exactly one INI ref per setp value, but handle multiple.
	for _, ref := range refs {
		calibreg.Record(calibreg.IniPinMapping{
			Pin:      pinName,
			Section:  ref.Section,
			Key:      ref.Key,
			IniValue: val,
		})
	}
}

// --- helper converters ---

func parsePinType(s string, loc SourceLoc) (hal.PinType, *ParseError) {
	switch strings.ToLower(s) {
	case "bit":
		return hal.TypeBit, nil
	case "float":
		return hal.TypeFloat, nil
	case "s32":
		return hal.TypeS32, nil
	case "u32":
		return hal.TypeU32, nil
	case "port":
		return hal.TypePort, nil
	default:
		return 0, &ParseError{Loc: loc, Msg: fmt.Sprintf("unknown pin type: %q", s)}
	}
}

func parseLockLevelStr(s string, loc SourceLoc) (LockLevel, *ParseError) {
	switch strings.ToLower(s) {
	case "none":
		return LockNone, nil
	case "load":
		return LockLoad, nil
	case "config":
		return LockConfig, nil
	case "tune":
		return LockTune, nil
	case "params":
		return LockParams, nil
	case "run":
		return LockRun, nil
	case "all":
		return LockAll, nil
	default:
		return 0, &ParseError{Loc: loc, Msg: fmt.Sprintf("unknown lock level: %q", s)}
	}
}

func parseHalObjType(s string, loc SourceLoc) (HalObjType, *ParseError) {
	switch strings.ToLower(s) {
	case "pin":
		return ObjPin, nil
	case "sig":
		return ObjSig, nil
	case "param":
		return ObjParam, nil
	case "funct":
		return ObjFunct, nil
	case "thread":
		return ObjThread, nil
	case "comp":
		return ObjComp, nil
	case "all":
		return ObjAll, nil
	default:
		return 0, &ParseError{Loc: loc, Msg: fmt.Sprintf("unknown HAL object type: %q", s)}
	}
}

func parseSaveTypeStr(s string, loc SourceLoc) (SaveType, *ParseError) {
	switch strings.ToLower(s) {
	case "comp":
		return SaveComp, nil
	case "sig":
		return SaveSig, nil
	case "link":
		return SaveLink, nil
	case "net":
		return SaveNet, nil
	case "param":
		return SaveParam, nil
	case "thread":
		return SaveThread, nil
	case "all":
		return SaveAll, nil
	default:
		return 0, &ParseError{Loc: loc, Msg: fmt.Sprintf("unknown save type: %q", s)}
	}
}

func parseAliasKind(s string, loc SourceLoc) (AliasKind, *ParseError) {
	switch strings.ToLower(s) {
	case "pin":
		return AliasPin, nil
	case "param":
		return AliasParam, nil
	default:
		return 0, &ParseError{Loc: loc, Msg: fmt.Sprintf("alias: unknown kind %q (want \"pin\" or \"param\")", s)}
	}
}

// --- per-command parse functions ---

func parseNet(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) < 1 {
		return Token{}, &ParseError{Loc: loc, Msg: "net: missing signal name"}
	}
	tok := &NetToken{Signal: tokens[0]}
	for _, arg := range tokens[1:] {
		if arg == "=>" || arg == "<=" || arg == "<=>" {
			continue
		}
		tok.Pins = append(tok.Pins, arg)
	}
	return Token{Location: loc, Data: tok}, nil
}

func parseSetP(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("setp: expected 2 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &SetPToken{Name: tokens[0], Value: tokens[1]}}, nil
}

func parseSetS(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("sets: expected 2 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &SetSToken{Name: tokens[0], Value: tokens[1]}}, nil
}

func parseGetP(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("getp: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &GetPToken{Name: tokens[0]}}, nil
}

func parseGetS(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("gets: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &GetSToken{Name: tokens[0]}}, nil
}

func parseAddF(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) < 2 || len(tokens) > 3 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("addf: expected 2-3 arguments, got %d", len(tokens))}
	}
	tok := &AddFToken{Funct: tokens[0], Thread: tokens[1], Pos: -1}
	if len(tokens) == 3 {
		n, err := strconv.Atoi(tokens[2])
		if err != nil {
			return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("addf: invalid position: %q", tokens[2])}
		}
		tok.Pos = n
	}
	return Token{Location: loc, Data: tok}, nil
}

func parseDelF(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("delf: expected 2 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &DelFToken{Funct: tokens[0], Thread: tokens[1]}}, nil
}

func parseNewSig(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("newsig: expected 2 arguments, got %d", len(tokens))}
	}
	pt, err := parsePinType(tokens[1], loc)
	if err != nil {
		return Token{}, err
	}
	return Token{Location: loc, Data: &NewSigToken{Name: tokens[0], SigType: pt}}, nil
}

func parseDelSig(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("delsig: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &DelSigToken{Name: tokens[0]}}, nil
}

// parseNewThread parses: newthread NAME PERIOD [fp|nofp] [cpu=N]
func parseNewThread(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) < 2 {
		return Token{}, &ParseError{Loc: loc, Msg: "newthread: requires at least NAME and PERIOD"}
	}
	period, err := strconv.ParseInt(tokens[1], 10, 64)
	if err != nil || period <= 0 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("newthread: invalid period %q", tokens[1])}
	}
	tok := &NewThreadToken{Name: tokens[0], Period: period, FP: 1, CPU: -1}
	for _, arg := range tokens[2:] {
		switch strings.ToLower(arg) {
		case "fp":
			tok.FP = 1
		case "nofp":
			tok.FP = 0
		default:
			if strings.HasPrefix(strings.ToLower(arg), "cpu=") {
				v, err := strconv.Atoi(arg[4:])
				if err != nil {
					return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("newthread: invalid cpu value %q", arg[4:])}
				}
				tok.CPU = v
			} else {
				return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("newthread: unknown option %q", arg)}
			}
		}
	}
	return Token{Location: loc, Data: tok}, nil
}

func parseDelThread(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("delthread: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &DelThreadToken{Name: tokens[0]}}, nil
}

func parseRetain(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("retain: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &RetainToken{Name: tokens[0]}}, nil
}

func parseUnretain(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("unretain: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &UnretainToken{Name: tokens[0]}}, nil
}

func parseLinkPS(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("linkps: expected 2 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &LinkPSToken{Pin: tokens[0], Sig: tokens[1]}}, nil
}

func parseLinkSP(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("linksp: expected 2 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &LinkSPToken{Sig: tokens[0], Pin: tokens[1]}}, nil
}

func parseLinkPP(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("linkpp: expected 2 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &LinkPPToken{Pin1: tokens[0], Pin2: tokens[1]}}, nil
}

func parseUnlinkP(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("unlinkp: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &UnlinkPToken{Pin: tokens[0]}}, nil
}

func parseAlias(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 3 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("alias: expected 3 arguments, got %d", len(tokens))}
	}
	kind, err := parseAliasKind(tokens[0], loc)
	if err != nil {
		return Token{}, err
	}
	return Token{Location: loc, Data: &AliasToken{Kind: kind, Name: tokens[1], Alias: tokens[2]}}, nil
}

func parseUnAlias(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 2 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("unalias: expected 2 arguments, got %d", len(tokens))}
	}
	kind, err := parseAliasKind(tokens[0], loc)
	if err != nil {
		return Token{}, err
	}
	return Token{Location: loc, Data: &UnAliasToken{Kind: kind, Name: tokens[1]}}, nil
}

func parseStart(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 0 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("start: expected 0 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &StartToken{}}, nil
}

func parseStop(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 0 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("stop: expected 0 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &StopToken{}}, nil
}

func parseLock(tokens []string, loc SourceLoc) (Token, *ParseError) {
	level := LockAll
	if len(tokens) == 1 {
		var err *ParseError
		level, err = parseLockLevelStr(tokens[0], loc)
		if err != nil {
			return Token{}, err
		}
	} else if len(tokens) > 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("lock: expected 0-1 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &LockToken{Level: level}}, nil
}

func parseUnlock(tokens []string, loc SourceLoc) (Token, *ParseError) {
	level := LockAll
	if len(tokens) == 1 {
		var err *ParseError
		level, err = parseLockLevelStr(tokens[0], loc)
		if err != nil {
			return Token{}, err
		}
	} else if len(tokens) > 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("unlock: expected 0-1 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &UnlockToken{Level: level}}, nil
}

func parseList(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) < 1 {
		return Token{}, &ParseError{Loc: loc, Msg: "list: missing object type"}
	}
	objType, err := parseHalObjType(tokens[0], loc)
	if err != nil {
		return Token{}, err
	}
	return Token{Location: loc, Data: &ListToken{ObjType: objType, Patterns: tokens[1:]}}, nil
}

func parseShow(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) == 0 {
		return Token{Location: loc, Data: &ShowToken{ObjType: ObjAll}}, nil
	}
	// "all" is also accepted as explicit type for show
	if strings.ToLower(tokens[0]) == "all" {
		return Token{Location: loc, Data: &ShowToken{ObjType: ObjAll, Patterns: tokens[1:]}}, nil
	}
	objType, err := parseHalObjType(tokens[0], loc)
	if err != nil {
		return Token{}, err
	}
	return Token{Location: loc, Data: &ShowToken{ObjType: objType, Patterns: tokens[1:]}}, nil
}

func parseSave(tokens []string, loc SourceLoc) (Token, *ParseError) {
	tok := &SaveToken{SaveType: SaveAll}
	if len(tokens) == 0 {
		return Token{Location: loc, Data: tok}, nil
	}
	st, err := parseSaveTypeStr(tokens[0], loc)
	if err != nil {
		return Token{}, err
	}
	tok.SaveType = st
	if len(tokens) >= 2 {
		tok.File = tokens[1]
	}
	return Token{Location: loc, Data: tok}, nil
}

func parseStatus(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 0 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("status: expected 0 arguments, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &StatusToken{}}, nil
}

func parseDebug(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("debug: expected 1 argument, got %d", len(tokens))}
	}
	n, err := strconv.Atoi(tokens[0])
	if err != nil {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("debug: invalid level: %q", tokens[0])}
	}
	return Token{Location: loc, Data: &DebugToken{Level: n}}, nil
}

func parsePType(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("ptype: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &PTypeToken{Name: tokens[0]}}, nil
}

func parseSType(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) != 1 {
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("stype: expected 1 argument, got %d", len(tokens))}
	}
	return Token{Location: loc, Data: &STypeToken{Name: tokens[0]}}, nil
}

func parseEcho(_ []string, loc SourceLoc) (Token, *ParseError) {
	return Token{Location: loc, Data: &EchoToken{}}, nil
}

func parseUnEcho(_ []string, loc SourceLoc) (Token, *ParseError) {
	return Token{Location: loc, Data: &UnEchoToken{}}, nil
}

func parsePrint(tokens []string, loc SourceLoc) (Token, *ParseError) {
	return Token{Location: loc, Data: &PrintToken{Message: strings.Join(tokens, " ")}}, nil
}

func parseLoad(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) < 1 {
		return Token{}, &ParseError{Loc: loc, Msg: "load: missing module path"}
	}
	tok := &LoadToken{
		Path: tokens[0],
	}
	rest := tokens[1:]
	// Check for optional <name1,name2,...> instance name list.
	if len(rest) > 0 && strings.HasPrefix(rest[0], "<") && strings.HasSuffix(rest[0], ">") {
		nameList := rest[0][1 : len(rest[0])-1] // strip angle brackets
		if nameList == "" {
			return Token{}, &ParseError{Loc: loc, Msg: "load: empty instance name list <>"}
		}
		names := strings.Split(nameList, ",")
		for i, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				return Token{}, &ParseError{Loc: loc, Msg: "load: empty name in instance name list"}
			}
			names[i] = n
		}
		tok.Names = names
		rest = rest[1:]
	}
	if len(rest) > 0 {
		tok.Args = rest
	}
	return Token{Location: loc, Data: tok}, nil
}

// parseLine dispatches a tokenized line to the appropriate per-command parse
// function. The command name (tokens[0]) is matched case-insensitively.
// The "source" command is NOT handled here — it is resolved by SingleFileParser.
func parseLine(tokens []string, loc SourceLoc) (Token, *ParseError) {
	if len(tokens) == 0 {
		return Token{}, &ParseError{Loc: loc, Msg: "empty token list"}
	}
	cmd := strings.ToLower(tokens[0])
	args := tokens[1:]

	switch cmd {
	case "net":
		return parseNet(args, loc)
	case "setp":
		return parseSetP(args, loc)
	case "sets":
		return parseSetS(args, loc)
	case "getp":
		return parseGetP(args, loc)
	case "gets":
		return parseGetS(args, loc)
	case "addf":
		return parseAddF(args, loc)
	case "delf":
		return parseDelF(args, loc)
	case "newsig":
		return parseNewSig(args, loc)
	case "delsig":
		return parseDelSig(args, loc)
	case "newthread":
		return parseNewThread(args, loc)
	case "delthread":
		return parseDelThread(args, loc)
	case "retain":
		return parseRetain(args, loc)
	case "unretain":
		return parseUnretain(args, loc)
	case "linkps":
		return parseLinkPS(args, loc)
	case "linksp":
		return parseLinkSP(args, loc)
	case "linkpp":
		return parseLinkPP(args, loc)
	case "unlinkp":
		return parseUnlinkP(args, loc)
	case "alias":
		return parseAlias(args, loc)
	case "unalias":
		return parseUnAlias(args, loc)
	case "start":
		return parseStart(args, loc)
	case "stop":
		return parseStop(args, loc)
	case "lock":
		return parseLock(args, loc)
	case "unlock":
		return parseUnlock(args, loc)
	case "list":
		return parseList(args, loc)
	case "show":
		return parseShow(args, loc)
	case "save":
		return parseSave(args, loc)
	case "status":
		return parseStatus(args, loc)
	case "debug":
		return parseDebug(args, loc)
	case "ptype":
		return parsePType(args, loc)
	case "stype":
		return parseSType(args, loc)
	case "echo":
		return parseEcho(args, loc)
	case "unecho":
		return parseUnEcho(args, loc)
	case "print":
		return parsePrint(args, loc)
	case "load":
		return parseLoad(args, loc)
	case "source":
		return Token{}, &ParseError{Loc: loc, Msg: "source: not a halcmd command (resolved at parse time)"}
	default:
		return Token{}, &ParseError{Loc: loc, Msg: fmt.Sprintf("unknown command: %q", tokens[0])}
	}
}

// SingleFileParser parses a single HAL file into a ParseResult. It handles
// template rendering, INI/ENV variable substitution, line continuation, and
// recursive source inclusion.
type SingleFileParser struct {
	ini          INILookup
	templateData *HalTemplateData
	resolver     PathResolver
	depth        int
	readFile     func(path string) (string, error) // injectable for testing
}

func (sp *SingleFileParser) readFileContent(path string) (string, error) {
	if sp.readFile != nil {
		return sp.readFile(path)
	}
	return defaultReadFile(path)
}

// defaultReadFile reads a HAL file from disk. It is the fallback used when no
// readFile shim is installed.
func defaultReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Parse reads and parses a HAL file, returning a classified ParseResult.
func (sp *SingleFileParser) Parse(path string) (*ParseResult, error) {
	if strings.HasSuffix(strings.ToLower(path), ".tcl") {
		return nil, &ParseError{Loc: SourceLoc{File: path, Line: 0}, Msg: fmt.Sprintf("source: %q is a Tcl file; use haltcl to execute Tcl HAL files", path)}
	}
	content, err := sp.readFileContent(path)
	if err != nil {
		return nil, err
	}

	// Template rendering (only when templateData is available)
	if strings.Contains(content, "{{") && sp.templateData != nil {
		content, err = RenderHalTemplate(path, content, sp.templateData)
		if err != nil {
			return nil, err
		}
	}

	// Join continuation lines before splitting
	content = joinContinuationLines(content)

	result := &ParseResult{}
	lines := strings.Split(content, "\n")

	for lineNum, line := range lines {
		loc := SourceLoc{File: path, Line: lineNum + 1}

		// 2.9 order: strip comments (quote-aware) → substitute vars → tokenize.
		line, sErr := stripComments(line)
		if sErr != nil {
			return nil, &ParseError{Loc: loc, Msg: sErr.Error()}
		}

		// Capture the comment-stripped, pre-substitution line for calibreg
		// (a [SECTION]KEY inside a comment must not be recorded).
		rawLine := line

		// Substitute INI and environment variables (missing var → hard error).
		if sp.ini != nil {
			var subErr error
			line, subErr = substituteVars(line, sp.ini)
			if subErr != nil {
				return nil, &ParseError{Loc: loc, Msg: subErr.Error()}
			}
		}

		// Tokenize the line
		tokens, tokErr := tokenizeLine(line)
		if tokErr != nil {
			return nil, &ParseError{Loc: loc, Msg: tokErr.Error()}
		}

		if len(tokens) == 0 {
			continue
		}

		// Record setp INI→pin mappings for emccalib discovery.
		if sp.ini != nil && len(tokens) >= 3 && strings.ToLower(tokens[0]) == "setp" {
			recordSetpMapping(rawLine, tokens[1], tokens[2])
		}

		// Handle "source" before parseLine (it is not a regular token)
		if strings.ToLower(tokens[0]) == "source" {
			if len(tokens) < 2 {
				return nil, &ParseError{Loc: loc, Msg: "source: missing file argument"}
			}
			if sp.depth >= 20 {
				return nil, &ParseError{Loc: loc, Msg: "source: maximum include depth exceeded"}
			}

			childPath := tokens[1]
			if sp.resolver != nil {
				childPath, err = sp.resolver.Resolve(tokens[1])
				if err != nil {
					return nil, &ParseError{Loc: loc, Msg: fmt.Sprintf("source: cannot resolve %q: %v", tokens[1], err)}
				}
			}

			child := &SingleFileParser{
				ini:          sp.ini,
				templateData: sp.templateData,
				resolver:     sp.resolver,
				depth:        sp.depth + 1,
				readFile:     sp.readFile,
			}
			childResult, parseErr := child.Parse(childPath)
			if parseErr != nil {
				return nil, parseErr
			}
			result.Loads = append(result.Loads, childResult.Loads...)
			result.HALCmd = append(result.HALCmd, childResult.HALCmd...)
			continue
		}

		// Parse the command line
		tok, parseErr := parseLine(tokens, loc)
		if parseErr != nil {
			return nil, parseErr
		}

		// Classify into the appropriate bucket
		switch tok.Data.(type) {
		case *LoadToken:
			result.Loads = append(result.Loads, tok)
		default:
			result.HALCmd = append(result.HALCmd, tok)
		}
	}

	return result, nil
}

// NewSingleFileParser creates a SingleFileParser with the given INILookup and
// PathResolver. Either argument may be nil.
func NewSingleFileParser(ini INILookup, resolver PathResolver) *SingleFileParser {
	var td *HalTemplateData
	if ini != nil {
		td = NewHalTemplateData(ini.GetAll())
	}
	return &SingleFileParser{ini: ini, templateData: td, resolver: resolver}
}

// ParseContent parses HAL commands from an in-memory string instead of a file.
// filename is used only for error-location reporting; no filesystem access is
// performed. This is useful for executing [HAL]HALCMD lines that are already
// held in memory.
func (sp *SingleFileParser) ParseContent(filename, content string) (*ParseResult, error) {
	// Temporarily install a readFile shim that returns our in-memory content
	// ONLY for the virtual filename.  Any other path (e.g. a file pulled in by a
	// `source` directive within content) must be read normally — otherwise the
	// in-memory content would be returned for the sourced file too, causing the
	// `source` line to be re-parsed recursively until the include-depth limit.
	saved := sp.readFile
	sp.readFile = func(path string) (string, error) {
		if path == filename {
			return content, nil
		}
		if saved != nil {
			return saved(path)
		}
		return defaultReadFile(path)
	}
	result, err := sp.Parse(filename)
	sp.readFile = saved
	return result, err
}

// MultiFileParser parses multiple HAL files and merges their ParseResults.
type MultiFileParser struct {
	ini          INILookup
	templateData *HalTemplateData
	resolver     PathResolver
}

// NewMultiFileParser creates a MultiFileParser with the given INILookup and
// PathResolver. Either argument may be nil.
func NewMultiFileParser(ini INILookup, resolver PathResolver) *MultiFileParser {
	var td *HalTemplateData
	if ini != nil {
		td = NewHalTemplateData(ini.GetAll())
	}
	return &MultiFileParser{ini: ini, templateData: td, resolver: resolver}
}

// Parse parses each file and returns a single merged ParseResult. The first
// error encountered stops processing and is returned immediately.
func (mp *MultiFileParser) Parse(paths []string) (*ParseResult, error) {
	result := &ParseResult{}
	for _, path := range paths {
		sp := &SingleFileParser{
			ini:          mp.ini,
			templateData: mp.templateData,
			resolver:     mp.resolver,
		}
		fileResult, err := sp.Parse(path)
		if err != nil {
			return nil, err
		}
		result.Loads = append(result.Loads, fileResult.Loads...)
		result.HALCmd = append(result.HALCmd, fileResult.HALCmd...)
	}
	return result, nil
}
