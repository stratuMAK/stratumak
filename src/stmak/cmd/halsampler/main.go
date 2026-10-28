// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// halsampler — reads HAL sample data from the stream_server WebSocket endpoint
// and writes it to stdout in the same format as the legacy halsampler.
//
// -t prints each sample's number, counted from the first sample received. When
// samples are lost (the reader fell more than the sampler's depth behind), the
// numbers jump by the count lost, a message goes to stderr at once, and the
// exit status is 1.
//
// -R creates the given file once the stream is subscribed: the server sends
// the header only after it has started this connection's read position, so
// from then on no sample is missed. Test drivers wait for the file before
// they start the HAL threads, instead of sleeping and hoping.
//
// Usage:
//   halsampler [-n num_samples] [-t] [-R ready_file] [instance_name]
//
// Environment:
//   STMAK_REST_URL — base URL of stmakd (default: http://127.0.0.1:5080)

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
	"github.com/stratuMAK/stratumak/src/stmak/internal/halstream"
)

const (
	defaultRestURL = "http://127.0.0.1:5080"
	envRestURL     = "STMAK_REST_URL"
)

func main() {
	var (
		numSamples int
		showTag    bool
		readyFile  string
	)

	flag.IntVar(&numSamples, "n", -1, "number of samples to capture (-1 = infinite)")
	flag.BoolVar(&showTag, "t", false, "print sample number")
	flag.StringVar(&readyFile, "R", "", "create this file once the stream is subscribed")
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

	// SIGTERM is how test drivers end a background sampler: it must still
	// flush and report losses through the exit status.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halsampler: connect failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.CloseNow() }()
	// A message is one poll result, up to 64 KiB (apiserver.PumpTransmit);
	// the library default of 32 KiB would end the stream at the first
	// backlog.
	conn.SetReadLimit(1 << 20)

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

	if readyFile != "" {
		if err := os.WriteFile(readyFile, nil, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "halsampler: ready file: %v\n", err)
			os.Exit(1)
		}
	}

	// Every sample costs a dozen small writes; unbuffered, that is ~10k
	// syscalls a second at a 1 kHz servo rate, enough to make this the slowest
	// link in the stream. Flushed after every message, so a reader polling the
	// output file still sees samples as they arrive.
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	var seq halstream.Sequence
	var lostTotal uint64
	// finish ends the run. Losing samples is a failure: the output then looks
	// like a continuous record with a hole in it, and every consumer of it is a
	// test or a diagnosis that would silently draw the wrong conclusion.
	finish := func() {
		if err := out.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "halsampler: write: %v\n", err)
			os.Exit(1)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "done")
		if lostTotal > 0 {
			fmt.Fprintf(os.Stderr, "halsampler: %d samples lost in total\n", lostTotal)
			os.Exit(1)
		}
		os.Exit(0)
	}

	for numSamples != 0 {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break // clean shutdown
			}
			_ = out.Flush()
			fmt.Fprintf(os.Stderr, "halsampler: read: %v\n", err)
			os.Exit(1)
		}

		segs, err := halstream.SplitSegments(data, sampleSize)
		if err != nil {
			_ = out.Flush()
			fmt.Fprintf(os.Stderr, "halsampler: %v\n", err)
			os.Exit(1)
		}
		for _, seg := range segs {
			lost, tag, err := seq.Advance(seg)
			if err != nil {
				_ = out.Flush()
				fmt.Fprintf(os.Stderr, "halsampler: %v\n", err)
				os.Exit(1)
			}
			if lost > 0 {
				// Reported as it happens, not only at exit: tests run this
				// in the background and may end it with a signal.
				_ = out.Flush()
				fmt.Fprintf(os.Stderr, "halsampler: lost %d samples before sample %d (the reader fell more than the sampler's depth behind)\n", lost, tag)
				lostTotal += uint64(lost)
			}
			for k := 0; k < seg.Count && numSamples != 0; k++ {
				sample := seg.Data[k*sampleSize:]
				// Write errors on out are sticky and surface at the Flush
				// after this message.
				if showTag {
					_, _ = fmt.Fprintf(out, "%d ", tag+uint64(k))
				}
				for i := 0; i < numPins; i++ {
					val, err := halstream.Decode(pinTypes[i], halstream.ReadRaw(sample, i))
					if err != nil {
						_ = out.Flush()
						fmt.Fprintf(os.Stderr, "halsampler: %v\n", err)
						os.Exit(1)
					}
					switch v := val.(type) {
					case float64:
						_, _ = fmt.Fprintf(out, "%f ", v)
					case bool:
						if v {
							_, _ = out.WriteString("1 ")
						} else {
							_, _ = out.WriteString("0 ")
						}
					default:
						_, _ = fmt.Fprintf(out, "%d ", v)
					}
				}
				_ = out.WriteByte('\n')
				if numSamples > 0 {
					numSamples--
				}
			}
		}
		if err := out.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "halsampler: write: %v\n", err)
			os.Exit(1)
		}
	}

	finish()
}
