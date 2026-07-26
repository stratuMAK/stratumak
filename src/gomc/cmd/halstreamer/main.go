// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// halstreamer — reads data from stdin and streams it to the HAL streamer
// component via the stream_server WebSocket endpoint.
//
// Usage:
//   halstreamer [-n num_lines] [instance_name] < data.txt
//
// Input format: space-separated values, one sample per line.
// Values are interpreted according to the pin configuration of the streamer.
//
// Environment:
//   GMC_REST_URL — base URL of gomc-server (default: http://127.0.0.1:5080)

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/coder/websocket"
	"github.com/sittner/linuxcnc/src/gomc/internal/halstream"
)

const (
	defaultRestURL = "http://127.0.0.1:5080"
	envRestURL     = "GMC_REST_URL"
)

func main() {
	var numLines int

	flag.IntVar(&numLines, "n", -1, "number of lines to send (-1 = all)")
	flag.Parse()

	instance := "streamer"
	if flag.NArg() > 0 {
		instance = flag.Arg(0)
	}

	restURL := os.Getenv(envRestURL)
	if restURL == "" {
		restURL = defaultRestURL
	}

	wsURL := halstream.HTTPToWS(restURL) + "/api/v1/stream/hal_streamer_stream/" + instance

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halstreamer: connect failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.CloseNow() }()

	// Read header from server: "cfg:<types>"
	_, headerMsg, err := conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halstreamer: read header: %v\n", err)
		os.Exit(1)
	}

	pinTypes, ok := halstream.ParseHeader(headerMsg)
	if !ok {
		fmt.Fprintf(os.Stderr, "halstreamer: unexpected header: %s\n", string(headerMsg))
		os.Exit(1)
	}
	numPins := len(pinTypes)

	scanner := bufio.NewScanner(os.Stdin)
	lineNum := 0

	for scanner.Scan() {
		if numLines >= 0 && lineNum >= numLines {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		// Blank and '#' comment lines are skipped, as in the classic
		// halstreamer and in the filestream cmod that replays the same capture
		// files. Only blanks were skipped here, so feeding back a file with a
		// header comment aborted with "expected N values, got 1".
		if line == "" || line[0] == '#' {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < numPins {
			fmt.Fprintf(os.Stderr, "halstreamer: line %d: expected %d values, got %d\n",
				lineNum+1, numPins, len(fields))
			os.Exit(1)
		}

		// Encode one sample as binary (numPins * ValueSize bytes)
		buf := make([]byte, numPins*halstream.ValueSize)
		for i := 0; i < numPins; i++ {
			raw, err := halstream.Encode(pinTypes[i], fields[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "halstreamer: line %d pin %d: %v\n",
					lineNum+1, i, err)
				os.Exit(1)
			}
			halstream.WriteRaw(buf, i, raw)
		}

		err := conn.Write(ctx, websocket.MessageBinary, buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			fmt.Fprintf(os.Stderr, "halstreamer: write: %v\n", err)
			os.Exit(1)
		}

		lineNum++
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "halstreamer: stdin read error: %v\n", err)
		os.Exit(1)
	}

	_ = conn.Close(websocket.StatusNormalClosure, "done")
}
