// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// ethercat — CLI tool for EtherCAT master diagnostics via REST API.
//
// Drop-in replacement for the IgH EtherCAT master "ethercat" command-line tool.
// Communicates via REST API instead of Unix socket / ioctl.
//
// Environment:
//
//	EC_INST       Instance name (default: "ethercat")
//	STMAK_REST_URL  REST endpoint (default: "http://localhost:5080/")
package main

import (
	"fmt"
	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/ethercatclient"
	"os"
	"sort"
	"strings"
)

// Verbosity levels matching the C++ tool.
type Verbosity int

const (
	Quiet   Verbosity = -1
	Normal  Verbosity = 0
	Verbose Verbosity = 1
)

// Global options parsed from command line.
type GlobalOpts struct {
	Masters    string // --master -m (default "-" = all)
	Positions  string // --position -p (default "-" = all)
	Aliases    string // --alias -a (default "-" = all)
	Domains    string // --domain -d (default "-" = all)
	DataType   string // --type -t
	Force      bool   // --force -f
	Emergency  bool   // --emergency -e
	Verbosity  Verbosity
	OutputFile string // --output-file -o
	Skin       string // --skin -s
}

// Command is a subcommand implementation.
type Command struct {
	Name  string
	Brief string
	Run   func(client *ethercatclient.EthercatClient, opts *GlobalOpts, args []string) error
}

var commands []*Command

func registerCommand(c *Command) {
	commands = append(commands, c)
}

func findCommand(name string) *Command {
	// Support abbreviation (like the C++ tool)
	var matches []*Command
	for _, c := range commands {
		if c.Name == name {
			return c
		}
		if strings.HasPrefix(c.Name, name) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return nil
}

func usage(progName string) {
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Name < commands[j].Name
	})

	maxWidth := 0
	for _, c := range commands {
		if len(c.Name) > maxWidth {
			maxWidth = len(c.Name)
		}
	}

	fmt.Fprintf(os.Stderr, "Usage: %s <COMMAND> [OPTIONS] [ARGUMENTS]\n\n", progName)
	fmt.Fprintf(os.Stderr, "Commands (can be abbreviated):\n")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-*s  %s\n", maxWidth, c.Name, c.Brief)
	}
	fmt.Fprintf(os.Stderr, `
Global options:
  --master  -m <master>  Comma separated list of masters
                         to select, ranges are allowed.
                         Examples: '1,3', '5-7,9', '-3'.
                         Default: '-' (all).
  --force   -f           Force a command.
  --quiet   -q           Output less information.
  --verbose -v           Output more information.
  --help    -h           Show this help.

Environment:
  EC_INST       EtherCAT instance name (default: "ethercat")
  STMAK_REST_URL  REST server URL (default: "http://localhost:5080/")

Numeric values can be specified as:
  - Decimal:     12345
  - Octal:       012345
  - Hexadecimal: 0x12345

Call '%s <COMMAND> --help' for command-specific help.
`, progName)
}

// parseArgs parses global options and the command name/arguments from the
// argument list. It accepts the getopt_long forms the IgH tool accepts:
// separated (-p 0), attached (-p0), long with '=' (--position=0), and clustered
// short options (-fq == -f -q, -fp0 == -f -p 0). "--" ends option processing.
// It sets help=true for -h/--help and returns an error on a malformed option.
func parseArgs(args []string) (opts *GlobalOpts, cmdName string, cmdArgs []string, help bool, err error) {
	opts = &GlobalOpts{
		Masters:   "-",
		Positions: "-",
		Aliases:   "-",
		Domains:   "-",
		Verbosity: Normal,
	}

	// valuePtr returns the destination for a value-taking option (by its short
	// letter), or nil if the letter is not a value option.
	valuePtr := func(c byte) *string {
		switch c {
		case 'm':
			return &opts.Masters
		case 'p':
			return &opts.Positions
		case 'a':
			return &opts.Aliases
		case 'd':
			return &opts.Domains
		case 't':
			return &opts.DataType
		case 'o':
			return &opts.OutputFile
		case 's':
			return &opts.Skin
		}
		return nil
	}
	// setBool applies a boolean option by its short letter; returns false if the
	// letter is not a known boolean option.
	setBool := func(c byte) bool {
		switch c {
		case 'f':
			opts.Force = true
		case 'e':
			opts.Emergency = true
		case 'q':
			opts.Verbosity = Quiet
		case 'v':
			opts.Verbosity = Verbose
		case 'h':
			help = true
		default:
			return false
		}
		return true
	}
	longToShort := map[string]byte{
		"master": 'm', "position": 'p', "alias": 'a', "domain": 'd',
		"type": 't', "output-file": 'o', "skin": 's',
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// End of options; everything after is positional.
			for _, rest := range args[i+1:] {
				if cmdName == "" {
					cmdName = rest
				} else {
					cmdArgs = append(cmdArgs, rest)
				}
			}
			return
		case strings.HasPrefix(a, "--"):
			name := a[2:]
			val, hasVal := "", false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name, val, hasVal = name[:eq], name[eq+1:], true
			}
			if name == "help" {
				help = true
				continue
			}
			if short, ok := longToShort[name]; ok {
				p := valuePtr(short)
				if hasVal {
					*p = val
				} else {
					i++
					if i >= len(args) {
						err = fmt.Errorf("option '--%s' requires a value", name)
						return
					}
					*p = args[i]
				}
				continue
			}
			switch name {
			case "force":
				opts.Force = true
			case "emergency":
				opts.Emergency = true
			case "quiet":
				opts.Verbosity = Quiet
			case "verbose":
				opts.Verbosity = Verbose
			default:
				err = fmt.Errorf("unknown option '%s'", a)
				return
			}
		case len(a) > 1 && a[0] == '-':
			// Short option cluster: -v, -fq, -p0, -fp0 ...
			for j := 1; j < len(a); j++ {
				c := a[j]
				if p := valuePtr(c); p != nil {
					// A value-taking option consumes the rest of the token as
					// its value, or the next argument if the token ends here.
					if j+1 < len(a) {
						*p = a[j+1:]
					} else {
						i++
						if i >= len(args) {
							err = fmt.Errorf("option '-%c' requires a value", c)
							return
						}
						*p = args[i]
					}
					break
				}
				if !setBool(c) {
					err = fmt.Errorf("unknown option '-%c'", c)
					return
				}
			}
		default:
			if cmdName == "" {
				cmdName = a
			} else {
				cmdArgs = append(cmdArgs, a)
			}
		}
	}
	return
}

func main() {
	progName := "ethercat"
	if len(os.Args) > 0 {
		parts := strings.Split(os.Args[0], "/")
		progName = parts[len(parts)-1]
	}

	opts, cmdName, cmdArgs, help, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v.\n", err)
		os.Exit(1)
	}
	if help {
		usage(progName)
		os.Exit(0)
	}
	if cmdName == "" {
		usage(progName)
		os.Exit(1)
	}

	cmd := findCommand(cmdName)
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "Error: Unknown command '%s'.\n", cmdName)
		os.Exit(1)
	}

	// Create client from environment.
	restURL := os.Getenv("STMAK_REST_URL")
	if restURL == "" {
		restURL = "http://localhost:5080/"
	}
	instance := os.Getenv("EC_INST")
	if instance == "" {
		instance = "ethercat"
	}

	client := ethercatclient.NewEthercatClientInstance(restURL, instance)

	if err := cmd.Run(client, opts, cmdArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
