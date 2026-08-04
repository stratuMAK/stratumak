// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// halsampler — reads HAL sample data from the stream_server WebSocket endpoint
// and writes it to stdout in the same format as the legacy halsampler.
//
// Usage:
//   halsampler [-n num_samples] [-t] [instance_name]
//
// Environment:
//   GMC_REST_URL — base URL of stmakd (default: http://127.0.0.1:5080)

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/coder/websocket"
	"github.com/stratuMAK/stratumak/src/stmak/internal/halstream"
)

const (
	defaultRestURL = "http://127.0.0.1:5080"
	envRestURL     = "GMC_REST_URL"
)

func main() {
	var (
		numSamples int
		showTag    bool
	)

	flag.IntVar(&numSamples, "n", -1, "number of samples to capture (-1 = infinite)")
	flag.BoolVar(&showTag, "t", false, "print sample number")
	flag.Parse()

	instance := "sampler"
	if flag.NArg() > 0 {
		instance = flag.Arg(0)
	}

	restURL := os.Getenv(envRestURL)
	if restURL == "" {
		restURL = defaultRestURL
	}

	// Convert http(s) URL to ws(s) URL
	wsURL := halstream.HTTPToWS(restURL) + "/api/v1/stream/hal_sampler_stream/" + instance

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halsampler: connect failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.CloseNow() }()

	// The first message from the server is a header with pin types
	// Format: "cfg:<types>" e.g. "cfg:uffb"
	// We read this to know how to decode subsequent binary frames.
	_, headerMsg, err := conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halsampler: read header: %v\n", err)
		os.Exit(1)
	}

	pinTypes, ok := halstream.ParseHeader(headerMsg)
	if !ok {
		fmt.Fprintf(os.Stderr, "halsampler: unexpected header: %s\n", string(headerMsg))
		os.Exit(1)
	}
	numPins := len(pinTypes)
	sampleSize := numPins * halstream.ValueSize

	sampleNum := 0
	for numSamples != 0 {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break // clean shutdown
			}
			fmt.Fprintf(os.Stderr, "halsampler: read: %v\n", err)
			os.Exit(1)
		}

		// Process all samples in the frame
		for offset := 0; offset+sampleSize <= len(data); offset += sampleSize {
			if showTag {
				fmt.Printf("%d ", sampleNum)
			}

			for i := 0; i < numPins; i++ {
				val, err := halstream.Decode(pinTypes[i], halstream.ReadRaw(data[offset:], i))
				if err != nil {
					fmt.Fprintf(os.Stderr, "halsampler: %v\n", err)
					os.Exit(1)
				}
				switch v := val.(type) {
				case float64:
					fmt.Printf("%f ", v)
				case bool:
					if v {
						fmt.Print("1 ")
					} else {
						fmt.Print("0 ")
					}
				default:
					fmt.Printf("%d ", v)
				}
			}
			fmt.Println()

			sampleNum++
			if numSamples > 0 {
				numSamples--
				if numSamples == 0 {
					_ = conn.Close(websocket.StatusNormalClosure, "done")
					return
				}
			}
		}
	}

	_ = conn.Close(websocket.StatusNormalClosure, "done")
}
